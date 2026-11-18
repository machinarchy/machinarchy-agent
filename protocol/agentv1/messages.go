package agentv1

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Bounds shared by both sides of the contract.
const (
	MaxIdentifierLength   = 128
	MinEnrollmentToken    = 32
	MaxEnrollmentToken    = 256
	MaxCommandLifetime    = 15 * time.Minute
	MaxClockSkew          = 2 * time.Minute
	MinHeartbeatInterval  = 5 * time.Second
	MaxHeartbeatInterval  = 5 * time.Minute
	MaxOperationInput     = 64 << 10
	MaxOperationOutput    = 256 << 10
	MaxOperationTimeout   = time.Hour
	MaxCapabilitiesListed = 32
)

// ValidIdentifier reports whether value is a safe identifier. Identifiers are
// also used as NATS subject tokens and JetStream consumer names.
func ValidIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentifierLength {
		return false
	}
	for _, r := range value {
		if !isIdentifierRune(r) {
			return false
		}
	}
	return true
}

func isIdentifierRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		return true
	}
	return false
}

// validText reports whether value is non-empty, at most maxLength bytes of
// UTF-8, and free of line breaks and NUL.
func validText(value string, maxLength int) bool {
	return value != "" && len(value) <= maxLength && utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n\x00")
}

func validVersion(version int) error {
	if version != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", version)
	}
	return nil
}

// ErrorCode values returned by the controller.
const (
	ErrorInvalidRequest     = "invalid_request"
	ErrorEnrollmentRejected = "enrollment_rejected"
	ErrorUnknownAgent       = "unknown_agent"
	ErrorLeaseRevoked       = "lease_revoked"
	ErrorUnavailable        = "unavailable"
)

// Error is a controller-side failure. Retryable is advisory.
type Error struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return "controller rejected request: " + e.Code }

func (e *Error) validate() error {
	if !ValidIdentifier(strings.ReplaceAll(e.Code, ".", "_")) {
		return errors.New("error code is invalid")
	}
	return nil
}

// Lease is the controller-granted right to receive commands.
type Lease struct {
	LeaseID                 string    `json:"lease_id"`
	ExpiresAt               time.Time `json:"expires_at"`
	HeartbeatIntervalMillis int64     `json:"heartbeat_interval_ms"`
}

// HeartbeatInterval returns the renewal cadence requested by the controller.
func (l Lease) HeartbeatInterval() time.Duration {
	return time.Duration(l.HeartbeatIntervalMillis) * time.Millisecond
}

func (l Lease) validate() error {
	interval := l.HeartbeatInterval()
	switch {
	case !ValidIdentifier(l.LeaseID):
		return errors.New("lease ID is invalid")
	case l.ExpiresAt.IsZero():
		return errors.New("lease expiry is required")
	case interval < MinHeartbeatInterval || interval > MaxHeartbeatInterval:
		return fmt.Errorf("heartbeat interval must be between %s and %s", MinHeartbeatInterval, MaxHeartbeatInterval)
	}
	return nil
}

// RegisterRequest enrolls a new agent. It is signed by AgentPublicKey.
type RegisterRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	RequestID       string    `json:"request_id"`
	EnrollmentToken string    `json:"enrollment_token"`
	AgentPublicKey  []byte    `json:"agent_public_key"`
	Hostname        string    `json:"hostname"`
	AgentVersion    string    `json:"agent_version"`
	OS              string    `json:"os"`
	Architecture    string    `json:"architecture"`
	Capabilities    []string  `json:"capabilities"`
	SentAt          time.Time `json:"sent_at"`
}

func (r *RegisterRequest) Validate() error {
	if err := validVersion(r.ProtocolVersion); err != nil {
		return err
	}
	switch {
	case !ValidIdentifier(r.RequestID):
		return errors.New("request ID is invalid")
	case len(r.EnrollmentToken) < MinEnrollmentToken || len(r.EnrollmentToken) > MaxEnrollmentToken || !validText(r.EnrollmentToken, MaxEnrollmentToken):
		return errors.New("enrollment token is invalid")
	case len(r.AgentPublicKey) != ed25519.PublicKeySize:
		return errors.New("agent public key is invalid")
	case !validText(r.Hostname, 255), !validText(r.AgentVersion, 64), !validText(r.OS, 32), !validText(r.Architecture, 32):
		return errors.New("agent metadata is invalid")
	case len(r.Capabilities) > MaxCapabilitiesListed:
		return errors.New("too many capabilities")
	case r.SentAt.IsZero():
		return errors.New("sent_at is required")
	}
	for _, name := range r.Capabilities {
		if !ValidCapabilityName(name) {
			return errors.New("capability name is invalid")
		}
	}
	return nil
}

