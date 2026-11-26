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
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/capabilities"
	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/fakecontroller"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

const enrollmentToken = "enroll-0123456789abcdef0123456789abcdef"

func controllerKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

type env struct {
	t          *testing.T
	controller *fakecontroller.Controller
	stateFile  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	controller, err := fakecontroller.Start(context.Background(), controllerKey(1), enrollmentToken)
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
	return &env{t: t, controller: controller, stateFile: filepath.Join(t.TempDir(), "state", "state.json")}
}

func (e *env) config(mutate func(*config.Config)) config.Config {
	cfg := config.Config{
		Environment: config.Development,
		Server:      config.Server{URL: e.controller.URL()},
		StateFile:   e.stateFile, ControllerPublicKeys: []ed25519.PublicKey{e.controller.Key.Public().(ed25519.PublicKey)},
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
	agent, err := New(cfg, "test", testLogger(), nil)
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
		if err == nil || !strings.Contains(err.Error(), agentv1.ErrorEnrollmentRejected) || !errors.Is(err, ErrEnrollmentRejected) || !NeedsOperator(err) {
			t.Fatalf("Run() = %v, want enrollment rejection", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent kept running with a rejected enrollment token")
	}
}

func TestResponsesFromUntrustedControllerAreIgnored(t *testing.T) {
	e := newEnv(t)
	r := e.start(e.config(func(c *config.Config) {
		c.ControllerPublicKeys = []ed25519.PublicKey{controllerKey(7).Public().(ed25519.PublicKey)} // not the responder's key
	}))
	time.Sleep(2 * time.Second)
	if err := r.stop(t); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	agent, err := New(e.config(nil), "test", nil, nil)
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
		if !errors.Is(err, ErrReenrollmentRequired) || !NeedsOperator(err) {
			t.Fatalf("Run() = %v, want ErrReenrollmentRequired", err)
		}
	case <-time.After(15 * time.Second): // heartbeat is 5s
		t.Fatal("agent did not stop after lease revocation")
	}
	agent, err := New(e.config(nil), "test", nil, nil)
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

// TestCommandsWhileLeaseExpiredAreRedelivered forces the "lease expired"
// path: the agent leaves the command unacknowledged and reopens the stream,
// and the controller delivers the command again once the lease is renewed, so
// a paused agent loses no work.
func TestCommandsWhileLeaseExpiredAreRedelivered(t *testing.T) {
	e := newEnv(t)
	agent, err := New(e.config(nil), "test", testLogger(), nil)
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
	// Wait until commands flow, then expire the lease locally. The next
	// scheduled renewal (5s heartbeat) restores it.
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
		t.Fatalf("command left unacknowledged during lease expiry was not redelivered: %v", err)
	}
}

// The agent survives a controller outage: it reconnects by itself, receives
// the commands queued meanwhile, and its identity is kept.
func TestAgentSurvivesControllerRestart(t *testing.T) {
	e := newEnv(t)
	e.start(e.config(nil))
	agentID := e.waitServing(t)

	e.controller.Stop() // network loss / controller restart
	ctx := timeout(t, 40*time.Second)
	op, err := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // the agent notices and starts backing off
	if err := e.controller.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatalf("command queued during the outage was not delivered after reconnect: %v", err)
	}
	if n := len(e.controller.Agents()); n != 1 {
		t.Fatalf("agent re-registered: %d identities", n)
	}
}

