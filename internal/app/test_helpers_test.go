package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"
	"github.com/machinarchy/machinarchy-agent/internal/events"
	"github.com/machinarchy/machinarchy-agent/internal/identity"
	agentv1 "github.com/machinarchy/machinarchy-protocol/agent/v1"
)

func appTestConfig() config.Config {
	return config.Config{
		Environment: "test", LogLevel: "info", ClientName: "machinarchy-agent-test",
		BrokerURLs: "nats://127.0.0.1:4222", ConnectTimeout: time.Second,
		StartupTimeout: time.Second, ReconnectWait: time.Second, DrainTimeout: time.Second,
		RequestTimeout: time.Second, EventBufferSize: config.DefaultEventBufferSize,
		JetStreamStream: agentv1.AgentEventsStreamName, EventNakDelay: 5 * time.Second,
		OperationWorkers: config.DefaultOperationWorkers, OperationQueueSize: config.DefaultOperationQueue,
		MaxOperationTimeout: 5 * time.Minute, MaxOperationInputBytes: config.MaxOperationInputBytes,
		MaxOperationOutputBytes: config.MaxOperationOutputBytes, ResultPublishTimeout: 5 * time.Second,
		IdentityFile: "./data/agent/identity.json", AgentVersion: "test",
		RegistrationSubject: "machinarchy.v1.agent.register", LeaseRenewSubject: "machinarchy.v1.agent.lease.renew",
	}
}

func appTestIdentity() identity.AgentIdentity {
	return identity.AgentIdentity{
		SchemaVersion: identity.IdentitySchemaVersion, AgentID: "agent-123", HostID: "host-456",
		LeaseID: "lease-789", LeaseToken: "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt:          time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		HeartbeatIntervalMillis: 15000,
	}
}

func appTestEvent(agentIdentity identity.AgentIdentity, now time.Time) events.AgentEventEnvelope {
	return events.AgentEventEnvelope{
		ProtocolVersion: agentv1.ProtocolVersion, EventID: "event-001",
		EventType: events.AgentEventOperationRequested, TargetAgentID: agentIdentity.AgentID,
		TargetHostID: agentIdentity.HostID, CorrelationID: "corr-001",
		IssuedAt: now.Add(-time.Second).UTC(), ExpiresAt: now.Add(time.Minute).UTC(),
		Payload: json.RawMessage(`{"operation_id":"operation-001","capability":"runtime.info","input":{},"timeout_ms":30000}`),
	}
}

func appEncodeTestEvent(t *testing.T, event events.AgentEventEnvelope) []byte {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return data
}
