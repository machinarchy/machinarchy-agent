package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileIdentityStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "identity.json")
	store, err := NewFileIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}

	identity := AgentIdentity{SchemaVersion: identitySchemaVersion, PendingRegistrationID: "f0f0f0f0-0000-4000-8000-000000000001"}
	if err := store.Save(context.Background(), identity); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("identity directory mode = %o, want 700", dirInfo.Mode().Perm())
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.PendingRegistrationID != identity.PendingRegistrationID {
		t.Fatalf("loaded identity = %#v", loaded)
	}
}

func TestFileIdentityStoreRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "identity.json")
	store, err := NewFileIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	identity := AgentIdentity{SchemaVersion: identitySchemaVersion, PendingRegistrationID: "pending-request-123"}
	if err := store.Save(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("Load() succeeded for a broadly readable identity file")
	}
}

func TestFileIdentityStoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"pending_registration_id":"pending"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "identity.json")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	store, err := NewFileIdentityStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("Load() succeeded for symlink")
	}
}

func TestFileIdentityStoreMissingIdentity(t *testing.T) {
	store, err := NewFileIdentityStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load(context.Background())
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("Load() error = %v, want ErrIdentityNotFound", err)
	}
}

func TestAgentIdentityValidateRegistered(t *testing.T) {
	identity := AgentIdentity{
		SchemaVersion: identitySchemaVersion,
		AgentID:       "agent-1", HostID: "host-1", LeaseID: "lease-1", LeaseToken: "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), HeartbeatIntervalMillis: 15000,
	}
	if err := identity.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
