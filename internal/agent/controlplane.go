package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/machinarchy/machinarchy-protocol/agent/v1"
)

const agentProtocolVersion = agentv1.ProtocolVersion
const maxControlPlaneMessageBytes = agentv1.MaxControlPlaneMessage

// Requester is the minimal request/reply transport capability required by the
// agent control-plane client. NATS implements this without exposing the NATS
// client type to the session manager.
type Requester interface {
	Request(context.Context, string, []byte) ([]byte, error)
}

type ControlPlane interface {
	Register(context.Context, RegistrationRequest) (RegistrationResponse, error)
	RenewLease(context.Context, LeaseRenewalRequest) (LeaseRenewalResponse, error)
}

// RemoteError is returned by the control plane. Retryable is advisory; the
// local lease expiration remains the hard upper bound for retries.
type RemoteError struct {
	Code      string
	Retryable bool
}

// TransportError identifies a failed request/reply exchange. The session may
// retry it while registration startup or the currently acknowledged lease is
// still valid. Protocol and local persistence errors are not TransportErrors.
type TransportError struct{ err error }

func (e *TransportError) Error() string {
	if e == nil || e.err == nil {
		return "control-plane transport request failed"
	}
	return "control-plane transport request failed: " + e.err.Error()
}
func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *RemoteError) Error() string {
	if e == nil {
		return "control plane rejected the request"
	}
	if e.Code == "" {
		return "control plane rejected the request"
	}
	return "control plane rejected the request: " + e.Code
}

type RegistrationRequest struct {
	RequestID       string
	EnrollmentToken string
	Hostname        string
	AgentVersion    string
	OperatingSystem string
	Architecture    string
}

type RegistrationResponse struct {
	AgentID           string
	HostID            string
	LeaseID           string
	LeaseToken        string
	LeaseExpiresAt    time.Time
	HeartbeatInterval time.Duration
}

type LeaseRenewalRequest struct {
	RequestID    string
	AgentID      string
	HostID       string
	LeaseID      string
	LeaseToken   string
	AgentVersion string
	Hostname     string
	SentAt       time.Time
}

type LeaseRenewalResponse struct {
	LeaseExpiresAt    time.Time
	HeartbeatInterval time.Duration
	LeaseToken        string
}

// NATSControlPlaneClient implements the initial versioned request/reply
// contract. The subject names and payloads are a proposed Phase 2 wire contract
// and must be implemented by the controller before this agent is deployed.
type NATSControlPlaneClient struct {
	requester         Requester
	registerSubject   string
	leaseRenewSubject string
	requestTimeout    time.Duration
}

func NewNATSControlPlaneClient(requester Requester, cfg Config) (*NATSControlPlaneClient, error) {
	if requester == nil {
		return nil, errors.New("control-plane requester is required")
	}
	if err := validateNATSSubject(envRegisterSubject, cfg.RegistrationSubject); err != nil {
		return nil, err
	}
	if err := validateNATSSubject(envLeaseRenewSubject, cfg.LeaseRenewSubject); err != nil {
		return nil, err
	}
	if cfg.RequestTimeout <= 0 {
		return nil, errors.New("control-plane request timeout must be positive")
	}
	return &NATSControlPlaneClient{
		requester:         requester,
		registerSubject:   cfg.RegistrationSubject,
		leaseRenewSubject: cfg.LeaseRenewSubject,
		requestTimeout:    cfg.RequestTimeout,
	}, nil
}

type controlPlaneError = agentv1.ControlPlaneError

type responseMetadata = agentv1.ResponseMetadata

type registerWireRequest = agentv1.RegistrationRequest

type registerWireResponse = agentv1.RegistrationResponse

type renewWireRequest = agentv1.LeaseRenewalRequest

type renewWireResponse = agentv1.LeaseRenewalResponse

func (c *NATSControlPlaneClient) Register(ctx context.Context, request RegistrationRequest) (RegistrationResponse, error) {
	if request.RequestID == "" || len(request.EnrollmentToken) < 32 || len(request.EnrollmentToken) > 256 || request.Hostname == "" || len(request.Hostname) > 255 || strings.ContainsAny(request.Hostname, "\r\n\x00") || request.AgentVersion == "" {
		return RegistrationResponse{}, errors.New("agent registration request is incomplete")
	}
	wire := registerWireRequest{
		ProtocolVersion: agentProtocolVersion,
		RequestID:       request.RequestID,
		EnrollmentToken: request.EnrollmentToken,
		Hostname:        request.Hostname,
		AgentVersion:    request.AgentVersion,
		OperatingSystem: request.OperatingSystem,
		Architecture:    request.Architecture,
	}
	var response registerWireResponse
	if err := c.roundTrip(ctx, c.registerSubject, request.RequestID, wire, &response); err != nil {
		return RegistrationResponse{}, fmt.Errorf("register agent: %w", err)
	}
	if response.AgentID == "" || response.HostID == "" || response.LeaseID == "" || len(response.LeaseToken) < 32 || len(response.LeaseToken) > 4096 {
		return RegistrationResponse{}, errors.New("control plane returned an incomplete agent registration")
	}
	interval := time.Duration(response.HeartbeatIntervalMillis) * time.Millisecond
	if err := validateLease(response.LeaseExpiresAt, interval); err != nil {
		return RegistrationResponse{}, fmt.Errorf("control plane returned an invalid agent lease: %w", err)
	}
	return RegistrationResponse{
		AgentID:           response.AgentID,
		HostID:            response.HostID,
		LeaseID:           response.LeaseID,
		LeaseToken:        response.LeaseToken,
		LeaseExpiresAt:    response.LeaseExpiresAt,
		HeartbeatInterval: interval,
	}, nil
}

