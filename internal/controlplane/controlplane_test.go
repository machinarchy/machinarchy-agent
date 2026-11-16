package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"
)

type fakeRequester struct {
	subject  string
	request  []byte
	response []byte
	err      error
}

func (f *fakeRequester) Request(_ context.Context, subject string, request []byte) ([]byte, error) {
	f.subject = subject
	f.request = append([]byte(nil), request...)
	return f.response, f.err
}

func testControlPlaneConfig() config.Config {
	return config.Config{
		RegistrationSubject: "machinarchy.v1.agent.register",
		LeaseRenewSubject:   "machinarchy.v1.agent.lease.renew",
		RequestTimeout:      2 * time.Second,
	}
}

func TestNATSControlPlaneClientRegister(t *testing.T) {
	requester := &fakeRequester{}
	client, err := NewNATSControlPlaneClient(requester, testControlPlaneConfig())
	if err != nil {
		t.Fatal(err)
	}
	requestID := "e1a2b3c4-0000-4000-8000-000000000001"
	response := registerWireResponse{
		ResponseMetadata: responseMetadata{ProtocolVersion: agentProtocolVersion, RequestID: requestID},
		AgentID:          "agent-1", HostID: "host-1", LeaseID: "lease-1",
		LeaseToken:     "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: time.Now().Add(10 * time.Minute), HeartbeatIntervalMillis: 15000,
	}
	requester.response, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.Register(context.Background(), RegistrationRequest{
		RequestID: requestID, EnrollmentToken: "enrollment-token-0123456789abcdef", Hostname: "worker-01", AgentVersion: "0.2.0", OperatingSystem: "linux", Architecture: "amd64",
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if requester.subject != testControlPlaneConfig().RegistrationSubject {
		t.Fatalf("subject = %q", requester.subject)
	}
	if got.AgentID != "agent-1" || got.HostID != "host-1" || got.HeartbeatInterval != 15*time.Second {
		t.Fatalf("response = %#v", got)
	}
	var wire registerWireRequest
	if err := json.Unmarshal(requester.request, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.EnrollmentToken != "enrollment-token-0123456789abcdef" || wire.RequestID != requestID {
		t.Fatalf("wire request = %#v", wire)
	}
}

func TestNATSControlPlaneClientRejectsMismatchedRequestID(t *testing.T) {
	requester := &fakeRequester{}
	client, err := NewNATSControlPlaneClient(requester, testControlPlaneConfig())
	if err != nil {
		t.Fatal(err)
	}
	requester.response, _ = json.Marshal(registerWireResponse{ResponseMetadata: responseMetadata{ProtocolVersion: agentProtocolVersion, RequestID: "different"}})
	_, err = client.Register(context.Background(), RegistrationRequest{RequestID: "expected", EnrollmentToken: "enrollment-token-0123456789abcdef", Hostname: "worker", AgentVersion: "dev"})
	if err == nil {
		t.Fatal("Register() succeeded with a mismatched request ID")
	}
}

func TestNATSControlPlaneClientSurfacesRemoteErrorWithoutMessage(t *testing.T) {
	requester := &fakeRequester{}
	client, err := NewNATSControlPlaneClient(requester, testControlPlaneConfig())
	if err != nil {
		t.Fatal(err)
	}
	requester.response, _ = json.Marshal(registerWireResponse{ResponseMetadata: responseMetadata{
		ProtocolVersion: agentProtocolVersion,
		RequestID:       "request-1",
		Error:           &controlPlaneError{Code: "enrollment_expired", Message: "sensitive internal detail", Retryable: false},
	}})
	_, err = client.Register(context.Background(), RegistrationRequest{RequestID: "request-1", EnrollmentToken: "enrollment-token-0123456789abcdef", Hostname: "worker", AgentVersion: "dev"})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if remote.Retryable {
		t.Fatal("RemoteError.Retryable = true, want false")
	}
	if got := remote.Error(); got == "" || got == "enrollment_expired" {
		t.Fatalf("unexpected error string: %q", got)
	}
}

func TestValidateLeaseRejectsHeartbeatTooCloseToExpiry(t *testing.T) {
	err := validateLease(time.Now().Add(20*time.Second), 15*time.Second)
	if err == nil {
		t.Fatal("validateLease() accepted heartbeat interval too close to expiry")
	}
}
