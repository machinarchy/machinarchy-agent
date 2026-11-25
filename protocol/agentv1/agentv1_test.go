package agentv1

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"io"
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
		"register_response.json": func() ([]byte, error) {
			return Seal(controller, KindRegisterResponse, &RegisterResponse{ProtocolVersion: 1, RequestID: "req-1", AgentID: "agent-1",
				HostID: "host-1", Lease: &Lease{LeaseID: "lease-1", ExpiresAt: fixedTime.Add(time.Minute), HeartbeatIntervalMillis: 15000},
				NATSUserJWT: sampleNATSUserJWT})
		},
		"renew_response.json": func() ([]byte, error) {
			return Seal(controller, KindRenewResponse, &RenewResponse{ProtocolVersion: 1, RequestID: "req-2", AgentID: "agent-1",
				Lease:       &Lease{LeaseID: "lease-2", ExpiresAt: fixedTime.Add(2 * time.Minute), HeartbeatIntervalMillis: 15000},
				NATSUserJWT: sampleNATSUserJWT})
		},
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

func TestOpenFromAgentUsesRegisteredKey(t *testing.T) {
	agentKey := testKey(5)
	completed := fixedTime
	raw, err := Seal(agentKey, KindResult, &Result{ProtocolVersion: 1, AgentID: "agent-9", OperationID: "op",
		Status: StatusFailed, AcceptedAt: fixedTime, CompletedAt: &completed})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{"agent-9": agentKey.Public().(ed25519.PublicKey)}
	lookup := func(id string) (ed25519.PublicKey, bool) { key, ok := keys[id]; return key, ok }
	var result Result
	if err := OpenFromAgent(KindResult, raw, MaxResultMessageBytes, lookup, &result); err != nil {
		t.Fatalf("OpenFromAgent() = %v", err)
	}
	keys["agent-9"] = testKey(6).Public().(ed25519.PublicKey)
	if err := OpenFromAgent(KindResult, raw, MaxResultMessageBytes, lookup, &result); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("impersonation error = %v", err)
	}
	delete(keys, "agent-9")
	if err := OpenFromAgent(KindResult, raw, MaxResultMessageBytes, lookup, &result); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("unknown agent error = %v", err)
	}
}

// sampleNATSUserJWT has the shape of a NATS user JWT; agentv1 checks only the
// shape, the NATS server verifies it.
const sampleNATSUserJWT = "eyJ0eXAiOiJKV1QiLCJhbGciOiJlZDI1NTE5LW5rZXkifQ.eyJzdWIiOiJVQUJDIn0.c2lnbmF0dXJl"

func TestNATSUserJWTShape(t *testing.T) {
	lease := &Lease{LeaseID: "lease-1", ExpiresAt: fixedTime.Add(time.Minute), HeartbeatIntervalMillis: 15000}
	for name, tc := range map[string]struct {
		token string
		ok    bool
	}{
		"absent":           {"", true},
		"well formed":      {sampleNATSUserJWT, true},
		"two segments":     {"a.b", false},
		"empty segment":    {"a..c", false},
		"padding":          {"a.b=.c", false},
		"whitespace":       {"a.b c.d", false},
		"oversized":        {strings.Repeat("a", MaxNATSUserJWTBytes) + ".b.c", false},
		"standard base64+": {"a+b.c.d", false},
	} {
		t.Run(name, func(t *testing.T) {
			register := &RegisterResponse{ProtocolVersion: 1, RequestID: "r", AgentID: "a", HostID: "h", Lease: lease, NATSUserJWT: tc.token}
			renew := &RenewResponse{ProtocolVersion: 1, RequestID: "r", AgentID: "a", Lease: lease, NATSUserJWT: tc.token}
			if got := register.Validate() == nil; got != tc.ok {
				t.Errorf("register valid = %v, want %v", got, tc.ok)
			}
			if got := renew.Validate() == nil; got != tc.ok {
				t.Errorf("renew valid = %v, want %v", got, tc.ok)
			}
		})
	}
	rejected := &RegisterResponse{ProtocolVersion: 1, RequestID: "r", NATSUserJWT: sampleNATSUserJWT,
		Error: &Error{Code: ErrorEnrollmentRejected}}
	if rejected.Validate() == nil {
		t.Fatal("an error response carrying a credential was accepted")
	}
}

