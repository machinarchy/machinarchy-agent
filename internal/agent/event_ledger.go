package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const (
	eventLedgerSchemaVersion = 1
	maxRememberedEventIDs    = 4096
	maxEventLedgerFileBytes  = 1024 * 1024
)

type eventLedgerDocument struct {
	SchemaVersion int      `json:"schema_version"`
	EventIDs      []string `json:"event_ids"`
}

// FileEventLedger stores a bounded recent-event deduplication window. It is
// not an operation journal and does not provide durable message delivery.
type FileEventLedger struct {
	path   string
	mu     sync.Mutex
	loaded bool
	ids    map[string]struct{}
	order  []string
}

func NewFileEventLedger(path string) (*FileEventLedger, error) {
	if path == "" || filepath.Clean(path) == "." {
		return nil, errors.New("agent event ledger path must be non-empty")
	}
	return &FileEventLedger{path: expandHome(filepath.Clean(path)), ids: make(map[string]struct{})}, nil
}

func (l *FileEventLedger) Contains(ctx context.Context, eventID string) (bool, error) {
	if ctx == nil {
		return false, errors.New("event ledger context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !validEventIdentifier(eventID) {
		return false, errors.New("event ledger identifier is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return false, err
	}
	_, exists := l.ids[eventID]
	return exists, nil
}

func (l *FileEventLedger) Mark(ctx context.Context, eventID string) error {
	if ctx == nil {
		return errors.New("event ledger context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validEventIdentifier(eventID) {
		return errors.New("event ledger identifier is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadLocked(); err != nil {
		return err
	}
	if _, exists := l.ids[eventID]; exists {
		return nil
	}
	previousOrder := append([]string(nil), l.order...)
	previousIDs := l.ids
	candidateOrder := append(append([]string(nil), l.order...), eventID)
	if len(candidateOrder) > maxRememberedEventIDs {
		candidateOrder = candidateOrder[len(candidateOrder)-maxRememberedEventIDs:]
	}
	candidateIDs := make(map[string]struct{}, len(candidateOrder))
	for _, id := range candidateOrder {
		candidateIDs[id] = struct{}{}
	}
	l.order = candidateOrder
	l.ids = candidateIDs
	if err := l.persistLocked(); err != nil {
		l.order = previousOrder
		l.ids = previousIDs
		return err
	}
	return nil
}

func (l *FileEventLedger) loadLocked() error {
	if l.loaded {
		return nil
	}
	info, err := os.Lstat(l.path)
	if errors.Is(err, os.ErrNotExist) {
		l.loaded = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect agent event ledger: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("agent event ledger must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("agent event ledger permissions are too broad; use mode 0600")
	}
	if info.Size() <= 0 || info.Size() > maxEventLedgerFileBytes {
		return errors.New("agent event ledger has an invalid size")
	}
	file, err := os.Open(l.path)
	if err != nil {
		return fmt.Errorf("open agent event ledger: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEventLedgerFileBytes+1))
	if err != nil {
		return fmt.Errorf("read agent event ledger: %w", err)
	}
	if len(data) > maxEventLedgerFileBytes {
		return errors.New("agent event ledger exceeds the maximum size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document eventLedgerDocument
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode agent event ledger: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode agent event ledger: %w", err)
	}
	if document.SchemaVersion != eventLedgerSchemaVersion || len(document.EventIDs) > maxRememberedEventIDs {
		return errors.New("agent event ledger has an unsupported schema or excessive entry count")
	}
	loadedIDs := make(map[string]struct{}, len(document.EventIDs))
	loadedOrder := make([]string, 0, len(document.EventIDs))
	for _, id := range document.EventIDs {
		if !validEventIdentifier(id) {
			return errors.New("agent event ledger contains an invalid event identifier")
		}
		if _, duplicate := loadedIDs[id]; duplicate {
			return errors.New("agent event ledger contains duplicate identifiers")
		}
		loadedIDs[id] = struct{}{}
		loadedOrder = append(loadedOrder, id)
	}
	l.ids = loadedIDs
	l.order = loadedOrder
	l.loaded = true
	return nil
}

func (l *FileEventLedger) persistLocked() error {
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create agent event ledger directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect agent event ledger directory: %w", err)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return errors.New("agent event ledger directory must be a real directory, not a symlink")
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("agent event ledger directory permissions are too broad; use mode 0700")
	}
	if existing, err := os.Lstat(l.path); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return errors.New("agent event ledger path must be a regular file, not a symlink")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect existing agent event ledger: %w", err)
	}

	document := eventLedgerDocument{SchemaVersion: eventLedgerSchemaVersion, EventIDs: append([]string(nil), l.order...)}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent event ledger: %w", err)
	}
	if len(data) > maxEventLedgerFileBytes {
		return errors.New("encoded agent event ledger exceeds the maximum size")
	}
	tmp, err := os.CreateTemp(dir, ".agent-events-*")
	if err != nil {
		return fmt.Errorf("create temporary agent event ledger: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary agent event ledger: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write agent event ledger: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync agent event ledger: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close agent event ledger: %w", err)
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		return fmt.Errorf("atomically replace agent event ledger: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open agent event ledger directory for sync: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync agent event ledger directory: %w", err)
	}
	return nil
}
