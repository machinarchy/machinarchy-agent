package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/fakecontroller"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

const enrollmentToken = "enroll-0123456789abcdef0123456789abcdef"

// startNATS runs a real, embedded NATS server with JetStream.
func startNATS(t *testing.T) string {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("NATS server did not start")
	}
	t.Cleanup(ns.Shutdown)
	return ns.ClientURL()
}

func controllerKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

type env struct {
	t          *testing.T
	url        string
	controller *fakecontroller.Controller
	stateFile  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	url := startNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controller, err := fakecontroller.Start(ctx, url, controllerKey(1), enrollmentToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	// The fake controller reports protocol violations it sees from the agent
	// (bad signatures, malformed messages); a passing test must cause none.
	t.Cleanup(func() {
		for {
			select {
			case err := <-controller.Errors():
				t.Errorf("fake controller reported: %v", err)
			default:
				return
			}
		}
	})
	return &env{t: t, url: url, controller: controller, stateFile: filepath.Join(t.TempDir(), "state", "state.json")}
}

func (e *env) config(mutate func(*config.Config)) config.Config {
	cfg := config.Config{
		Environment: config.Development,
		NATS: config.NATS{URLs: []string{e.url}, ClientName: "test-agent", ConnectTimeout: 2 * time.Second,
			ReconnectWait: 100 * time.Millisecond, DrainTimeout: 2 * time.Second},
		StateFile: e.stateFile, ControllerPublicKey: e.controller.Key.Public().(ed25519.PublicKey),
		EnrollmentToken: enrollmentToken, RequestTimeout: time.Second, ShutdownTimeout: time.Second,
		Workers: 2, QueueSize: 8, MaxOperationTimeout: time.Minute, MaxOutputBytes: 64 << 10,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		e.t.Fatalf("test config invalid: %v", err)
	}
	return cfg
}

// testLogger discards agent logs unless AGENT_TEST_LOG is set.
func testLogger() *slog.Logger {
	if os.Getenv("AGENT_TEST_LOG") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type running struct {
	cancel context.CancelFunc
	done   chan error
}

func (e *env) start(cfg config.Config) *running {
	e.t.Helper()
	agent, err := New(cfg, "test", testLogger())
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- agent.Run(ctx) }()
	e.t.Cleanup(func() { _ = r.stop(e.t) })
	return r
}

func (r *running) stop(t *testing.T) error {
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	r.cancel = nil
	select {
	case err := <-r.done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not stop")
		return nil
	}
}

// waitServing blocks until the agent has registered, persisted its identity
// and is consuming commands, proven by a full runtime.info round trip.
// WaitForAgent alone only proves the controller recorded the registration;
// the agent may still be waiting for the response.
func (e *env) waitServing(t *testing.T) string {
	t.Helper()
	agentID, err := e.controller.WaitForAgent(timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := timeout(t, 20*time.Second)
	op, err := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	return agentID
}

func timeout(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func TestAgentEndToEnd(t *testing.T) {
	e := newEnv(t)
	systemctl := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\nprintf '%s\\n' Id=nginx.service LoadState=loaded ActiveState=active SubState=running\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.start(e.config(func(c *config.Config) {
		c.ServiceEnabled = true
		c.Services = capabilities.ServiceOptions{SystemctlPath: systemctl, Allowed: []string{"nginx.service"}}
	}))
	agentID, err := e.controller.WaitForAgent(timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := timeout(t, 20*time.Second)

	op, err := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	var info agentv1.RuntimeInfo
	if err := json.Unmarshal(result.Output, &info); err != nil || info.AgentVersion != "test" {
		t.Fatalf("runtime.info output = %s (%v)", result.Output, err)
	}
	if !strings.Contains(strings.Join(info.Capabilities, ","), agentv1.CapabilityServiceStatus) {
		t.Fatalf("service.status not advertised: %v", info.Capabilities)
	}

	op, _ = e.controller.Request(ctx, agentID, agentv1.CapabilitySystemFacts, map[string]any{}, 10*time.Second)
	if _, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatal(err)
	}

	op, _ = e.controller.Request(ctx, agentID, agentv1.CapabilityServiceStatus, agentv1.ServiceStatusRequest{Service: "nginx.service"}, 10*time.Second)
	result, err = e.controller.Await(ctx, op, agentv1.StatusSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	var status agentv1.ServiceStatus
	if err := json.Unmarshal(result.Output, &status); err != nil || status.ActiveState != "active" {
		t.Fatalf("service.status output = %s", result.Output)
	}

	// Disabled capability and invalid input are reported, not executed.
	op, _ = e.controller.Request(ctx, agentID, agentv1.CapabilityServiceControl, agentv1.ServiceControlRequest{Service: "nginx.service", Action: "stop"}, time.Second)
	if result, err = e.controller.Await(ctx, op, agentv1.StatusRejected); err != nil || result.Code != agentv1.CodeCapabilityUnavailable {
		t.Fatalf("service.control without opt-in = %+v, %v", result, err)
	}
	op, _ = e.controller.Request(ctx, agentID, agentv1.CapabilityServiceStatus, agentv1.ServiceStatusRequest{Service: "ssh.service"}, time.Second)
	if result, err = e.controller.Await(ctx, op, agentv1.StatusRejected); err != nil || result.Code != agentv1.CodeInvalidInput {
		t.Fatalf("unlisted service = %+v, %v", result, err)
	}
}

func TestAgentRejectsForgedAndMisaddressedCommands(t *testing.T) {
	e := newEnv(t)
	e.start(e.config(nil))
	agentID, err := e.controller.WaitForAgent(timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := timeout(t, 20*time.Second)
	input := json.RawMessage(`{}`)
	forged := agentv1.Command{Type: agentv1.CommandOperationRequest, OperationID: "forged", Capability: agentv1.CapabilityRuntimeInfo, Input: input, TimeoutMillis: 1000}
	if err := e.controller.SendSignedBy(ctx, controllerKey(9), agentID, forged); err != nil {
		t.Fatal(err)
	}
	misaddressed := agentv1.Command{Type: agentv1.CommandOperationRequest, OperationID: "misaddressed", AgentID: "agent-other",
		Capability: agentv1.CapabilityRuntimeInfo, Input: input, TimeoutMillis: 1000}
	if err := e.controller.Send(ctx, agentID, misaddressed); err != nil {
		t.Fatal(err)
	}
	// A genuine command sent afterwards proves the agent processed the queue.
	op, _ := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	for {
		select {
		case result := <-e.controller.Results():
			if result.OperationID == "forged" || result.OperationID == "misaddressed" {
				t.Fatalf("agent executed an untrusted command: %+v", result)
			}
			if result.OperationID == op && result.Status == agentv1.StatusSucceeded {
				return
			}
		case <-ctx.Done():
			t.Fatal("genuine command was not executed")
		}
	}
}

func TestAgentRestartKeepsIdentity(t *testing.T) {
	e := newEnv(t)
	first := e.start(e.config(nil))
	agentID := e.waitServing(t)
	if err := first.stop(t); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	// Restart without an enrollment token: the stored identity must suffice.
	e.start(e.config(func(c *config.Config) { c.EnrollmentToken = "" }))
	ctx := timeout(t, 20*time.Second)
	op, err := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if n := len(e.controller.Agents()); n != 1 {
		t.Fatalf("agent re-registered: %d identities", n)
	}
}

func TestEnrollmentRejectedStopsAgent(t *testing.T) {
	e := newEnv(t)
	r := e.start(e.config(func(c *config.Config) { c.EnrollmentToken = strings.Repeat("w", 40) }))
	select {
	case err := <-r.done:
		r.cancel = nil
		if err == nil || !strings.Contains(err.Error(), agentv1.ErrorEnrollmentRejected) {
			t.Fatalf("Run() = %v, want enrollment rejection", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent kept running with a rejected enrollment token")
	}
}

func TestResponsesFromUntrustedControllerAreIgnored(t *testing.T) {
	e := newEnv(t)
	r := e.start(e.config(func(c *config.Config) {
		c.ControllerPublicKey = controllerKey(7).Public().(ed25519.PublicKey) // not the responder's key
	}))
	time.Sleep(2 * time.Second)
	if err := r.stop(t); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	agent, err := New(e.config(nil), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if agent.store.Snapshot().Identity != nil {
		t.Fatal("agent accepted an identity from an untrusted responder")
	}
}

func TestRevokedLeaseClearsIdentity(t *testing.T) {
	e := newEnv(t)
	r := e.start(e.config(nil))
	e.waitServing(t)
	e.controller.RevokeRenewals.Store(true)
	select {
	case err := <-r.done:
		r.cancel = nil
		if !errors.Is(err, ErrReenrollmentRequired) {
			t.Fatalf("Run() = %v, want ErrReenrollmentRequired", err)
		}
	case <-time.After(15 * time.Second): // heartbeat is 5s
		t.Fatal("agent did not stop after lease revocation")
	}
	agent, err := New(e.config(nil), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if agent.store.Snapshot().Identity != nil {
		t.Fatal("revoked identity was kept")
	}
}

func TestShutdownIsBoundedWhileWaitingForController(t *testing.T) {
	e := newEnv(t)
	e.controller.Close() // nobody answers registration
	r := e.start(e.config(nil))
	time.Sleep(500 * time.Millisecond)
	began := time.Now()
	if err := r.stop(t); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
}

// TestCommandsNakedWhileLeaseExpiredAreRedelivered forces the "lease expired"
// nak path and checks JetStream redelivers the command once the lease is
// renewed, so a paused agent loses no work.
func TestCommandsNakedWhileLeaseExpiredAreRedelivered(t *testing.T) {
	e := newEnv(t)
	agent, err := New(e.config(nil), "test", testLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	agentID, err := e.controller.WaitForAgent(timeout(t, 10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the consumer is pulling, then expire the lease locally. The
	// next scheduled renewal (5s heartbeat) restores it.
	waitCtx := timeout(t, 30*time.Second)
	op, err := e.controller.Request(waitCtx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(waitCtx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	agent.leaseExpiry.Store(0)
	paused, err := e.controller.Request(waitCtx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(waitCtx, paused, agentv1.StatusSucceeded); err != nil {
		t.Fatalf("command nak'ed during lease expiry was not redelivered: %v", err)
	}
}
