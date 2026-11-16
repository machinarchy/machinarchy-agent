// Package agentv1 contains the versioned wire contracts shared by Machinarchy
// agents and the control plane. Keep transport and persistence types out of
// this package so both sides can depend on the same protocol definitions.
package agentv1

import "time"

const (
	ProtocolVersion          = 2
	MaxControlPlaneMessage   = 64 * 1024
	MaxAgentEventMessage     = 256 * 1024
	MaxOperationOutput       = 256 * 1024
	DefaultOperationLifetime = 5 * time.Minute
)

type ControlPlaneError struct {
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable"`
}

type ResponseMetadata struct {
	ProtocolVersion int                `json:"protocol_version"`
	RequestID       string             `json:"request_id"`
	Error           *ControlPlaneError `json:"error,omitempty"`
}

type RegistrationRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	RequestID       string `json:"request_id"`
	EnrollmentToken string `json:"enrollment_token"`
	Hostname        string `json:"hostname"`
	AgentVersion    string `json:"agent_version"`
	OperatingSystem string `json:"operating_system,omitempty"`
	Architecture    string `json:"architecture,omitempty"`
}

type RegistrationResponse struct {
	ResponseMetadata
	AgentID                 string    `json:"agent_id,omitempty"`
	HostID                  string    `json:"host_id,omitempty"`
	LeaseID                 string    `json:"lease_id,omitempty"`
	LeaseToken              string    `json:"lease_token,omitempty"`
	LeaseExpiresAt          time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatIntervalMillis int64     `json:"heartbeat_interval_ms,omitempty"`
}

type LeaseRenewalRequest struct {
	ProtocolVersion int       `json:"protocol_version"`
	RequestID       string    `json:"request_id"`
	AgentID         string    `json:"agent_id"`
	HostID          string    `json:"host_id"`
	LeaseID         string    `json:"lease_id"`
	LeaseToken      string    `json:"lease_token"`
	AgentVersion    string    `json:"agent_version"`
	Hostname        string    `json:"hostname"`
	SentAt          time.Time `json:"sent_at"`
}

type LeaseRenewalResponse struct {
	ResponseMetadata
	LeaseExpiresAt          time.Time `json:"lease_expires_at,omitempty"`
	HeartbeatIntervalMillis int64     `json:"heartbeat_interval_ms,omitempty"`
	LeaseToken              string    `json:"lease_token,omitempty"`
}