// RegisterResponse is signed by the controller. Exactly one of Error or the
// identity fields is set.
type RegisterResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	RequestID       string `json:"request_id"`
	AgentID         string `json:"agent_id,omitempty"`
	HostID          string `json:"host_id,omitempty"`
	Lease           *Lease `json:"lease,omitempty"`
	Error           *Error `json:"error,omitempty"`
}

func (r *RegisterResponse) Validate() error {
	if err := validVersion(r.ProtocolVersion); err != nil {
		return err
	}
	if !ValidIdentifier(r.RequestID) {
		return errors.New("request ID is invalid")
	}
	if r.Error != nil {
		if r.AgentID != "" || r.HostID != "" || r.Lease != nil {
			return errors.New("error response must not carry an identity")
		}
		return r.Error.validate()
	}
	if !ValidIdentifier(r.AgentID) || !ValidIdentifier(r.HostID) || r.Lease == nil {
		return errors.New("registration identity is incomplete")
	}
	return r.Lease.validate()
}

// RenewRequest extends the current lease. It is signed by the agent key.
type RenewRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	RequestID       string    `json:"request_id"`
	AgentID         string    `json:"agent_id"`
	LeaseID         string    `json:"lease_id"`
	AgentVersion    string    `json:"agent_version"`
	SentAt          time.Time `json:"sent_at"`
}

func (r *RenewRequest) Validate() error {
	if err := validVersion(r.ProtocolVersion); err != nil {
		return err
	}
	switch {
	case !ValidIdentifier(r.RequestID), !ValidIdentifier(r.AgentID), !ValidIdentifier(r.LeaseID):
		return errors.New("renew identifiers are invalid")
	case !validText(r.AgentVersion, 64):
		return errors.New("agent version is invalid")
	case r.SentAt.IsZero():
		return errors.New("sent_at is required")
	}
	return nil
}

// RenewResponse is signed by the controller.
type RenewResponse struct {
	ProtocolVersion int    `json:"protocol_version"`
	RequestID       string `json:"request_id"`
	AgentID         string `json:"agent_id"`
	Lease           *Lease `json:"lease,omitempty"`
	Error           *Error `json:"error,omitempty"`
}

func (r *RenewResponse) Validate() error {
	if err := validVersion(r.ProtocolVersion); err != nil {
		return err
	}
	if !ValidIdentifier(r.RequestID) || !ValidIdentifier(r.AgentID) {
		return errors.New("renew response identifiers are invalid")
	}
	if r.Error != nil {
		if r.Lease != nil {
			return errors.New("error response must not carry a lease")
		}
		return r.Error.validate()
	}
	if r.Lease == nil {
		return errors.New("lease is required")
	}
	return r.Lease.validate()
}

// CommandType selects the command semantics.
type CommandType string

const (
	CommandOperationRequest CommandType = "operation.request"
	CommandOperationCancel  CommandType = "operation.cancel"
	CommandResultAck        CommandType = "result.ack"
)

// Command is published by the controller to the agent's command subject and
// signed with the controller key. CommandID is unique per command and is used
// for replay protection; OperationID is the idempotency key for execution.
type Command struct {
	ProtocolVersion int             `json:"protocol_version"`
	CommandID       string          `json:"command_id"`
	AgentID         string          `json:"agent_id"`
	Type            CommandType     `json:"type"`
	IssuedAt        time.Time       `json:"issued_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	OperationID     string          `json:"operation_id"`
	Capability      string          `json:"capability,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	TimeoutMillis   int64           `json:"timeout_ms,omitempty"`
}

