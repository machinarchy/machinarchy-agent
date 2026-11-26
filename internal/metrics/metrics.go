// Package metrics exports the agent's health as a Prometheus textfile
// (ADR-0004). The agent opens no listener: it rewrites one file atomically on
// an interval, and node_exporter's textfile collector (or any reader) picks it
// up.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Recorder holds the values that events update. It is safe for concurrent
// use; the zero value is ready.
type Recorder struct {
	controllerReconnects  atomic.Uint64
	controllerDisconnects atomic.Uint64
	leaseRenewed          atomic.Int64 // unix nanoseconds
	warnings              atomic.Uint64
	errors                atomic.Uint64
	lastWarning           atomic.Int64 // unix nanoseconds
	lastError             atomic.Int64 // unix nanoseconds
}

// ControllerReconnected counts a reconnect.
func (r *Recorder) ControllerReconnected() { r.controllerReconnects.Add(1) }

// ControllerDisconnected counts a lost connection.
func (r *Recorder) ControllerDisconnected() { r.controllerDisconnects.Add(1) }

// LeaseRenewed records a successful registration or renewal.
func (r *Recorder) LeaseRenewed(at time.Time) { r.leaseRenewed.Store(at.UnixNano()) }

func (r *Recorder) logged(level slog.Level, at time.Time) {
	switch {
	case level >= slog.LevelError:
		r.errors.Add(1)
		r.lastError.Store(at.UnixNano())
	case level >= slog.LevelWarn:
		r.warnings.Add(1)
		r.lastWarning.Store(at.UnixNano())
	}
}

// Handler wraps next so every warning and error is counted, with its time.
// Every failure the agent reports goes through the logger, so this is the
// "last error time" without touching each call site.
func Handler(next slog.Handler, r *Recorder) slog.Handler { return &handler{next: next, r: r} }

type handler struct {
	next slog.Handler
	r    *Recorder
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn || h.next.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	at := record.Time
	if at.IsZero() {
		at = time.Now()
	}
	h.r.logged(record.Level, at)
	if !h.next.Enabled(ctx, record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{next: h.next.WithAttrs(attrs), r: h.r}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{next: h.next.WithGroup(name), r: h.r}
}

// Gauges are read at each write from the agent's current state.
type Gauges struct {
	Version string
	// Enrolled: the state file holds an identity.
	Enrolled bool
	// ControllerConnected: the connection to the controller is up.
	ControllerConnected bool
	// LeaseExpiresAt is zero before registration.
	LeaseExpiresAt time.Time
	// Operations counts the operations in the state file by phase and
	// result status ("" until the operation finishes).
	Operations map[OperationKey]int
	// ResultsAwaitingAck counts finished operations whose result the
	// controller has not acknowledged.
	ResultsAwaitingAck int
	// QueueDepth and Running describe the dispatcher; both 0 before it runs.
	QueueDepth int
	Running    int
	QueueSize  int
}

// OperationKey labels an operation count.
type OperationKey struct{ Phase, Status string }

// Render writes the Prometheus text exposition of r and g at now.
func Render(w io.Writer, r *Recorder, g Gauges, now time.Time) error {
	var b strings.Builder
	metric := func(name, kind, help string, samples ...string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
		for _, sample := range samples {
			b.WriteString(sample)
			b.WriteByte('\n')
		}
	}
	plain := func(name string, value any) string { return fmt.Sprintf("%s %v", name, value) }
	seconds := func(nanos int64) string {
		if nanos == 0 {
			return "0"
		}
		return fmt.Sprintf("%.3f", float64(nanos)/1e9)
	}

	metric("machinarchy_agent_info", "gauge", "Agent build; always 1.",
		fmt.Sprintf(`machinarchy_agent_info{version=%q} 1`, labelValue(g.Version)))
	metric("machinarchy_agent_metrics_written_timestamp_seconds", "gauge",
		"When this file was written. Alert when it is older than a few intervals: the agent is not running.",
		plain("machinarchy_agent_metrics_written_timestamp_seconds", seconds(now.UnixNano())))
	metric("machinarchy_agent_enrolled", "gauge", "1 when the agent holds an identity from the controller.",
		plain("machinarchy_agent_enrolled", boolValue(g.Enrolled)))
	metric("machinarchy_agent_controller_connected", "gauge", "1 while the connection to the controller is up.",
		plain("machinarchy_agent_controller_connected", boolValue(g.ControllerConnected)))
	metric("machinarchy_agent_controller_reconnects_total", "counter", "Controller reconnects since the agent started.",
		plain("machinarchy_agent_controller_reconnects_total", r.controllerReconnects.Load()))
	metric("machinarchy_agent_controller_disconnects_total", "counter", "Controller connections lost since the agent started.",
		plain("machinarchy_agent_controller_disconnects_total", r.controllerDisconnects.Load()))

	var expiry int64
	if !g.LeaseExpiresAt.IsZero() {
		expiry = g.LeaseExpiresAt.UnixNano()
	}
	metric("machinarchy_agent_lease_expiry_timestamp_seconds", "gauge",
		"When the controller lease expires; 0 before registration. Commands pause after it.",
		plain("machinarchy_agent_lease_expiry_timestamp_seconds", seconds(expiry)))
	metric("machinarchy_agent_lease_renewed_timestamp_seconds", "gauge",
		"Last successful registration or lease renewal; 0 if none since start. Lease age is time() minus this.",
		plain("machinarchy_agent_lease_renewed_timestamp_seconds", seconds(r.leaseRenewed.Load())))

	keys := make([]OperationKey, 0, len(g.Operations))
	for key := range g.Operations {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Phase != keys[j].Phase {
			return keys[i].Phase < keys[j].Phase
		}
		return keys[i].Status < keys[j].Status
	})
	var operations []string
	for _, key := range keys {
		operations = append(operations, fmt.Sprintf(`machinarchy_agent_operations{phase=%q,status=%q} %d`,
			labelValue(key.Phase), labelValue(key.Status), g.Operations[key]))
	}
	metric("machinarchy_agent_operations", "gauge",
		"Operations in the state file by phase (queued, running, done) and result status.", operations...)
	metric("machinarchy_agent_results_awaiting_ack", "gauge",
		"Finished operations whose result the controller has not acknowledged yet.",
		plain("machinarchy_agent_results_awaiting_ack", g.ResultsAwaitingAck))
	metric("machinarchy_agent_dispatch_queue_depth", "gauge", "Operations waiting for a worker.",
		plain("machinarchy_agent_dispatch_queue_depth", g.QueueDepth))
	metric("machinarchy_agent_dispatch_queue_capacity", "gauge", "Dispatch queue size (MACHINARCHY_AGENT_OPERATION_QUEUE_SIZE).",
		plain("machinarchy_agent_dispatch_queue_capacity", g.QueueSize))
	metric("machinarchy_agent_dispatch_running", "gauge", "Operations executing now.",
		plain("machinarchy_agent_dispatch_running", g.Running))

	metric("machinarchy_agent_log_messages_total", "counter", "Warnings and errors logged since the agent started.",
		fmt.Sprintf(`machinarchy_agent_log_messages_total{level="warn"} %d`, r.warnings.Load()),
		fmt.Sprintf(`machinarchy_agent_log_messages_total{level="error"} %d`, r.errors.Load()))
	metric("machinarchy_agent_last_warning_timestamp_seconds", "gauge", "Time of the last warning; 0 if none since start.",
		plain("machinarchy_agent_last_warning_timestamp_seconds", seconds(r.lastWarning.Load())))
	metric("machinarchy_agent_last_error_timestamp_seconds", "gauge", "Time of the last error; 0 if none since start.",
		plain("machinarchy_agent_last_error_timestamp_seconds", seconds(r.lastError.Load())))

	_, err := io.WriteString(w, b.String())
	return err
}

