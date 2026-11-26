package metrics

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestRenderIsPrometheusText(t *testing.T) {
	r := &Recorder{}
	r.ControllerReconnected()
	r.ControllerDisconnected()
	r.ControllerDisconnected()
	r.LeaseRenewed(time.Unix(1700000000, 0))
	now := time.Unix(1700000100, 500_000_000)
	var out strings.Builder
	err := Render(&out, r, Gauges{
		Version: "1.2.3", Enrolled: true, ControllerConnected: true, LeaseExpiresAt: time.Unix(1700000060, 0),
		Operations:         map[OperationKey]int{{Phase: "running"}: 1, {Phase: "done", Status: "succeeded"}: 4, {Phase: "done", Status: "unknown"}: 1},
		ResultsAwaitingAck: 2, QueueDepth: 3, Running: 1, QueueSize: 32,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		`machinarchy_agent_info{version="1.2.3"} 1`,
		"machinarchy_agent_metrics_written_timestamp_seconds 1700000100.500",
		"machinarchy_agent_enrolled 1",
		"machinarchy_agent_controller_connected 1",
		"machinarchy_agent_controller_reconnects_total 1",
		"machinarchy_agent_controller_disconnects_total 2",
		"machinarchy_agent_lease_expiry_timestamp_seconds 1700000060.000",
		"machinarchy_agent_lease_renewed_timestamp_seconds 1700000000.000",
		`machinarchy_agent_operations{phase="done",status="succeeded"} 4`,
		`machinarchy_agent_operations{phase="done",status="unknown"} 1`,
		`machinarchy_agent_operations{phase="running",status=""} 1`,
		"machinarchy_agent_results_awaiting_ack 2",
		"machinarchy_agent_dispatch_queue_depth 3",
		"machinarchy_agent_dispatch_queue_capacity 32",
		"machinarchy_agent_dispatch_running 1",
		`machinarchy_agent_log_messages_total{level="error"} 0`,
		"machinarchy_agent_last_error_timestamp_seconds 0",
		"# TYPE machinarchy_agent_controller_reconnects_total counter",
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("missing %q", want)
		}
	}
	// Every sample line belongs to a declared metric; label order is stable.
	declared := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if name, ok := strings.CutPrefix(line, "# TYPE "); ok {
			declared[strings.Fields(name)[0]] = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.FieldsFunc(line, func(r rune) bool { return r == '{' || r == ' ' })[0]
		if !declared[name] {
			t.Errorf("sample before its TYPE: %q", line)
		}
	}
	var again strings.Builder
	_ = Render(&again, r, Gauges{Version: "1.2.3", Enrolled: true, ControllerConnected: true, LeaseExpiresAt: time.Unix(1700000060, 0),
		Operations:         map[OperationKey]int{{Phase: "running"}: 1, {Phase: "done", Status: "succeeded"}: 4, {Phase: "done", Status: "unknown"}: 1},
		ResultsAwaitingAck: 2, QueueDepth: 3, Running: 1, QueueSize: 32}, now)
	if again.String() != text {
		t.Error("output is not deterministic")
	}
	// Label values are escaped.
	var odd strings.Builder
	_ = Render(&odd, &Recorder{}, Gauges{Version: "v\"1\n"}, now)
	if !strings.Contains(odd.String(), `machinarchy_agent_info{version="v\"1\n"} 1`) {
		t.Errorf("version label not escaped: %s", odd.String())
	}
}

func TestHandlerCountsWarningsAndErrorsAtEveryLogLevel(t *testing.T) {
	r := &Recorder{}
	var out bytes.Buffer
	// The output is set to error only: warnings are still counted.
	logger := slog.New(Handler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelError}), r)).With("component", "x")
	logger.Info("ignored")
	logger.Warn("first")
	logger.Warn("second")
	logger.Error("bad")
	if r.warnings.Load() != 2 || r.errors.Load() != 1 || r.lastWarning.Load() == 0 || r.lastError.Load() == 0 {
		t.Fatalf("warnings %d errors %d", r.warnings.Load(), r.errors.Load())
	}
	if strings.Contains(out.String(), "first") || !strings.Contains(out.String(), "bad") || !strings.Contains(out.String(), "component=x") {
		t.Fatalf("log output changed: %q", out.String())
	}
}

func TestWriteFileIsAtomicAndReadableByTheExporter(t *testing.T) {
	old := syscall.Umask(0o077) // the systemd unit's UMask
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.prom")
	for _, content := range []string{"a 1\n", "a 2\n"} {
		if err := WriteFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("content = %q, %v", got, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != FileMode {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
	if err := WriteFile(filepath.Join(dir, "missing", "agent.prom"), []byte("x")); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestWriterWritesAtOnceOnEachTickAndAtTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.prom")
	var collected atomic.Int32
	connected := atomic.Bool{}
	connected.Store(true)
	w := &Writer{Path: path, Interval: 20 * time.Millisecond, Recorder: &Recorder{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Collect: func() Gauges { collected.Add(1); return Gauges{ControllerConnected: connected.Load()} }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for collected.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	connected.Store(false)
	cancel()
	<-done
	got, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(got), "machinarchy_agent_controller_connected 0\n") {
		t.Fatalf("final write = %q, %v", got, err)
	}
}

func TestWriterLogsAFailingPathOnce(t *testing.T) {
	var logs bytes.Buffer
	w := &Writer{Path: filepath.Join(t.TempDir(), "missing", "agent.prom"), Interval: 5 * time.Millisecond, Recorder: &Recorder{},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)), Collect: func() Gauges { return Gauges{} }}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	if n := strings.Count(logs.String(), "cannot write the metrics file"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logs.String())
	}
}

func TestValidPath(t *testing.T) {
	for path, ok := range map[string]bool{
		"/var/lib/node_exporter/textfile_collector/machinarchy_agent.prom": true,
		"relative/agent.prom": false, "/var/lib/agent.txt": false, "/var/lib/../agent.prom": false,
	} {
		if got := ValidPath(path) == nil; got != ok {
			t.Errorf("%s: valid = %v, want %v", path, got, ok)
		}
	}
}
