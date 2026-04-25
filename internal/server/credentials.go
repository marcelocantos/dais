// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/marcelocantos/pigeon/crypto"
)

// CredentialStore persists the server-side PairingRecord — the
// counterpart to the device-side PairingArtifact in pigeon's
// CredentialStore. Single-credential for now; multi-device support
// arrives with a future target (jevons typically has one paired iPad).
type CredentialStore struct {
	mu     sync.RWMutex
	path   string
	record *crypto.PairingRecord
}

// NewCredentialStore returns a store backed by the given path. Creates
// no files until Save is called.
func NewCredentialStore(path string) *CredentialStore {
	return &CredentialStore{path: path}
}

// Load reads the persisted PairingRecord from disk and caches it.
// Returns nil with no error when no credential has been saved yet.
func (s *CredentialStore) Load() (*crypto.PairingRecord, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read credential: %w", err)
	}
	var rec crypto.PairingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse credential: %w", err)
	}
	s.mu.Lock()
	s.record = &rec
	s.mu.Unlock()
	return &rec, nil
}

// Save writes the PairingRecord to disk, replacing any existing one,
// and updates the in-memory cache.
func (s *CredentialStore) Save(rec *crypto.PairingRecord) error {
	if rec == nil {
		return errors.New("cannot save nil record")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("mkdir credential dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename credential: %w", err)
	}
	s.mu.Lock()
	s.record = rec
	s.mu.Unlock()
	return nil
}

// Get returns the cached PairingRecord, or nil if none is loaded.
func (s *CredentialStore) Get() *crypto.PairingRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.record
}

// Path returns the on-disk path.
func (s *CredentialStore) Path() string { return s.path }