func (c *NATSControlPlaneClient) RenewLease(ctx context.Context, request LeaseRenewalRequest) (LeaseRenewalResponse, error) {
	if request.RequestID == "" || request.AgentID == "" || request.HostID == "" || request.LeaseID == "" || len(request.LeaseToken) < 32 || request.Hostname == "" || len(request.Hostname) > 255 || strings.ContainsAny(request.Hostname, "\r\n\x00") {
		return LeaseRenewalResponse{}, errors.New("agent lease renewal request is incomplete")
	}
	wire := renewWireRequest{
		ProtocolVersion: agentProtocolVersion,
		RequestID:       request.RequestID,
		AgentID:         request.AgentID,
		HostID:          request.HostID,
		LeaseID:         request.LeaseID,
		LeaseToken:      request.LeaseToken,
		AgentVersion:    request.AgentVersion,
		Hostname:        request.Hostname,
		SentAt:          request.SentAt,
	}
	var response renewWireResponse
	if err := c.roundTrip(ctx, c.leaseRenewSubject, request.RequestID, wire, &response); err != nil {
		return LeaseRenewalResponse{}, fmt.Errorf("renew agent lease: %w", err)
	}
	interval := time.Duration(response.HeartbeatIntervalMillis) * time.Millisecond
	if err := validateLease(response.LeaseExpiresAt, interval); err != nil {
		return LeaseRenewalResponse{}, fmt.Errorf("control plane returned an invalid renewed lease: %w", err)
	}
	if response.LeaseToken != "" && (len(response.LeaseToken) < 32 || len(response.LeaseToken) > 4096) {
		return LeaseRenewalResponse{}, errors.New("control plane returned an invalid rotated lease credential")
	}
	return LeaseRenewalResponse{
		LeaseExpiresAt:    response.LeaseExpiresAt,
		HeartbeatInterval: interval,
		LeaseToken:        response.LeaseToken,
	}, nil
}

func (c *NATSControlPlaneClient) roundTrip(ctx context.Context, subject, requestID string, request any, response any) error {
	if ctx == nil {
		return errors.New("control-plane request context is required")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode control-plane request: %w", err)
	}
	if len(payload) > maxControlPlaneMessageBytes {
		return errors.New("control-plane request exceeds the maximum message size")
	}
	body, err := c.requester.Request(requestCtx, subject, payload)
	if err != nil {
		return &TransportError{err: fmt.Errorf("send request to subject %q: %w", subject, err)}
	}
	if len(body) == 0 || len(body) > maxControlPlaneMessageBytes {
		return errors.New("control plane returned an empty or oversized response")
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("decode control-plane response: %w", err)
	}
	metadata, ok := responseMetadataFrom(response)
	if !ok {
		return errors.New("control-plane response does not contain protocol metadata")
	}
	if metadata.ProtocolVersion != agentProtocolVersion {
		return fmt.Errorf("unsupported control-plane protocol version %d", metadata.ProtocolVersion)
	}
	if metadata.RequestID != requestID {
		return errors.New("control-plane response request identifier does not match")
	}
	if metadata.Error != nil {
		code := sanitizeRemoteCode(metadata.Error.Code)
		return &RemoteError{Code: code, Retryable: metadata.Error.Retryable}
	}
	return nil
}

func responseMetadataFrom(value any) (responseMetadata, bool) {
	switch v := value.(type) {
	case *registerWireResponse:
		return v.ResponseMetadata, true
	case *renewWireResponse:
		return v.ResponseMetadata, true
	default:
		return responseMetadata{}, false
	}
}

func sanitizeRemoteCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		value = value[:64]
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return "remote_error"
		}
	}
	if value == "" {
		return "remote_error"
	}
	return value
}

func validateLease(expiresAt time.Time, interval time.Duration) error {
	now := time.Now()
	remaining := expiresAt.Sub(now)
	if expiresAt.IsZero() || remaining <= 0 {
		return errors.New("lease expiration must be in the future")
	}
	if interval < 5*time.Second || interval > 5*time.Minute {
		return errors.New("heartbeat interval must be between 5 seconds and 5 minutes")
	}
	if interval*2 >= remaining {
		return errors.New("lease must remain valid for at least two heartbeat intervals")
	}
	return nil
}

func parseLeaseExpiry(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}