// At-least-once delivery means repeats happen: the same operation queued a
// second time (under a new command ID) runs once, the agent's ledger drops it.
func TestRepeatedOperationRunsOnce(t *testing.T) {
	e := newEnv(t)
	e.controller.AckWait = 200 * time.Millisecond
	e.start(e.config(nil))
	agentID := e.waitServing(t)
	ctx := timeout(t, 30*time.Second)
	op, err := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.controller.Await(ctx, op, agentv1.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	// Queue the identical command again under a new command ID: the same
	// operation ID must not execute twice.
	if err := e.controller.Send(ctx, agentID, agentv1.Command{Type: agentv1.CommandOperationRequest, OperationID: op,
		Capability: agentv1.CapabilityRuntimeInfo, Input: json.RawMessage(`{}`), TimeoutMillis: 1000}); err != nil {
		t.Fatal(err)
	}
	next, _ := e.controller.Request(ctx, agentID, agentv1.CapabilityRuntimeInfo, map[string]any{}, time.Second)
	count := 0
	for {
		select {
		case result := <-e.controller.Results():
			if result.OperationID == op && result.Status == agentv1.StatusSucceeded {
				count++
			}
			if result.OperationID == next && result.Status == agentv1.StatusSucceeded {
				if count > 0 {
					t.Fatalf("operation %s ran again (%d extra results)", op, count)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("no result for the follow-up command")
		}
	}
}

// TLS is verified: with the controller's certificate trusted the agent
// enrols over https; without it, it never does and holds no identity.
func TestAgentVerifiesControllerCertificate(t *testing.T) {
	serverTLS, caPEM, err := fakecontroller.NewTLS()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := fakecontroller.Start(context.Background(), controllerKey(1), enrollmentToken, fakecontroller.WithTLS(serverTLS))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	e := &env{t: t, controller: controller, stateFile: filepath.Join(t.TempDir(), "state", "state.json")}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// Production rules apply: https is required and nothing is skipped.
	untrusted := e.config(func(c *config.Config) { c.Environment = config.Production })
	r := e.start(untrusted)
	time.Sleep(1500 * time.Millisecond)
	if len(controller.Agents()) != 0 {
		t.Fatal("agent enrolled with an untrusted certificate")
	}
	if err := r.stop(t); err != nil {
		t.Fatal(err)
	}

	e.start(e.config(func(c *config.Config) {
		c.Environment = config.Production
		c.Server.CAFile = caFile
	}))
	e.waitServing(t)
}

// The enrolment token and the lease are never logged, even at debug level.
func TestSecretsAreNotLogged(t *testing.T) {
	e := newEnv(t)
	var buf lockedBuffer
	agent, err := New(e.config(nil), "test", slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- agent.Run(ctx) }()
	e.waitServing(t)
	cancel()
	<-done
	if strings.Contains(buf.String(), enrollmentToken) {
		t.Fatal("the enrolment token was logged")
	}
	if strings.Contains(buf.String(), agent.store.Snapshot().Identity.LeaseID) {
		t.Fatal("the lease ID was logged")
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ADR-0004: the agent rewrites the metrics textfile while it runs and once
// more when it stops, so the file shows the final state.
func TestMetricsTextfile(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(t.TempDir(), "machinarchy_agent.prom")
	r := e.start(e.config(func(c *config.Config) {
		c.MetricsFile, c.MetricsInterval = path, 50*time.Millisecond
	}))
	e.waitServing(t)
	read := func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	deadline := time.Now().Add(10 * time.Second)
	text := read()
	for !strings.Contains(text, `machinarchy_agent_operations{phase="done",status="succeeded"} 1`) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		text = read()
	}
	for _, want := range []string{
		"machinarchy_agent_enrolled 1\n", "machinarchy_agent_controller_connected 1\n",
		`machinarchy_agent_operations{phase="done",status="succeeded"} 1` + "\n",
		"machinarchy_agent_dispatch_queue_capacity 8\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
	for _, zero := range []string{"machinarchy_agent_lease_renewed_timestamp_seconds 0\n", "machinarchy_agent_lease_expiry_timestamp_seconds 0\n"} {
		if strings.Contains(text, zero) {
			t.Fatalf("lease not reported (%q):\n%s", zero, text)
		}
	}
	if err := r.stop(t); err != nil {
		t.Fatal(err)
	}
	if text := read(); !strings.Contains(text, "machinarchy_agent_controller_connected 0\n") {
		t.Fatalf("after stop the file still shows a connection:\n%s", text)
	}
}

// Phase 16: an agent pinning two keys works with a controller that signs with
// either, so the controller can switch keys without stopping agents.
func TestAgentAcceptsEitherPinnedControllerKey(t *testing.T) {
	e := newEnv(t)
	e.start(e.config(func(c *config.Config) {
		// The controller signs with the second pinned key ("next").
		c.ControllerPublicKeys = []ed25519.PublicKey{controllerKey(7).Public().(ed25519.PublicKey), e.controller.Key.Public().(ed25519.PublicKey)}
	}))
	e.waitServing(t)
}
