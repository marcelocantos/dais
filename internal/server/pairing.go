// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/marcelocantos/pigeon"
	"github.com/marcelocantos/pigeon/crypto"
)

// MintArtifact issues a fresh PairingArtifact for the given peer
// instance ID, persisting the server-side PairingRecord to the
// credential store. The returned artifact carries everything the
// client needs for ConnectWithArtifact: relay URL, peer instance ID,
// key material, and expiry.
func (s *Server) MintArtifact(relayURL, instanceID string) (*pigeon.PairingArtifact, error) {
	host := pigeon.NewPairingHost(relayURL)
	artifact, serverRec, err := host.Mint(instanceID)
	if err != nil {
		return nil, fmt.Errorf("mint pairing artifact: %w", err)
	}
	if err := s.creds.Save(serverRec); err != nil {
		return nil, fmt.Errorf("persist server record: %w", err)
	}
	return artifact, nil
}

// AddCredential ingests a server-side PairingRecord from the given
// path (the JSON form emitted by `pigeon pair --server-record-out=...`)
// and persists it to the credential store, replacing any existing
// credential. Designed for out-of-band deploy flows where the artifact
// is minted by an external process.
func (s *Server) AddCredential(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read server record: %w", err)
	}
	var rec crypto.PairingRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("parse server record: %w", err)
	}
	return s.creds.Save(&rec)
}
