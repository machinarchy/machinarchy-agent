package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
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
)

const (
	operationStoreSchemaVersion = 1
	maxOperationStoreEntries    = 4096
	maxTrackedOperations        = 128
	maxOperationStoreBytes      = 48 * 1024 * 1024
	maxPendingResultBytes       = 24 * 1024 * 1024
	resultRetryInterval         = 15 * time.Second
)

// StoredOperation is the durable local record for one controller-assigned
// operation ID. Result remains present until the controller explicitly
// acknowledges that it has persisted the result.
type StoredOperation struct {
	OperationID        string                  `json:"operation_id"`
	RequestDigest      string                  `json:"request_digest"`
	Capability         string                  `json:"capability,omitempty"`
	Request            OperationRequestPayload `json:"request,omitempty"`
	Event              AgentEventEnvelope      `json:"event"`
	Status             OperationStatus         `json:"status"`
	AcceptedAt         time.Time               `json:"accepted_at,omitempty"`
	StartedAt          time.Time               `json:"started_at,omitempty"`
	CompletedAt        time.Time               `json:"completed_at,omitempty"`
	Result             *OperationResult        `json:"result,omitempty"`
	LastPublishedAt    time.Time               `json:"last_published_at,omitempty"`
	ResultAcknowledged bool                    `json:"result_acknowledged,omitempty"`
}

type OperationStore interface {
	List(context.Context) ([]StoredOperation, error)
	Get(context.Context, string) (StoredOperation, bool, error)
	Accept(context.Context, StoredOperation) (StoredOperation, bool, error)
	MarkRunning(context.Context, string, time.Time) error
	MarkCancellation(context.Context, string, AgentEventEnvelope) error
	Complete(context.Context, string, OperationResult) error
	PendingResults(context.Context, time.Time) ([]OperationResult, error)
	MarkResultPublished(context.Context, string, time.Time) error
	AcknowledgeResult(context.Context, string) error
}

type operationStoreDocument struct {
	SchemaVersion int               `json:"schema_version"`
	Operations    []StoredOperation `json:"operations"`
}

// FileOperationStore is a deliberately small local durable store. It uses an
// atomically replaced, private JSON snapshot instead of introducing a database
// dependency into the Agent. The bounded document is suitable for operation
// metadata and pending results; it is not a general-purpose data store.
type FileOperationStore struct {
	path    string
	mu      sync.Mutex
	loaded  bool
	entries map[string]StoredOperation
	order   []string
}

func NewFileOperationStore(path string) (*FileOperationStore, error) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path || strings.ContainsAny(path, "\r\n\x00") {
		return nil, errors.New("agent operation store path is invalid")
	}
	return &FileOperationStore{path: expandHome(filepath.Clean(path)), entries: make(map[string]StoredOperation)}, nil
}

// NewMemoryOperationStore is used by unit tests and embedders that explicitly
// do not require persistence. Production sessions use FileOperationStore.
func NewMemoryOperationStore() *FileOperationStore {
	return &FileOperationStore{loaded: true, entries: make(map[string]StoredOperation)}
}

