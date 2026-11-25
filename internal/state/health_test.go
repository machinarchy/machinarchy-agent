package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.json")
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if err := Health(path, now); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("missing state = %v, want unhealthy", err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Health(path, now); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("not enrolled = %v, want unhealthy", err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Identity = &Identity{AgentID: "agent-1", HostID: "host-1", LeaseID: "lease-1", LeaseExpiresAt: now.Add(time.Minute), HeartbeatIntervalMillis: 15000}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := Health(path, now); err != nil {
		t.Fatalf("enrolled with a live lease = %v", err)
	}
	if err := Health(path, now.Add(2*time.Minute)); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("expired lease = %v, want unhealthy", err)
	}
}
