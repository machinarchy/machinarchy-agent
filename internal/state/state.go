// Package state persists everything the agent must remember across restarts
// in one atomically replaced, owner-only JSON file: its signing key, the
// controller-assigned identity and lease, the command replay ledger, and
// operations whose results have not been acknowledged yet.
//
// Keeping it in one document means a command can be marked as seen and its
// operation accepted in a single durable write.
package state

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
)

const (
	schemaVersion = 1
	// MaxFileBytes bounds the state file. Operation output is bounded by
	// configuration and the tracked-operation limit keeps the total below it.
	MaxFileBytes = 32 << 20
	// MaxSeenCommands bounds the replay ledger. Entries also expire with their
	// command, so the ledger only grows past this under a flood of valid
	// commands, in which case the oldest entries are dropped.
	MaxSeenCommands = 8192
)

// Phase is the local execution phase of an operation.
type Phase string

const (
	PhaseQueued  Phase = "queued"
	PhaseRunning Phase = "running"
	PhaseDone    Phase = "done"
)

// Identity is assigned by the controller at registration.
type Identity struct {
	AgentID                 string    `json:"agent_id"`
	HostID                  string    `json:"host_id"`
	LeaseID                 string    `json:"lease_id"`
	LeaseExpiresAt          time.Time `json:"lease_expires_at"`
	HeartbeatIntervalMillis int64     `json:"heartbeat_interval_ms"`
	// NATSUserJWT is the latest per-agent NATS credential from the controller
	// (operator mode only). It is not a secret: it is useless without the
	// identity key, which signs the server's nonce.
	NATSUserJWT string `json:"nats_user_jwt,omitempty"`
}

// HeartbeatInterval returns the lease renewal cadence.
func (i Identity) HeartbeatInterval() time.Duration {
	return time.Duration(i.HeartbeatIntervalMillis) * time.Millisecond
}

// Operation is a durably accepted unit of work.
type Operation struct {
	OperationID     string          `json:"operation_id"`
	Capability      string          `json:"capability"`
	Input           json.RawMessage `json:"input,omitempty"`
	TimeoutMillis   int64           `json:"timeout_ms"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Phase           Phase           `json:"phase"`
	AcceptedAt      time.Time       `json:"accepted_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	CancelRequested bool            `json:"cancel_requested,omitempty"`
	Result          *agentv1.Result `json:"result,omitempty"`
	ResultAcked     bool            `json:"result_acked,omitempty"`
}

// Document is the persisted state.
type Document struct {
	SchemaVersion         int                   `json:"schema_version"`
	AgentKeySeed          []byte                `json:"agent_key_seed"`
	PendingRegistrationID string                `json:"pending_registration_id,omitempty"`
	Identity              *Identity             `json:"identity,omitempty"`
	SeenCommands          map[string]time.Time  `json:"seen_commands"`
	Operations            map[string]*Operation `json:"operations"`
}

func (d *Document) validate() error {
	if d.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported state schema version %d", d.SchemaVersion)
	}
	if len(d.AgentKeySeed) != ed25519.SeedSize {
		return errors.New("agent key is missing or malformed")
	}
	if d.Identity != nil && (!agentv1.ValidIdentifier(d.Identity.AgentID) || !agentv1.ValidIdentifier(d.Identity.HostID) || !agentv1.ValidIdentifier(d.Identity.LeaseID)) {
		return errors.New("stored identity is malformed")
	}
	for id, op := range d.Operations {
		if op == nil || op.OperationID != id || !agentv1.ValidIdentifier(id) {
			return errors.New("stored operation is malformed")
		}
	}
	return nil
}

// Store serializes access to the state document and persists every change.
type Store struct {
	mu   sync.Mutex
	path string
	doc  Document
	now  func() time.Time
}

// Open loads the state file, creating it with a new agent key if absent.
func Open(path string) (*Store, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("state file path must be absolute")
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	s := &Store{path: path, now: time.Now}
	doc, err := load(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		seed := make([]byte, ed25519.SeedSize)
		if _, err := io.ReadFull(rand.Reader, seed); err != nil {
			return nil, fmt.Errorf("generate agent key: %w", err)
		}
		doc = Document{SchemaVersion: schemaVersion, AgentKeySeed: seed}
		normalize(&doc)
		if err := write(path, &doc); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	s.doc = doc
	return s, nil
}

// PrivateKey returns the agent's Ed25519 signing key.
func (s *Store) PrivateKey() ed25519.PrivateKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ed25519.NewKeyFromSeed(s.doc.AgentKeySeed)
}

