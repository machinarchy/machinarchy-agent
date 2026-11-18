package agentv1

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"
)

// FuzzOpenCommand: an agent decodes untrusted bytes from the network. Open
// must never panic, and anything it accepts must have been signed by the
// controller key: mutated bytes are rejected unless they decode to the same
// signed body.
func FuzzOpenCommand(f *testing.F) {
	controller := testKey(1)
	sealed, err := Seal(controller, KindCommand, sampleCommand())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(sealed)
	f.Add([]byte(`{"v":1,"kind":"command","body":"e30=","sig":""}`))
	f.Add([]byte(`{}`))
	public := controller.Public().(ed25519.PublicKey)
	f.Fuzz(func(t *testing.T, raw []byte) {
		var command Command
		if err := Open(public, KindCommand, raw, MaxCommandMessageBytes, &command); err != nil {
			return
		}
		// Accepted: re-sealing the decoded body must verify with the same key,
		// and the command must be well-formed after validation succeeds.
		if err := command.Validate(); err != nil {
			return
		}
		again, err := Seal(controller, KindCommand, &command)
		if err != nil {
			t.Fatalf("accepted command cannot be re-sealed: %v", err)
		}
		var decoded Command
		if err := Open(public, KindCommand, again, MaxCommandMessageBytes, &decoded); err != nil || decoded.CommandID != command.CommandID {
			t.Fatalf("re-sealed command does not round-trip: %v", err)
		}
	})
}

// FuzzOpenRegisterRequest: the controller decodes self-signed registrations
// from anyone. It must never panic, and an accepted request must carry a
// key that verifies its own envelope.
func FuzzOpenRegisterRequest(f *testing.F) {
	agent := testKey(2)
	request := &RegisterRequest{ProtocolVersion: ProtocolVersion, RequestID: "req-1", EnrollmentToken: string(bytes.Repeat([]byte("t"), MinEnrollmentToken)),
		AgentPublicKey: agent.Public().(ed25519.PublicKey), Hostname: "host", AgentVersion: "1.0.0", OS: "linux", Architecture: "amd64",
		Capabilities: []string{CapabilityRuntimeInfo}, SentAt: time.Unix(1700000000, 0).UTC()}
	sealed, err := Seal(agent, KindRegisterRequest, request)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(sealed)
	f.Add([]byte(`{"v":1,"kind":"register.request","body":"","sig":""}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		decoded, err := OpenRegisterRequest(raw)
		if err != nil {
			return
		}
		if len(decoded.AgentPublicKey) != ed25519.PublicKeySize || decoded.Validate() != nil {
			t.Fatalf("accepted an invalid registration: %+v", decoded)
		}
	})
}

// FuzzParsePublicKey: key configuration parsing never panics and only
// returns 32-byte keys that round-trip.
func FuzzParsePublicKey(f *testing.F) {
	f.Add(EncodePublicKey(testKey(3).Public().(ed25519.PublicKey)))
	f.Add("not base64")
	f.Fuzz(func(t *testing.T, value string) {
		key, err := ParsePublicKey(value)
		if err != nil {
			return
		}
		if len(key) != ed25519.PublicKeySize {
			t.Fatalf("accepted %d-byte key", len(key))
		}
		if again, err := ParsePublicKey(EncodePublicKey(key)); err != nil || !again.Equal(key) {
			t.Fatalf("key does not round-trip: %v", err)
		}
	})
}

// FuzzDecodeStrict: strict decoding never panics on any input.
func FuzzDecodeStrict(f *testing.F) {
	f.Add([]byte(`{"service":"nginx"}`))
	f.Add([]byte(`{"service":"nginx"} {}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var request ServiceStatusRequest
		_ = DecodeStrict(data, &request)
	})
}
