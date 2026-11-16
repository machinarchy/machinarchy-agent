package agentv1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRegistrationWireContractJSONNames(t *testing.T) {
	request := RegistrationRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "request-1",
		EnrollmentToken: "sensitive-enrollment-token",
		Hostname:        "worker-01",
		AgentVersion:    "1.2.3",
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"protocol_version", "request_id", "enrollment_token", "hostname", "agent_version"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("wire payload missing %q: %s", key, body)
		}
	}
}

func TestSubjectsAreAgentScopedAndRejectWildcards(t *testing.T) {
	got, err := AgentEventsSubject("agent-01")
	if err != nil || got != "machinarchy.v1.agent.agent-01.events" {
		t.Fatalf("AgentEventsSubject() = %q, %v", got, err)
	}
	if _, err := AgentEventsSubject("agent.*"); err == nil {
		t.Fatal("AgentEventsSubject accepted wildcard input")
	}
	got, err = AgentOperationResultsSubject("agent-01")
	if err != nil || got != "machinarchy.v1.agent.agent-01.operations.results" {
		t.Fatalf("AgentOperationResultsSubject() = %q, %v", got, err)
	}
}

func TestEventEnvelopeRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	want := EventEnvelope{
		ProtocolVersion: ProtocolVersion,
		EventID:         "event-1",
		EventType:       EventOperationRequested,
		TargetAgentID:   "agent-1",
		TargetHostID:    "host-1",
		CorrelationID:   "corr-1",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Minute),
		Payload:         json.RawMessage(`{"operation_id":"op-1","output_limit_bytes":8192}`),
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got EventEnvelope
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != want.EventID || got.TargetAgentID != want.TargetAgentID || got.EventType != want.EventType {
		t.Fatalf("round-trip mismatch: %#v", got)
	}
}

func TestOperationRequestCarriesOptionalOutputLimit(t *testing.T) {
	request := OperationRequest{
		OperationID:      "operation-1",
		Capability:       "exec.command",
		Input:            json.RawMessage(`{"command":"/usr/bin/uptime"}`),
		TimeoutMilli:     30000,
		OutputLimitBytes: 8192,
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var got OperationRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.OutputLimitBytes != request.OutputLimitBytes || got.TimeoutMilli != request.TimeoutMilli {
		t.Fatalf("operation request round-trip mismatch: got %#v, want %#v", got, request)
	}
}

func TestOperationResultAcknowledgementAndUnknownStatusContracts(t *testing.T) {
	if ProtocolVersion != 2 {
		t.Fatalf("ProtocolVersion = %d, want 2", ProtocolVersion)
	}
	ack := OperationResultAcknowledgement{OperationID: "operation-1"}
	body, err := json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	var decoded OperationResultAcknowledgement
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OperationID != ack.OperationID {
		t.Fatalf("acknowledgement round trip = %#v", decoded)
	}
	if OperationStatusUnknown != "unknown" {
		t.Fatalf("unknown operation status = %q", OperationStatusUnknown)
	}
	if EventOperationResultAcknowledged != "operation.result.acknowledged" {
		t.Fatalf("acknowledgement event type = %q", EventOperationResultAcknowledged)
	}
}

func TestAgentEventsConsumerNameIsStableAndSafe(t *testing.T) {
	got, err := AgentEventsConsumerName("agent-01")
	if err != nil || got != "agent_agent-01" {
		t.Fatalf("AgentEventsConsumerName() = %q, %v", got, err)
	}
	for _, invalid := range []string{"", "agent.*", "agent.01", "agent/01", " agent-01", strings.Repeat("a", 123)} {
		if _, err := AgentEventsConsumerName(invalid); err == nil {
			t.Errorf("AgentEventsConsumerName(%q) accepted invalid input", invalid)
		}
	}
}
