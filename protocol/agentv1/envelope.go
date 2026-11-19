package agentv1

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ProtocolVersion is the wire version carried by every envelope and body.
const ProtocolVersion = 1

// Message size limits for complete envelopes on the wire.
const (
	MaxControlMessageBytes = 64 << 10
	MaxCommandMessageBytes = 192 << 10
	MaxResultMessageBytes  = 512 << 10
)

// Kind identifies the body type and is part of the signed input, so a
// signature for one kind can never be replayed as another.
type Kind string

const (
	KindRegisterRequest  Kind = "register.request"
	KindRegisterResponse Kind = "register.response"
	KindRenewRequest     Kind = "lease.renew.request"
	KindRenewResponse    Kind = "lease.renew.response"
	KindCommand          Kind = "command"
	KindResult           Kind = "result"
)

const signingDomain = "machinarchy-agent/v1"

// Envelope is the only top-level message format. Body holds the exact JSON
// bytes that were signed; it is base64-encoded on the wire so re-encoding by
// intermediaries cannot change it.
type Envelope struct {
	Version   int    `json:"v"`
	Kind      Kind   `json:"kind"`
	Body      []byte `json:"body"`
	Signature []byte `json:"sig"`
}

// Body is implemented by every message body.
type Body interface {
	Validate() error
}

var (
	ErrInvalidMessage   = errors.New("invalid agent protocol message")
	ErrInvalidSignature = errors.New("agent protocol message signature is invalid")
)

func signingInput(kind Kind, body []byte) []byte {
	input := make([]byte, 0, len(signingDomain)+len(kind)+len(body)+2)
	input = append(input, signingDomain...)
	input = append(input, 0)
	input = append(input, kind...)
	input = append(input, 0)
	return append(input, body...)
}

// Seal validates body, encodes it and returns a signed envelope.
func Seal(key ed25519.PrivateKey, kind Kind, body Body) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("signing key must be an Ed25519 private key")
	}
	return SealWith(key, kind, body)
}

// SealWith is Seal with any Ed25519 crypto.Signer, such as a key held in a
// KMS. The signature is checked against the signer's public key before the
// envelope is returned, so a misbehaving signer cannot emit messages that
// agents would reject. The wire format is the same as Seal's.
func SealWith(signer crypto.Signer, kind Kind, body Body) ([]byte, error) {
	if signer == nil {
		return nil, errors.New("a signer is required")
	}
	public, ok := signer.Public().(ed25519.PublicKey)
	if !ok || len(public) != ed25519.PublicKeySize {
		return nil, errors.New("signing key must be an Ed25519 key")
	}
	if err := body.Validate(); err != nil {
		return nil, fmt.Errorf("seal %s: %w", kind, err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode %s body: %w", kind, err)
	}
	input := signingInput(kind, encoded)
	signature, err := signer.Sign(nil, input, crypto.Hash(0)) // pure Ed25519 over the message
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", kind, err)
	}
	if !ed25519.Verify(public, input, signature) {
		return nil, fmt.Errorf("sign %s: the signer returned an invalid signature", kind)
	}
	return json.Marshal(Envelope{
		Version:   ProtocolVersion,
		Kind:      kind,
		Body:      encoded,
		Signature: signature,
	})
}

// Open verifies raw against key, decodes the body strictly into out and
// validates it. It never returns body content when verification fails.
func Open(key ed25519.PublicKey, kind Kind, raw []byte, limit int, out Body) error {
	return OpenAny([]ed25519.PublicKey{key}, kind, raw, limit, out)
}

// OpenAny is Open for a receiver that trusts several keys, such as an agent
// pinning the controller's current and next key during a rotation. The
// message is accepted if any key verifies it. The wire format is unchanged.
func OpenAny(keys []ed25519.PublicKey, kind Kind, raw []byte, limit int, out Body) error {
	envelope, err := decodeEnvelope(kind, raw, limit)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("at least one verification key is required")
	}
	input := signingInput(kind, envelope.Body)
	verified := false
	for _, key := range keys {
		if len(key) != ed25519.PublicKeySize {
			return errors.New("verification key must be an Ed25519 public key")
		}
		if ed25519.Verify(key, input, envelope.Signature) {
			verified = true
			break
		}
	}
	if !verified {
		return ErrInvalidSignature
	}
	return decodeBody(envelope.Body, out)
}

// OpenRegisterRequest verifies a self-signed registration request with the
// public key it carries (proof of possession). The controller must still
// authorize the request through its enrollment token.
func OpenRegisterRequest(raw []byte) (RegisterRequest, error) {
	var request RegisterRequest
	envelope, err := decodeEnvelope(KindRegisterRequest, raw, MaxControlMessageBytes)
	if err != nil {
		return request, err
	}
	if err := decodeBody(envelope.Body, &request); err != nil {
		return RegisterRequest{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(request.AgentPublicKey), signingInput(KindRegisterRequest, envelope.Body), envelope.Signature) {
		return RegisterRequest{}, ErrInvalidSignature
	}
	return request, nil
}

// AgentSigned is implemented by bodies signed with a registered agent key.
type AgentSigned interface {
	Body
	SignerAgentID() string
}

// OpenFromAgent verifies a message signed by a registered agent. keyFor
// returns the public key recorded at registration for the agent ID claimed in
// the body; unknown agents are rejected with ErrInvalidSignature. Used by the
// controller for lease renewals and results.
func OpenFromAgent(kind Kind, raw []byte, limit int, keyFor func(agentID string) (ed25519.PublicKey, bool), out AgentSigned) error {
	envelope, err := decodeEnvelope(kind, raw, limit)
	if err != nil {
		return err
	}
	if err := decodeBody(envelope.Body, out); err != nil {
		return err
	}
	key, ok := keyFor(out.SignerAgentID())
	if !ok || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, signingInput(kind, envelope.Body), envelope.Signature) {
		return ErrInvalidSignature
	}
	return nil
}

func decodeEnvelope(kind Kind, raw []byte, limit int) (Envelope, error) {
	var envelope Envelope
	if len(raw) == 0 || len(raw) > limit {
		return envelope, fmt.Errorf("%w: message is empty or exceeds %d bytes", ErrInvalidMessage, limit)
	}
	if err := DecodeStrict(raw, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	switch {
	case envelope.Version != ProtocolVersion:
		return Envelope{}, fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidMessage, envelope.Version)
	case envelope.Kind != kind:
		return Envelope{}, fmt.Errorf("%w: expected kind %q", ErrInvalidMessage, kind)
	case len(envelope.Signature) != ed25519.SignatureSize:
		return Envelope{}, fmt.Errorf("%w: malformed signature", ErrInvalidMessage)
	case len(envelope.Body) == 0:
		return Envelope{}, fmt.Errorf("%w: empty body", ErrInvalidMessage)
	}
	return envelope, nil
}

func decodeBody(body []byte, out Body) error {
	if err := DecodeStrict(body, out); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	if err := out.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	return nil
}

// DecodeStrict decodes exactly one JSON value and rejects unknown fields.
func DecodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON value")
	}
	return nil
}

// ParsePublicKey decodes a standard base64 Ed25519 public key.
func ParsePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, errors.New("public key must be a base64-encoded 32-byte Ed25519 key")
	}
	return ed25519.PublicKey(decoded), nil
}

// EncodePublicKey is the inverse of ParsePublicKey.
func EncodePublicKey(key ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(key)
}