// Snapshot returns a deep copy of the document.
func (s *Store) Snapshot() Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(&s.doc)
}

// Update applies change to a copy of the document and persists it. The
// in-memory state only changes if the write succeeds. Returning an error from
// change aborts without writing.
func (s *Store) Update(change func(*Document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(&s.doc)
	if err := change(&next); err != nil {
		return err
	}
	prune(&next, s.now())
	if err := next.validate(); err != nil {
		return fmt.Errorf("refusing to persist invalid state: %w", err)
	}
	if err := write(s.path, &next); err != nil {
		return err
	}
	s.doc = next
	return nil
}

func normalize(doc *Document) {
	if doc.SeenCommands == nil {
		doc.SeenCommands = map[string]time.Time{}
	}
	if doc.Operations == nil {
		doc.Operations = map[string]*Operation{}
	}
}

// prune drops expired ledger entries and acknowledged operations whose
// command can no longer be delivered, so neither map grows without bound.
func prune(doc *Document, now time.Time) {
	for id, expiry := range doc.SeenCommands {
		if !expiry.After(now) {
			delete(doc.SeenCommands, id)
		}
	}
	for len(doc.SeenCommands) > MaxSeenCommands {
		var oldestID string
		var oldest time.Time
		for id, expiry := range doc.SeenCommands {
			if oldestID == "" || expiry.Before(oldest) {
				oldestID, oldest = id, expiry
			}
		}
		delete(doc.SeenCommands, oldestID)
	}
	for id, op := range doc.Operations {
		if op.ResultAcked && !op.ExpiresAt.After(now) {
			delete(doc.Operations, id)
		}
	}
}

func clone(doc *Document) Document {
	encoded, err := json.Marshal(doc)
	if err != nil {
		panic(fmt.Sprintf("state: clone encode: %v", err)) // only plain data types; cannot fail
	}
	var out Document
	if err := json.Unmarshal(encoded, &out); err != nil {
		panic(fmt.Sprintf("state: clone decode: %v", err))
	}
	normalize(&out)
	return out
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("state directory must be a real directory, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state directory %s must not be accessible by group or others (chmod 0700)", dir)
	}
	return nil
}

func load(path string) (Document, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Document{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Document{}, errors.New("state file must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Document{}, errors.New("state file must not be accessible by group or others (chmod 0600)")
	}
	if info.Size() == 0 || info.Size() > MaxFileBytes {
		return Document{}, errors.New("state file is empty or exceeds the maximum size")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured state path, validated above.
	if err != nil {
		return Document{}, fmt.Errorf("read state file: %w", err)
	}
	var doc Document
	if err := agentv1.DecodeStrict(data, &doc); err != nil {
		return Document{}, fmt.Errorf("decode state file: %w", err)
	}
	normalize(&doc)
	if err := doc.validate(); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func write(path string, doc *Document) error {
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if len(data) > MaxFileBytes {
		return errors.New("state exceeds the maximum file size")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary state file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if existing, err := os.Lstat(path); err == nil && (existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular()) {
		return errors.New("state file path is not a regular file")
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	dirHandle, err := os.Open(dir) // #nosec G304 -- directory of the validated state path.
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	defer func() { _ = dirHandle.Close() }()
	if err := dirHandle.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

// ErrUnhealthy reports that the agent is not enrolled or its lease expired.
var ErrUnhealthy = errors.New("agent is unhealthy")

// Health reads the state file without creating or changing it and reports
// whether the agent holds an unexpired lease. Lease renewals are written to
// the state file, so a stuck or disconnected agent turns unhealthy once its
// lease lapses. It backs `machinarchy-agent --healthcheck`.
func Health(path string, now time.Time) error {
	doc, err := load(path)
	if err != nil {
		return fmt.Errorf("%w: read state: %w", ErrUnhealthy, err)
	}
	switch {
	case doc.Identity == nil || doc.Identity.AgentID == "":
		return fmt.Errorf("%w: not enrolled", ErrUnhealthy)
	case !doc.Identity.LeaseExpiresAt.After(now):
		return fmt.Errorf("%w: lease expired at %s", ErrUnhealthy, doc.Identity.LeaseExpiresAt.Format(time.RFC3339))
	}
	return nil
}
