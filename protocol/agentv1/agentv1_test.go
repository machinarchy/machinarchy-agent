package agentv1

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden fixtures")

func testKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func sampleCommand() *Command {
	return &Command{
		ProtocolVersion: ProtocolVersion, CommandID: "cmd-1", AgentID: "agent-1", Type: CommandOperationRequest,
		IssuedAt: fixedTime, ExpiresAt: fixedTime.Add(5 * time.Minute), OperationID: "op-1",
		Capability: CapabilityServiceStatus, Input: json.RawMessage(`{"service":"nginx.service"}`), TimeoutMillis: 30000,
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(1)
	raw, err := Seal(key, KindCommand, sampleCommand())
	if err != nil {
		t.Fatal(err)
	}
	var got Command
	if err := Open(key.Public().(ed25519.PublicKey), KindCommand, raw, MaxCommandMessageBytes, &got); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got.OperationID != "op-1" || string(got.Input) != `{"service":"nginx.service"}` {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestOpenRejectsForgeriesAndConfusion(t *testing.T) {
	key := testKey(1)
	public := key.Public().(ed25519.PublicKey)
	raw, err := Seal(key, KindCommand, sampleCommand())
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}

	tampered := envelope
	tampered.Body = bytes.Replace(append([]byte(nil), envelope.Body...), []byte("op-1"), []byte("op-2"), 1)
	tamperedRaw, _ := json.Marshal(tampered)

	relabelled := envelope
	relabelled.Kind = KindResult
	relabelledRaw, _ := json.Marshal(relabelled)

	cases := map[string]struct {
		key  ed25519.PublicKey
		kind Kind
		raw  []byte
		want error
	}{
		"wrong key":         {testKey(2).Public().(ed25519.PublicKey), KindCommand, raw, ErrInvalidSignature},
		"tampered body":     {public, KindCommand, tamperedRaw, ErrInvalidSignature},
		"kind confusion":    {public, KindResult, relabelledRaw, ErrInvalidSignature},
		"wrong kind":        {public, KindResult, raw, ErrInvalidMessage},
		"unknown field":     {public, KindCommand, []byte(strings.Replace(string(raw), `"v":1`, `"v":1,"x":1`, 1)), ErrInvalidMessage},
		"trailing data":     {public, KindCommand, append(append([]byte(nil), raw...), []byte(" {}")...), ErrInvalidMessage},
		"oversized":         {public, KindCommand, bytes.Repeat([]byte(" "), MaxCommandMessageBytes+1), ErrInvalidMessage},
		"unsupported major": {public, KindCommand, []byte(strings.Replace(string(raw), `"v":1`, `"v":2`, 1)), ErrInvalidMessage},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out Command
			err := Open(tc.key, tc.kind, tc.raw, MaxCommandMessageBytes, &out)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open() error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestOpenRegisterRequestRequiresProofOfPossession(t *testing.T) {
	agentKey := testKey(3)
	request := &RegisterRequest{
		ProtocolVersion: ProtocolVersion, RequestID: "req-1", EnrollmentToken: strings.Repeat("t", 32),
		AgentPublicKey: agentKey.Public().(ed25519.PublicKey), Hostname: "host", AgentVersion: "1.0.0",
		OS: "linux", Architecture: "amd64", Capabilities: []string{CapabilityRuntimeInfo}, SentAt: fixedTime,
	}
	raw, err := Seal(agentKey, KindRegisterRequest, request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRegisterRequest(raw)
	if err != nil || got.RequestID != "req-1" {
		t.Fatalf("OpenRegisterRequest() = %+v, %v", got, err)
	}
	// Signed by a key other than the one declared in the body.
	forged, err := Seal(testKey(4), KindRegisterRequest, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRegisterRequest(forged); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("forged registration error = %v, want ErrInvalidSignature", err)
	}
}

func TestCommandValidation(t *testing.T) {
	mutate := func(change func(*Command)) *Command { c := sampleCommand(); change(c); return c }
	invalid := map[string]*Command{
		"bad version":       mutate(func(c *Command) { c.ProtocolVersion = 2 }),
		"bad agent id":      mutate(func(c *Command) { c.AgentID = "a.b" }),
		"lifetime too long": mutate(func(c *Command) { c.ExpiresAt = c.IssuedAt.Add(MaxCommandLifetime + time.Second) }),
		"expires first":     mutate(func(c *Command) { c.ExpiresAt = c.IssuedAt }),
		"unknown type":      mutate(func(c *Command) { c.Type = "exec" }),
		"invalid input":     mutate(func(c *Command) { c.Input = json.RawMessage(`{`) }),
		"no timeout":        mutate(func(c *Command) { c.TimeoutMillis = 0 }),
		"cancel with input": mutate(func(c *Command) { c.Type = CommandOperationCancel }),
	}
	for name, command := range invalid {
		if err := command.Validate(); err == nil {
			t.Errorf("%s: Validate() succeeded", name)
		}
	}
	command := sampleCommand()
	if err := command.Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	if err := command.ValidateAt("agent-1", fixedTime.Add(time.Minute)); err != nil {
		t.Fatalf("ValidateAt() = %v", err)
	}
	for name, check := range map[string]func() error{
		"other agent": func() error { return command.ValidateAt("agent-2", fixedTime) },
		"expired":     func() error { return command.ValidateAt("agent-1", fixedTime.Add(6*time.Minute)) },
		"future":      func() error { return command.ValidateAt("agent-1", fixedTime.Add(-MaxClockSkew-time.Second)) },
	} {
		if check() == nil {
			t.Errorf("%s: ValidateAt() succeeded", name)
		}
	}
}

func TestResponsesRejectMixedErrorAndIdentity(t *testing.T) {
	lease := &Lease{LeaseID: "lease-1", ExpiresAt: fixedTime, HeartbeatIntervalMillis: 10000}
	response := &RegisterResponse{ProtocolVersion: 1, RequestID: "r", AgentID: "a", HostID: "h", Lease: lease, Error: &Error{Code: ErrorUnavailable}}
	if response.Validate() == nil {
		t.Fatal("response with both error and identity was accepted")
	}
	response.Error = nil
	if err := response.Validate(); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	response.Lease = &Lease{LeaseID: "lease-1", ExpiresAt: fixedTime, HeartbeatIntervalMillis: 100}
	if response.Validate() == nil {
		t.Fatal("heartbeat interval below minimum was accepted")
	}
}

func TestServiceValidation(t *testing.T) {
	for _, name := range []string{"nginx.service", "getty@tty1.service", "my_app-2.service"} {
		if err := ValidateServiceName(name); err != nil {
			t.Errorf("ValidateServiceName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", ".service", "-x.service", ".x.service", "a..b.service", "nginx", "nginx.socket", "a b.service", "a/b.service", "--help.service"} {
		if ValidateServiceName(name) == nil {
			t.Errorf("ValidateServiceName(%q) accepted", name)
		}
	}
	for _, action := range []string{"mask", "disable", "kill", "edit", "set-property", "--now", ""} {
		if ValidateServiceAction(action) == nil {
			t.Errorf("ValidateServiceAction(%q) accepted", action)
		}
	}
}

func TestSubjectsStayOutsideControllerNamespace(t *testing.T) {
	subject, err := CommandSubject("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{SubjectRegister, SubjectLeaseRenew, subject} {
		if strings.HasPrefix(s, "machinarchy.") {
			t.Fatalf("subject %q would be captured by the controller's machinarchy.> stream", s)
		}
	}
	if _, err := CommandSubject("a.b"); err == nil {
		t.Fatal("agent ID with a dot was accepted as a subject token")
	}
	if _, err := CommandConsumer(strings.Repeat("a", MaxIdentifierLength)); err == nil {
		t.Fatal("over-long consumer name accepted")
	}
}

// TestGoldenWireFormat pins the exact wire encoding. Ed25519 signatures are
// deterministic, so controller implementations can use these fixtures as
// contract tests. Regenerate with: go test ./protocol/... -run Golden -update
func TestGoldenWireFormat(t *testing.T) {
	controller := testKey(1)
	agent := testKey(2)
	completed := fixedTime.Add(time.Second)
	fixtures := map[string]func() ([]byte, error){
		"command.json": func() ([]byte, error) { return Seal(controller, KindCommand, sampleCommand()) },
		"result.json": func() ([]byte, error) {
			return Seal(agent, KindResult, &Result{ProtocolVersion: 1, AgentID: "agent-1", OperationID: "op-1",
				Capability: CapabilityServiceStatus, Status: StatusSucceeded, Output: json.RawMessage(`{"service":"nginx.service"}`),
				AcceptedAt: fixedTime, StartedAt: &fixedTime, CompletedAt: &completed})
		},
	}
	for name, build := range fixtures {
		t.Run(name, func(t *testing.T) {
			got, err := build()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("testdata", name)
			if *update {
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden file (run with -update to create): %v", err)
			}
			if !bytes.Equal(bytes.TrimSpace(want), got) {
				t.Fatalf("wire format changed for %s:\n got %s\nwant %s", name, got, want)
			}
		})
	}
	t.Logf("controller public key: %s", EncodePublicKey(controller.Public().(ed25519.PublicKey)))
}
