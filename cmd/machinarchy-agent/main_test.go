package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/app"
	"github.com/machinarchy/machinarchy-agent/internal/state"
	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

func testEnv(t *testing.T) func(string) string {
	key := agentv1.EncodePublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	values := map[string]string{
		"MACHINARCHY_ENVIRONMENT":                 "development",
		"MACHINARCHY_AGENT_NATS_URL":              "nats://127.0.0.1:4222",
		"MACHINARCHY_AGENT_CONTROLLER_PUBLIC_KEY": key,
		"MACHINARCHY_AGENT_STATE_FILE":            filepath.Join(t.TempDir(), "state", "state.json"),
	}
	return func(k string) string { return values[k] }
}

func TestFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut, func(string) string { return "" }); code != 0 || !strings.Contains(out.String(), "protocol v1") {
		t.Fatalf("--version = %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"--check-config"}, &out, &errOut, testEnv(t)); code != 0 {
		t.Fatalf("--check-config = %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"--check-config"}, &out, &errOut, func(string) string { return "" }); code != 2 || !strings.Contains(errOut.String(), "invalid configuration") {
		t.Fatalf("invalid config = %d %q", code, errOut.String())
	}
	out.Reset()
	env := testEnv(t)
	if code := run([]string{"--public-key"}, &out, &errOut, env); code != 0 {
		t.Fatalf("--public-key = %d %q", code, errOut.String())
	}
	first := strings.TrimSpace(out.String())
	if _, err := agentv1.ParsePublicKey(first); err != nil {
		t.Fatalf("--public-key printed %q: %v", first, err)
	}
	out.Reset()
	run([]string{"--public-key"}, &out, &errOut, env)
	if strings.TrimSpace(out.String()) != first {
		t.Fatal("agent key is not stable across runs")
	}
	if code := run([]string{"--unknown"}, &out, &errOut, env); code != 2 {
		t.Fatalf("unknown flag = %d", code)
	}
}

func TestExitCodeStopsSupervisorRestartsOnlyWhenOperatorMustAct(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{app.ErrReenrollmentRequired, exitNeedsOperator},
		{fmt.Errorf("lease: %w", app.ErrReenrollmentRequired), exitNeedsOperator},
		{fmt.Errorf("%w (%w)", app.ErrEnrollmentRejected, errors.New("enrollment_rejected")), exitNeedsOperator},
		{errors.New("persist lease: disk full"), 1},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// Q-24: the health check needs only the state file, not the configuration
// (no NATS URL, controller key or secrets).
func TestHealthcheckNeedsOnlyTheStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.json")
	env := func(k string) string {
		if k == "MACHINARCHY_AGENT_STATE_FILE" {
			return path
		}
		return ""
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"--healthcheck"}, &out, &errOut, env); code != 1 || strings.Contains(errOut.String(), "invalid configuration") {
		t.Fatalf("no state file = %d %q, want 1 without a configuration error", code, errOut.String())
	}
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"--healthcheck"}, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "not enrolled") {
		t.Fatalf("not enrolled = %d %q", code, errOut.String())
	}
	if err := store.Update(func(doc *state.Document) error {
		doc.Identity = &state.Identity{AgentID: "agent-1", HostID: "host-1", LeaseID: "lease-1",
			LeaseExpiresAt: time.Now().Add(time.Minute), HeartbeatIntervalMillis: 1000}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run([]string{"--healthcheck"}, &out, &errOut, env); code != 0 {
		t.Fatalf("enrolled with a lease = %d %q", code, errOut.String())
	}
}
