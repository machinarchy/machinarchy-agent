package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent")
	path := filepath.Join(dir, "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store, path
}

func TestOpenCreatesPrivateStateWithStableKey(t *testing.T) {
	store, path := openTemp(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.PrivateKey(), reopened.PrivateKey()) {
		t.Fatal("agent key changed across reopen")
	}
}

func TestUpdateIsAtomicAndAbortsOnError(t *testing.T) {
	store, path := openTemp(t)
	if err := store.Update(func(d *Document) error {
		d.Identity = &Identity{AgentID: "a", HostID: "h", LeaseID: "l", HeartbeatIntervalMillis: 10000}
		d.Operations["op"] = &Operation{OperationID: "op", Phase: PhaseQueued, ExpiresAt: time.Now().Add(time.Hour)}
		d.SeenCommands["cmd"] = time.Now().Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("abort")
	if err := store.Update(func(d *Document) error {
		d.Identity = nil
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v", err)
	}
	if store.Snapshot().Identity == nil {
		t.Fatal("aborted update changed in-memory state")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := reopened.Snapshot()
	if doc.Identity == nil || doc.Operations["op"] == nil || doc.SeenCommands["cmd"].IsZero() {
		t.Fatalf("state not persisted: %+v", doc)
	}
}

func TestUpdateRejectsInvalidState(t *testing.T) {
	store, _ := openTemp(t)
	err := store.Update(func(d *Document) error {
		d.Operations["x"] = &Operation{OperationID: "y"}
		return nil
	})
	if err == nil {
		t.Fatal("inconsistent operation key was persisted")
	}
}

func TestPruneExpiresLedgerAndAckedOperations(t *testing.T) {
	store, _ := openTemp(t)
	now := time.Now()
	store.now = func() time.Time { return now }
	if err := store.Update(func(d *Document) error {
		d.SeenCommands["old"] = now.Add(-time.Second)
		d.SeenCommands["new"] = now.Add(time.Minute)
		d.Operations["acked-expired"] = &Operation{OperationID: "acked-expired", ResultAcked: true, ExpiresAt: now.Add(-time.Second)}
		d.Operations["acked-live"] = &Operation{OperationID: "acked-live", ResultAcked: true, ExpiresAt: now.Add(time.Minute)}
		d.Operations["pending"] = &Operation{OperationID: "pending", ExpiresAt: now.Add(-time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	doc := store.Snapshot()
	if _, ok := doc.SeenCommands["old"]; ok {
		t.Fatal("expired ledger entry kept")
	}
	if _, ok := doc.Operations["acked-expired"]; ok {
		t.Fatal("acknowledged expired operation kept")
	}
	for _, id := range []string{"acked-live", "pending"} {
		if doc.Operations[id] == nil {
			t.Fatalf("operation %s pruned too early", id)
		}
	}
}

func TestOpenRejectsUnsafeFiles(t *testing.T) {
	_, path := openTemp(t)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("world-readable state file accepted")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlinked state file accepted")
	}

	if _, err := Open("relative/state.json"); err == nil {
		t.Fatal("relative state path accepted")
	}
}

func TestOpenRejectsCorruptState(t *testing.T) {
	_, path := openTemp(t)
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("state with unknown fields accepted")
	}
}