// Validate checks the static shape. Time-dependent checks are in ValidateAt.
func (c *Command) Validate() error {
	if err := validVersion(c.ProtocolVersion); err != nil {
		return err
	}
	switch {
	case !ValidIdentifier(c.CommandID), !ValidIdentifier(c.AgentID), !ValidIdentifier(c.OperationID):
		return errors.New("command identifiers are invalid")
	case c.IssuedAt.IsZero() || !c.ExpiresAt.After(c.IssuedAt):
		return errors.New("command validity window is invalid")
	case c.ExpiresAt.Sub(c.IssuedAt) > MaxCommandLifetime:
		return fmt.Errorf("command lifetime exceeds %s", MaxCommandLifetime)
	}
	switch c.Type {
	case CommandOperationRequest:
		timeout := time.Duration(c.TimeoutMillis) * time.Millisecond
		switch {
		case !ValidCapabilityName(c.Capability):
			return errors.New("capability is invalid")
		case len(c.Input) == 0 || len(c.Input) > MaxOperationInput || !json.Valid(c.Input):
			return errors.New("operation input must be a JSON value within the size limit")
		case timeout <= 0 || timeout > MaxOperationTimeout:
			return errors.New("operation timeout is out of range")
		}
	case CommandOperationCancel, CommandResultAck:
		if c.Capability != "" || len(c.Input) != 0 || c.TimeoutMillis != 0 {
			return errors.New("command carries fields not allowed for its type")
		}
	default:
		return errors.New("command type is unsupported")
	}
	return nil
}

// ValidateAt checks the command is addressed to agentID and valid at now.
func (c *Command) ValidateAt(agentID string, now time.Time) error {
	switch {
	case c.AgentID != agentID:
		return errors.New("command is addressed to another agent")
	case c.IssuedAt.After(now.Add(MaxClockSkew)):
		return errors.New("command is issued in the future")
	case !c.ExpiresAt.After(now):
		return errors.New("command has expired")
	}
	return nil
}

// OperationStatus is the lifecycle state reported in a Result.
type OperationStatus string

const (
	StatusAccepted  OperationStatus = "accepted"
	StatusSucceeded OperationStatus = "succeeded"
	StatusFailed    OperationStatus = "failed"
	StatusRejected  OperationStatus = "rejected"
	StatusTimedOut  OperationStatus = "timed_out"
	StatusCancelled OperationStatus = "cancelled"
	// StatusUnknown means the agent restarted while executing; the side effect
	// may or may not have happened. Controllers must not retry automatically.
	StatusUnknown OperationStatus = "unknown"
)

// Terminal reports whether no further result follows for the operation.
func (s OperationStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusRejected, StatusTimedOut, StatusCancelled, StatusUnknown:
		return true
	}
	return false
}

func (s OperationStatus) valid() bool { return s == StatusAccepted || s.Terminal() }

// Result codes for non-success outcomes.
const (
	CodeCapabilityUnavailable = "capability_unavailable"
	CodeInvalidInput          = "invalid_input"
	CodeQueueFull             = "queue_full"
	CodeExpired               = "expired_before_execution"
	CodeExecutionFailed       = "execution_failed"
	CodeOutputTooLarge        = "output_too_large"
	CodeTimedOut              = "timed_out"
	CodeCancelled             = "cancelled"
	CodeAgentRestarted        = "agent_restarted_during_execution"
	CodeAgentShutdown         = "agent_shutdown"
)

// Result reports operation progress. It is signed by the agent key and
// republished until the controller sends a result.ack command for a terminal
// result. Controllers must upsert by OperationID.
type Result struct {
	ProtocolVersion int             `json:"protocol_version"`
	AgentID         string          `json:"agent_id"`
	OperationID     string          `json:"operation_id"`
	Capability      string          `json:"capability"`
	Status          OperationStatus `json:"status"`
	Code            string          `json:"code,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	AcceptedAt      time.Time       `json:"accepted_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
}

func (r *Result) Validate() error {
	if err := validVersion(r.ProtocolVersion); err != nil {
		return err
	}
	switch {
	case !ValidIdentifier(r.AgentID), !ValidIdentifier(r.OperationID):
		return errors.New("result identifiers are invalid")
	case r.Capability != "" && !ValidCapabilityName(r.Capability):
		return errors.New("capability is invalid")
	case !r.Status.valid():
		return errors.New("status is invalid")
	case r.Code != "" && !ValidIdentifier(r.Code):
		return errors.New("code is invalid")
	case len(r.Output) > MaxOperationOutput || (len(r.Output) > 0 && !json.Valid(r.Output)):
		return errors.New("output must be JSON within the size limit")
	case r.AcceptedAt.IsZero():
		return errors.New("accepted_at is required")
	case r.Status.Terminal() && r.CompletedAt == nil:
		return errors.New("terminal results require completed_at")
	}
	return nil
}

// SignerAgentID identifies the agent whose key signs this request.
func (r *RenewRequest) SignerAgentID() string { return r.AgentID }

// SignerAgentID identifies the agent whose key signs this result.
func (r *Result) SignerAgentID() string { return r.AgentID }
