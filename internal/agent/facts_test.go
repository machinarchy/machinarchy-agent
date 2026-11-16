package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCollectSystemFactsReturnsPortableBaseline(t *testing.T) {
	collectedAt := time.Date(2026, 11, 14, 12, 30, 0, 0, time.FixedZone("test", 2*60*60))
	facts, err := CollectSystemFacts(context.Background(), "test-version", collectedAt)
	if err != nil {
		t.Fatalf("CollectSystemFacts() error = %v", err)
	}
	if facts.SchemaVersion != "v1" {
		t.Fatalf("schema version = %q, want v1", facts.SchemaVersion)
	}
	if facts.CollectedAt.Location() != time.UTC {
		t.Fatalf("collected timestamp location = %v, want UTC", facts.CollectedAt.Location())
	}
	if facts.OS.Platform == "" || facts.Architecture == "" {
		t.Fatalf("portable platform facts missing: %+v", facts)
	}
	if facts.CPU.LogicalCores < 1 {
		t.Fatalf("logical CPU count = %d, want >= 1", facts.CPU.LogicalCores)
	}
	if facts.Agent.Version != "test-version" {
		t.Fatalf("agent version = %q", facts.Agent.Version)
	}
}

func TestSystemFactsCapabilityRejectsUnexpectedInput(t *testing.T) {
	capability := SystemFactsCapability("test")
	if _, err := capability.Execute(context.Background(), json.RawMessage(`{"include_environment":true}`)); err == nil {
		t.Fatal("expected unexpected input field to be rejected")
	}
}

func TestSystemFactsCapabilityReturnsBoundedJSON(t *testing.T) {
	capability := SystemFactsCapability("test")
	output, err := capability.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(output) == 0 || len(output) > maxFactsOutputBytes {
		t.Fatalf("output size = %d, expected 1..%d", len(output), maxFactsOutputBytes)
	}
	var facts SystemFacts
	if err := json.Unmarshal(output, &facts); err != nil {
		t.Fatalf("output is not valid SystemFacts JSON: %v", err)
	}
}

func TestCollectSystemFactsHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectSystemFacts(ctx, "test", time.Now()); err == nil {
		t.Fatal("expected cancelled context error")
	}
}

func TestSystemFactsDoesNotExposeSensitiveFields(t *testing.T) {
	output, err := SystemFactsCapability("test").Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"environment", "env", "ip_addresses", "machine_id", "processes", "username", "mounts"} {
		if _, exists := value[forbidden]; exists {
			t.Errorf("facts unexpectedly include sensitive field %q", forbidden)
		}
	}
}
