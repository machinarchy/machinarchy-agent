package agentv1

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type EventType string

const (
	EventOperationRequested          EventType = "operation.requested"
	EventOperationCancelled          EventType = "operation.cancelled"
	EventOperationResultAcknowledged EventType = "operation.result.acknowledged"
)

type EventEnvelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	EventID         string          `json:"event_id"`
	EventType       EventType       `json:"event_type"`
	TargetAgentID   string          `json:"target_agent_id"`
	TargetHostID    string          `json:"target_host_id"`
	CorrelationID   string          `json:"correlation_id"`
	IssuedAt        time.Time       `json:"issued_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Payload         json.RawMessage `json:"payload"`
}

type OperationRequest struct {
	OperationID      string          `json:"operation_id"`
	Capability       string          `json:"capability"`
	Input            json.RawMessage `json:"input"`
	TimeoutMilli     int64           `json:"timeout_ms,omitempty"`
	OutputLimitBytes int64           `json:"output_limit_bytes,omitempty"`
}

type OperationCancellation struct {
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason,omitempty"`
}

// OperationResultAcknowledgement confirms that the controller has durably
// persisted a result. The Agent may then remove the result payload from its
// local outbox while retaining a compact operation deduplication record.
type OperationResultAcknowledgement struct {
	OperationID string `json:"operation_id"`
}

type OperationStatus string

const (
	OperationStatusAccepted      OperationStatus = "accepted"
	OperationStatusRunning       OperationStatus = "running"
	OperationStatusCancelRequest OperationStatus = "cancel_requested"
	OperationStatusSucceeded     OperationStatus = "succeeded"
	OperationStatusFailed        OperationStatus = "failed"
	OperationStatusCancelled     OperationStatus = "cancelled"
	OperationStatusTimedOut      OperationStatus = "timed_out"
	OperationStatusRejected      OperationStatus = "rejected"
	OperationStatusUnknown       OperationStatus = "unknown"
)

type OperationResult struct {
	ProtocolVersion int             `json:"protocol_version"`
	AgentID         string          `json:"agent_id"`
	HostID          string          `json:"host_id"`
	OperationID     string          `json:"operation_id"`
	CorrelationID   string          `json:"correlation_id"`
	Capability      string          `json:"capability,omitempty"`
	Status          OperationStatus `json:"status"`
	ErrorCode       string          `json:"error_code,omitempty"`
	Output          json.RawMessage `json:"output,omitempty"`
	AcceptedAt      time.Time       `json:"accepted_at,omitempty"`
	StartedAt       time.Time       `json:"started_at,omitempty"`
	CompletedAt     time.Time       `json:"completed_at,omitempty"`
	DurationMillis  int64           `json:"duration_ms,omitempty"`
}

// AgentEventsConsumerName returns the stable JetStream durable consumer name
// for one Agent. The controller provisions this consumer during enrollment;
// the Agent only binds to it and never creates or mutates broker resources.
func AgentEventsConsumerName(agentID string) (string, error) {
	if !ValidSubjectToken(agentID) {
		return "", errors.New("agent ID is not a valid durable consumer token")
	}
	name := "agent_" + agentID
	if len(name) > 128 {
		return "", errors.New("agent ID is too long for a durable consumer name")
	}
	return name, nil
}

func AgentEventsSubject(agentID string) (string, error) {
	if !ValidSubjectToken(agentID) {
		return "", errors.New("agent ID is not a valid NATS subject token")
	}
	return "machinarchy.v1.agent." + agentID + ".events", nil
}

func AgentOperationResultsSubject(agentID string) (string, error) {
	if !ValidSubjectToken(agentID) {
		return "", errors.New("agent ID is not a valid NATS subject token")
	}
	return "machinarchy.v1.agent." + agentID + ".operations.results", nil
}

func ValidSubjectToken(value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}
