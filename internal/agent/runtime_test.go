package agent

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConnector struct {
	conn Connection
}

func (f fakeConnector) Connect(_ Config, _ chan<- ConnectionEvent) (Connection, error) {
	return f.conn, nil
}

type fakeConnection struct {
	connected atomic.Bool
	drained   atomic.Bool
	closed    atomic.Bool
}

func newFakeConnection(connected bool) *fakeConnection {
	c := &fakeConnection{}
	c.connected.Store(connected)
	return c
}

func (c *fakeConnection) IsConnected() bool { return c.connected.Load() }
func (c *fakeConnection) Drain() error {
	c.drained.Store(true)
	return nil
}
func (c *fakeConnection) Close() { c.closed.Store(true) }

func TestRuntimeDrainsOnContextCancellation(t *testing.T) {
	cfg := validTestConfig()
	conn := newFakeConnection(true)
	ready := make(chan struct{})
	logger := slog.New(&readyHandler{ready: ready})
	runtime, err := NewRuntime(cfg, logger, fakeConnector{conn: conn})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("runtime did not establish the broker connection")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
	if !conn.drained.Load() {
		t.Fatal("connection was not drained")
	}
	if !conn.closed.Load() {
		t.Fatal("connection was not closed")
	}
}

func TestRuntimeFailsWhenInitialConnectionTimesOut(t *testing.T) {
	cfg := validTestConfig()
	cfg.StartupTimeout = 20 * time.Millisecond
	conn := newFakeConnection(false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, err := NewRuntime(cfg, logger, fakeConnector{conn: conn})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	err = runtime.Run(context.Background())
	if err == nil {
		t.Fatal("Run() error = nil, want startup timeout")
	}
	if !conn.drained.Load() || !conn.closed.Load() {
		t.Fatal("connection was not cleaned up after startup timeout")
	}
}

func TestNewRuntimeRequiresConnector(t *testing.T) {
	_, err := NewRuntime(validTestConfig(), nil, nil)
	if err == nil {
		t.Fatal("NewRuntime() error = nil, want connector validation error")
	}
}

type readyHandler struct {
	ready chan struct{}
	once  sync.Once
}

func (h *readyHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *readyHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "agent connected to event broker" {
		h.once.Do(func() { close(h.ready) })
	}
	return nil
}

func (h *readyHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *readyHandler) WithGroup(string) slog.Handler      { return h }
