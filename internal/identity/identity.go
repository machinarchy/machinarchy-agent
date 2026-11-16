package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/machinarchy/machinarchy-agent/internal/config"
)

const IdentitySchemaVersion = 1
const identitySchemaVersion = IdentitySchemaVersion
const maxIdentityFileBytes = 32 * 1024

var ErrIdentityNotFound = errors.New("agent identity not found")

// AgentIdentity is assigned by the Machinarchy control plane. LeaseToken is a
// bearer credential and is therefore persisted only in a private 0600 file.
// PendingRegistrationID makes enrollment retries idempotent after process or
// network failure; it contains no secret.
type AgentIdentity struct {
	SchemaVersion           int    `json:"schema_version"`
	PendingRegistrationID   string `json:"pending_registration_id,omitempty"`
	AgentID                 string `json:"agent_id,omitempty"`
	HostID                  string `json:"host_id,omitempty"`
	LeaseID                 string `json:"lease_id,omitempty"`
	LeaseToken              string `json:"lease_token,omitempty"`
	LeaseExpiresAt          string `json:"lease_expires_at,omitempty"`
	HeartbeatIntervalMillis int64  `json:"heartbeat_interval_ms,omitempty"`
}

// HeartbeatInterval returns the controller-assigned heartbeat interval.
func (i AgentIdentity) HeartbeatInterval() time.Duration {
	return time.Duration(i.HeartbeatIntervalMillis) * time.Millisecond
}

func (i AgentIdentity) Registered() bool {
	return i.AgentID != "" && i.HostID != "" && i.LeaseID != "" && i.LeaseToken != "" && i.LeaseExpiresAt != ""
}

func (i AgentIdentity) Validate() error {
	if i.SchemaVersion != identitySchemaVersion {
		return fmt.Errorf("unsupported agent identity schema version %d", i.SchemaVersion)
	}
	if i.Registered() {
		for _, value := range []string{i.AgentID, i.HostID, i.LeaseID} {
			if len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return errors.New("agent identity contains an invalid identifier")
			}
		}
		if len(i.LeaseToken) < 32 || len(i.LeaseToken) > 4096 {
			return errors.New("agent identity contains an invalid lease credential length")
		}
		if i.HeartbeatIntervalMillis < 5000 || i.HeartbeatIntervalMillis > 300000 {
			return errors.New("agent identity has an invalid heartbeat interval")
		}
		if strings.TrimSpace(i.LeaseToken) != i.LeaseToken || strings.ContainsAny(i.LeaseToken, "\r\n\x00") {
			return errors.New("agent identity has an invalid lease credential")
		}
		if _, err := parseLeaseExpiry(i.LeaseExpiresAt); err != nil {
			return errors.New("agent identity has an invalid lease expiration")
		}
		return nil
	}
	if i.PendingRegistrationID == "" || len(i.PendingRegistrationID) > 64 || strings.TrimSpace(i.PendingRegistrationID) != i.PendingRegistrationID || strings.ContainsAny(i.PendingRegistrationID, "\r\n\x00") || i.AgentID != "" || i.HostID != "" || i.LeaseID != "" || i.LeaseToken != "" || i.LeaseExpiresAt != "" {
		return errors.New("agent identity is neither a valid pending registration nor a complete registered identity")
	}
	return nil
}

// IdentityStore persists agent identity and lease credentials.
type IdentityStore interface {
	Load(context.Context) (AgentIdentity, error)
	Save(context.Context, AgentIdentity) error
}

// FileIdentityStore stores agent identity in an atomically replaced private file.
type FileIdentityStore struct {
	path string
	mu   sync.Mutex
}

func NewFileIdentityStore(path string) (*FileIdentityStore, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path || strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("agent identity file path is invalid")
	}
	return &FileIdentityStore{path: filepath.Clean(config.ExpandHome(path))}, nil
}

func (s *FileIdentityStore) Load(ctx context.Context) (AgentIdentity, error) {
	if ctx == nil {
		return AgentIdentity{}, errors.New("agent identity load context is required")
	}
	if err := ctx.Err(); err != nil {
		return AgentIdentity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := os.Lstat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AgentIdentity{}, ErrIdentityNotFound
		}
		return AgentIdentity{}, fmt.Errorf("inspect agent identity file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return AgentIdentity{}, errors.New("agent identity path must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return AgentIdentity{}, errors.New("agent identity file permissions are too broad; use mode 0600")
	}
	if info.Size() <= 0 || info.Size() > maxIdentityFileBytes {
		return AgentIdentity{}, errors.New("agent identity file has an invalid size")
	}

	file, err := os.Open(s.path)
	if err != nil {
		return AgentIdentity{}, fmt.Errorf("open agent identity file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxIdentityFileBytes+1))
	if err != nil {
		return AgentIdentity{}, fmt.Errorf("read agent identity file: %w", err)
	}
	if len(data) > maxIdentityFileBytes {
		return AgentIdentity{}, errors.New("agent identity file exceeds the maximum size")
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var identity AgentIdentity
	if err := decoder.Decode(&identity); err != nil {
		return AgentIdentity{}, fmt.Errorf("decode agent identity file: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return AgentIdentity{}, fmt.Errorf("decode agent identity file: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return AgentIdentity{}, fmt.Errorf("validate agent identity file: %w", err)
	}
	return identity, nil
}

func (s *FileIdentityStore) Save(ctx context.Context, identity AgentIdentity) error {
	if ctx == nil {
		return errors.New("agent identity save context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("validate agent identity: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent identity directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect agent identity directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return errors.New("agent identity directory must be a real directory, not a symlink")
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("agent identity directory permissions are too broad; use mode 0700")
	}
	if existing, err := os.Lstat(s.path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return errors.New("agent identity path must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing agent identity file: %w", err)
	}

	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent identity: %w", err)
	}
	if len(data) > maxIdentityFileBytes {
		return errors.New("encoded agent identity exceeds the maximum size")
	}

	tmp, err := os.CreateTemp(dir, ".agent-identity-*")
	if err != nil {
		return fmt.Errorf("create temporary agent identity file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set agent identity file permissions: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write agent identity file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync agent identity file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close agent identity file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("atomically replace agent identity file: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open agent identity directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync agent identity directory: %w", err)
	}
	return nil
}

func NewRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate agent request identifier: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(value[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func parseLeaseExpiry(value string) (time.Time, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return time.Time{}, errors.New("lease expiration is empty or malformed")
	}
	return time.Parse(time.RFC3339Nano, value)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// ErrLeaseExpired indicates that the persisted control-plane lease has expired.
var ErrLeaseExpired = errors.New("agent lease expired")

// EnsureLeaseAlive rejects operations using an expired or malformed lease.
func EnsureLeaseAlive(identity AgentIdentity) error {
	expiresAt, err := time.Parse(time.RFC3339Nano, identity.LeaseExpiresAt)
	if err != nil {
		return fmt.Errorf("parse stored agent lease expiration: %w", err)
	}
	if !time.Now().Before(expiresAt) {
		return fmt.Errorf("%w; agent must be re-enrolled or the control plane must restore its identity", ErrLeaseExpired)
	}
	return nil
}