func TestAgentPermissionsAreScopedToOneAgent(t *testing.T) {
	permissions, err := AgentPermissions("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range append(append([]string{}, permissions.Publish...), permissions.Subscribe...) {
		if strings.Contains(subject, "*") {
			t.Errorf("permission %q uses a single-token wildcard", subject)
		}
		if strings.HasSuffix(subject, ".>") && !strings.Contains(subject, "agent-1") {
			t.Errorf("permission %q is not scoped to agent-1", subject)
		}
	}
	if len(permissions.Subscribe) != 1 || permissions.Subscribe[0] != AgentInboxRoot+".agent-1.>" {
		t.Fatalf("subscribe = %v", permissions.Subscribe)
	}
	if _, err := AgentPermissions("agent.1"); err == nil {
		t.Fatal("an agent ID with a dot was accepted")
	}
	bootstrap := BootstrapPermissions()
	for _, subject := range bootstrap.Subscribe {
		if strings.HasPrefix(AgentInboxRoot+".x", strings.TrimSuffix(subject, ">")) ||
			strings.HasPrefix(ControllerInboxPrefix+".x", strings.TrimSuffix(subject, ">")) {
			t.Fatalf("bootstrap subscription %q covers agent or controller inboxes", subject)
		}
	}
}

// badSigner returns a signature from a different key.
type badSigner struct{ public, other ed25519.PrivateKey }

func (s badSigner) Public() crypto.PublicKey { return s.public.Public() }
func (s badSigner) Sign(_ io.Reader, message []byte, _ crypto.SignerOpts) ([]byte, error) {
	return ed25519.Sign(s.other, message), nil
}

// Key rotation (Phase 16): SealWith accepts any Ed25519 crypto.Signer and
// produces the same bytes as Seal; OpenAny accepts a message signed by any
// pinned key and nothing else.
func TestSealWithSignerAndOpenAny(t *testing.T) {
	current := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	next := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	stranger := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	body := &RenewResponse{ProtocolVersion: ProtocolVersion, RequestID: "req-1", AgentID: "agent-1",
		Lease: &Lease{LeaseID: "lease-1", ExpiresAt: time.Unix(1700000000, 0).UTC(), HeartbeatIntervalMillis: 5000}}

	viaKey, err := Seal(current, KindRenewResponse, body)
	if err != nil {
		t.Fatal(err)
	}
	viaSigner, err := SealWith(crypto.Signer(current), KindRenewResponse, body)
	if err != nil || !bytes.Equal(viaKey, viaSigner) {
		t.Fatalf("SealWith differs from Seal (Ed25519 is deterministic): %v", err)
	}
	if _, err := SealWith(badSigner{public: current, other: stranger}, KindRenewResponse, body); err == nil {
		t.Fatal("a signer returning a signature from another key was accepted")
	}

	pins := []ed25519.PublicKey{current.Public().(ed25519.PublicKey), next.Public().(ed25519.PublicKey)}
	fromNext, _ := Seal(next, KindRenewResponse, body)
	fromStranger, _ := Seal(stranger, KindRenewResponse, body)
	var out RenewResponse
	for name, raw := range map[string][]byte{"current": viaKey, "next": fromNext} {
		if err := OpenAny(pins, KindRenewResponse, raw, MaxControlMessageBytes, &out); err != nil {
			t.Errorf("%s key: %v", name, err)
		}
	}
	if err := OpenAny(pins, KindRenewResponse, fromStranger, MaxControlMessageBytes, &out); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("unpinned key = %v", err)
	}
	if err := Open(pins[0], KindRenewResponse, fromNext, MaxControlMessageBytes, &out); !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("Open with only the current key accepted the next key: %v", err)
	}
	if err := OpenAny(nil, KindRenewResponse, viaKey, MaxControlMessageBytes, &out); err == nil {
		t.Error("no keys accepted")
	}
}