func (s *FileOperationStore) List(ctx context.Context) ([]StoredOperation, error) {
	if err := validateStoreContext(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	result := make([]StoredOperation, 0, len(s.order))
	for _, id := range s.order {
		if entry, ok := s.entries[id]; ok {
			result = append(result, cloneStoredOperation(entry))
		}
	}
	return result, nil
}

func (s *FileOperationStore) Get(ctx context.Context, operationID string) (StoredOperation, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return StoredOperation{}, false, err
	}
	if !validEventIdentifier(operationID) {
		return StoredOperation{}, false, errors.New("operation store identifier is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return StoredOperation{}, false, err
	}
	entry, ok := s.entries[operationID]
	return cloneStoredOperation(entry), ok, nil
}

func (s *FileOperationStore) Accept(ctx context.Context, entry StoredOperation) (StoredOperation, bool, error) {
	if err := validateStoreContext(ctx); err != nil {
		return StoredOperation{}, false, err
	}
	if !validEventIdentifier(entry.OperationID) || !validCapabilityName(entry.Capability) || entry.Status != OperationStatusAccepted || entry.AcceptedAt.IsZero() {
		return StoredOperation{}, false, errors.New("operation store acceptance record is invalid")
	}
	if len(entry.Request.Input) == 0 || !json.Valid(entry.Request.Input) {
		return StoredOperation{}, false, errors.New("operation store request input is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return StoredOperation{}, false, err
	}
	entry.RequestDigest = operationRequestDigest(entry.Request)
	if existing, ok := s.entries[entry.OperationID]; ok {
		if existing.RequestDigest != "" && existing.RequestDigest != entry.RequestDigest || existing.RequestDigest == "" && (existing.Capability != entry.Capability || !bytes.Equal(existing.Request.Input, entry.Request.Input)) {
			return StoredOperation{}, false, errors.New("operation ID was reused with a different capability or input")
		}
		return cloneStoredOperation(existing), false, nil
	}
	if trackedOperationCount(s.entries) >= maxTrackedOperations {
		return StoredOperation{}, false, errors.New("agent durable operation capacity is full; waiting for controller result acknowledgements")
	}
	candidate := cloneOperationEntries(s.entries)
	order := append([]string(nil), s.order...)
	pruneAcknowledgedOperations(candidate, &order)
	if len(candidate) >= maxOperationStoreEntries {
		return StoredOperation{}, false, errors.New("agent operation history is full; acknowledged history cannot be pruned")
	}
	entry = cloneStoredOperation(entry)
	candidate[entry.OperationID] = entry
	order = append(order, entry.OperationID)
	if err := s.persistCandidateLocked(candidate, order); err != nil {
		return StoredOperation{}, false, err
	}
	s.entries, s.order = candidate, order
	return cloneStoredOperation(entry), true, nil
}

func (s *FileOperationStore) MarkRunning(ctx context.Context, operationID string, at time.Time) error {
	return s.update(ctx, operationID, func(entry *StoredOperation) error {
		if entry.Status == OperationStatusRunning {
			return nil
		}
		if entry.Status != OperationStatusAccepted {
			return errors.New("operation is not in accepted state")
		}
		entry.Status, entry.StartedAt = OperationStatusRunning, at.UTC()
		// The worker already holds its in-memory request. The durable record only
		// needs the digest and lifecycle metadata once execution has started.
		entry.Request.Input = nil
		entry.Event.Payload = nil
		return nil
	})
}

func (s *FileOperationStore) MarkCancellation(ctx context.Context, operationID string, event AgentEventEnvelope) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if !validEventIdentifier(operationID) {
		return errors.New("operation store identifier is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	candidate := cloneOperationEntries(s.entries)
	order := append([]string(nil), s.order...)
	entry, ok := candidate[operationID]
	if !ok {
		entry = StoredOperation{OperationID: operationID, Event: event, Status: OperationStatusCancelled, CompletedAt: time.Now().UTC()}
		candidate[operationID] = entry
		order = append(order, operationID)
	} else if entry.Status == OperationStatusAccepted || entry.Status == OperationStatusRunning || entry.Status == OperationStatusCancelRequest {
		entry.Status = OperationStatusCancelRequest
		entry.Event = event
		candidate[operationID] = entry
	}
	pruneAcknowledgedOperations(candidate, &order)
	if len(candidate) > maxOperationStoreEntries {
		return errors.New("agent operation history is full")
	}
	if err := s.persistCandidateLocked(candidate, order); err != nil {
		return err
	}
	s.entries, s.order = candidate, order
	return nil
}

func (s *FileOperationStore) Complete(ctx context.Context, operationID string, result OperationResult) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if !validEventIdentifier(operationID) || result.OperationID != operationID || !isTerminalOperationStatus(result.Status) {
		return errors.New("operation completion record is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	entry, ok := s.entries[operationID]
	if !ok {
		return errors.New("operation completion has no accepted local record")
	}
	entry.Status = result.Status
	entry.CompletedAt = result.CompletedAt
	// Inputs are no longer needed after a terminal outcome. Keep the digest for
	// deduplication while avoiding unnecessary retention of scripts/secrets.
	entry.Request.Input = nil
	entry.Event.Payload = nil
	if entry.CompletedAt.IsZero() {
		entry.CompletedAt = time.Now().UTC()
		result.CompletedAt = entry.CompletedAt
	}
	entry.StartedAt = result.StartedAt
	entry.ResultAcknowledged = false
	entry.LastPublishedAt = time.Time{}
	entry.Result = cloneOperationResult(&result)
	candidate := cloneOperationEntries(s.entries)
	candidate[operationID] = entry
	if pendingResultBytes(candidate) > maxPendingResultBytes {
		// Preserve the authoritative outcome even when output retention reaches
		// its disk budget. The controller receives the status and error code.
		result.Output = nil
		result.ErrorCode = "result_output_omitted_storage_limit"
		entry.Result = cloneOperationResult(&result)
		candidate[operationID] = entry
	}
	if err := s.persistCandidateLocked(candidate, s.order); err != nil {
		return err
	}
	s.entries = candidate
	return nil
}

func (s *FileOperationStore) PendingResults(ctx context.Context, now time.Time) ([]OperationResult, error) {
	if err := validateStoreContext(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	results := make([]OperationResult, 0)
	for _, id := range s.order {
		entry, ok := s.entries[id]
		if !ok || entry.Result == nil || entry.ResultAcknowledged {
			continue
		}
		if !entry.LastPublishedAt.IsZero() && now.Sub(entry.LastPublishedAt) < resultRetryInterval {
			continue
		}
		results = append(results, *cloneOperationResult(entry.Result))
	}
	return results, nil
}

func (s *FileOperationStore) MarkResultPublished(ctx context.Context, operationID string, at time.Time) error {
	return s.update(ctx, operationID, func(entry *StoredOperation) error {
		// An acknowledgement may arrive immediately after Publish returns and
		// before this timestamp update. That ordering is valid and idempotent.
		if entry.ResultAcknowledged {
			return nil
		}
		if entry.Result == nil {
			return errors.New("operation has no pending result")
		}
		entry.LastPublishedAt = at.UTC()
		return nil
	})
}

func (s *FileOperationStore) AcknowledgeResult(ctx context.Context, operationID string) error {
	return s.update(ctx, operationID, func(entry *StoredOperation) error {
		if entry.Result == nil && entry.ResultAcknowledged {
			return nil
		}
		if entry.Result == nil {
			return errors.New("operation result is not pending acknowledgement")
		}
		entry.ResultAcknowledged = true
		entry.Result = nil
		entry.Request.Input = nil
		entry.Event.Payload = nil
		entry.LastPublishedAt = time.Time{}
		return nil
	})
}

func (s *FileOperationStore) update(ctx context.Context, operationID string, change func(*StoredOperation) error) error {
	if err := validateStoreContext(ctx); err != nil {
		return err
	}
	if !validEventIdentifier(operationID) {
		return errors.New("operation store identifier is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	entry, ok := s.entries[operationID]
	if !ok {
		return errors.New("operation does not exist in durable store")
	}
	if err := change(&entry); err != nil {
		return err
	}
	candidate := cloneOperationEntries(s.entries)
	candidate[operationID] = entry
	if err := s.persistCandidateLocked(candidate, s.order); err != nil {
		return err
	}
	s.entries = candidate
	return nil
}

func (s *FileOperationStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	dir := filepath.Dir(s.path)
	dirInfo, dirErr := os.Lstat(dir)
	if dirErr == nil {
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
			return errors.New("agent operation store directory must be a real directory, not a symlink")
		}
		if dirInfo.Mode().Perm()&0o077 != 0 {
			return errors.New("agent operation store directory permissions are too broad; use mode 0700")
		}
	} else if !errors.Is(dirErr, os.ErrNotExist) {
		return fmt.Errorf("inspect agent operation store directory: %w", dirErr)
	}
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect agent operation store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("agent operation store must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("agent operation store permissions are too broad; use mode 0600")
	}
	if info.Size() <= 0 || info.Size() > maxOperationStoreBytes {
		return errors.New("agent operation store has an invalid size")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("open agent operation store: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxOperationStoreBytes+1))
	if err != nil {
		return fmt.Errorf("read agent operation store: %w", err)
	}
	if len(data) > maxOperationStoreBytes {
		return errors.New("agent operation store exceeds its maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document operationStoreDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode agent operation store: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode agent operation store: %w", err)
	}
	if document.SchemaVersion != operationStoreSchemaVersion || len(document.Operations) > maxOperationStoreEntries {
		return errors.New("agent operation store has an unsupported schema or excessive history")
	}
	entries := make(map[string]StoredOperation, len(document.Operations))
	order := make([]string, 0, len(document.Operations))
	for _, entry := range document.Operations {
		if !validEventIdentifier(entry.OperationID) || entry.Status == "" {
			return errors.New("agent operation store contains an invalid operation record")
		}
		if _, duplicate := entries[entry.OperationID]; duplicate {
			return errors.New("agent operation store contains duplicate operation IDs")
		}
		if entry.Result != nil && entry.Result.OperationID != entry.OperationID {
			return errors.New("agent operation store contains a mismatched result")
		}
		entries[entry.OperationID] = cloneStoredOperation(entry)
		order = append(order, entry.OperationID)
	}
	s.entries, s.order, s.loaded = entries, order, true
	return nil
}

func (s *FileOperationStore) persistCandidateLocked(entries map[string]StoredOperation, order []string) error {
	if s.path == "" {
		return nil
	}
	persisted := false
	defer func() {
		if !persisted {
			// A failure after rename can leave the on-disk snapshot newer than the
			// in-memory view. Force the next operation to reload from disk.
			s.loaded = false
		}
	}()
	operations := make([]StoredOperation, 0, len(order))
	for _, id := range order {
		if entry, ok := entries[id]; ok {
			operations = append(operations, cloneStoredOperation(entry))
		}
	}
	data, err := json.MarshalIndent(operationStoreDocument{SchemaVersion: operationStoreSchemaVersion, Operations: operations}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent operation store: %w", err)
	}
	if len(data) > maxOperationStoreBytes {
		return errors.New("agent operation store exceeds its maximum size")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent operation store directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect agent operation store directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return errors.New("agent operation store directory must be a real directory, not a symlink")
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("agent operation store directory permissions are too broad; use mode 0700")
	}
	if existing, err := os.Lstat(s.path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return errors.New("agent operation store path must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing agent operation store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".agent-operations-*")
	if err != nil {
		return fmt.Errorf("create temporary agent operation store: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary agent operation store: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write agent operation store: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync agent operation store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close agent operation store: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("atomically replace agent operation store: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open agent operation store directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync agent operation store directory: %w", err)
	}
	persisted = true
	return nil
}

func operationRequestDigest(request OperationRequestPayload) string {
	payload, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func validateStoreContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("operation store context is required")
	}
	return ctx.Err()
}

func trackedOperationCount(entries map[string]StoredOperation) int {
	count := 0
	for _, entry := range entries {
		if !isTerminalOperationStatus(entry.Status) || (entry.Result != nil && !entry.ResultAcknowledged) {
			count++
		}
	}
	return count
}

func pendingResultBytes(entries map[string]StoredOperation) int {
	total := 0
	for _, entry := range entries {
		if entry.Result != nil && !entry.ResultAcknowledged {
			total += len(entry.Result.Output) + 2048
		}
	}
	return total
}

func pruneAcknowledgedOperations(entries map[string]StoredOperation, order *[]string) {
	for len(entries) >= maxOperationStoreEntries {
		removed := false
		for index, id := range *order {
			entry, ok := entries[id]
			if ok && entry.ResultAcknowledged && isTerminalOperationStatus(entry.Status) && !entry.Event.ExpiresAt.After(time.Now()) {
				delete(entries, id)
				*order = append((*order)[:index], (*order)[index+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			return
		}
	}
}

func cloneOperationEntries(source map[string]StoredOperation) map[string]StoredOperation {
	result := make(map[string]StoredOperation, len(source))
	for id, entry := range source {
		result[id] = cloneStoredOperation(entry)
	}
	return result
}

func cloneStoredOperation(entry StoredOperation) StoredOperation {
	entry.Request.Input = append(json.RawMessage(nil), entry.Request.Input...)
	entry.Event.Payload = append(json.RawMessage(nil), entry.Event.Payload...)
	entry.Result = cloneOperationResult(entry.Result)
	return entry
}

func cloneOperationResult(result *OperationResult) *OperationResult {
	if result == nil {
		return nil
	}
	copy := *result
	copy.Output = append(json.RawMessage(nil), result.Output...)
	return &copy
}

var _ OperationStore = (*FileOperationStore)(nil)