func boolValue(v bool) int {
	if v {
		return 1
	}
	return 0
}

// labelValue keeps label values printable and short; %q escapes the rest.
func labelValue(v string) string {
	if len(v) > 64 {
		v = v[:64]
	}
	return strings.ToValidUTF8(v, "?")
}

// FileMode is the textfile's mode: readable by the exporter, which runs as
// another user. It holds no secrets.
const FileMode = 0o644

// WriteFile replaces path atomically: a reader sees the old file or the new
// one, never a partial write.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create metrics file: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write metrics file: %w", err)
	}
	if err := tmp.Chmod(FileMode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod metrics file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close metrics file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		cleanup()
		return fmt.Errorf("replace metrics file: %w", err)
	}
	return nil
}

// Writer rewrites the textfile every Interval until its context ends, then
// once more so the file shows the final state.
type Writer struct {
	Path     string
	Interval time.Duration
	Recorder *Recorder
	Collect  func() Gauges
	Logger   *slog.Logger
	Now      func() time.Time
}

// Run writes until ctx ends. A failed write is logged once, and again only
// after a success, so a missing directory does not flood the log.
func (w *Writer) Run(ctx context.Context) {
	if w.Now == nil {
		w.Now = time.Now
	}
	failing := false
	write := func() {
		var b strings.Builder
		err := Render(&b, w.Recorder, w.Collect(), w.Now())
		if err == nil {
			err = WriteFile(w.Path, []byte(b.String()))
		}
		switch {
		case err != nil && !failing:
			failing = true
			w.Logger.Warn("cannot write the metrics file; retrying each interval", "path", w.Path, "error", err)
		case err == nil && failing:
			failing = false
			w.Logger.Info("metrics file written again", "path", w.Path)
		}
	}
	write()
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			write()
			return
		case <-ticker.C:
			write()
		}
	}
}

// ErrInvalidPath reports an unusable metrics file path.
var ErrInvalidPath = errors.New("metrics file must be an absolute path ending in .prom")

// ValidPath checks the path's form (node_exporter reads only *.prom).
func ValidPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".prom" || filepath.Clean(path) != path {
		return ErrInvalidPath
	}
	return nil
}
