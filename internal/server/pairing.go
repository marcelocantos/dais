// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/marcelocantos/tern"
	"github.com/marcelocantos/tern/crypto"
)

type KeyPair struct {
	PrivateKey string `json:"private_key"` // base64-encoded X25519 private key
	PublicKey  string `json:"public_key"`  // base64-encoded X25519 public key
}

// LoadOrGenerateKeyPair loads the key pair from ~/.jevon/keypair.json,
// or generates a new one if it doesn't exist.
func (s *Server) LoadOrGenerateKeyPair() error {
	dir := filepath.Join(os.Getenv("HOME"), ".jevon")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create .jevon dir: %w", err)
	}
	path := filepath.Join(dir, "keypair.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read keypair: %w", err)
		}
		// Generate new key pair
		priv, err := ecdh.X25519().GenerateKey(nil)
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		kp := KeyPair{
			PrivateKey: base64.StdEncoding.EncodeToString(priv.Bytes()),
			PublicKey:  base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()),
		}
		data, err := json.Marshal(kp)
		if err != nil {
			return fmt.Errorf("marshal keypair: %w", err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return fmt.Errorf("write keypair: %w", err)
		}
		slog.Info("generated new key pair", "path", path)
		s.serverKP = priv
		s.pubKeyBase64 = kp.PublicKey
		return nil
	}

	var kp KeyPair
	if err := json.Unmarshal(data, &kp); err != nil {
		return fmt.Errorf("unmarshal keypair: %w", err)
	}
	privBytes, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
	if err != nil {
		return fmt.Errorf("decode private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	s.serverKP = priv
	s.pubKeyBase64 = kp.PublicKey
	return nil
}

// handlePairing handles the key exchange after tern registration.
func (s *Server) handlePairing(ctx context.Context, conn *tern.Conn, clientPubKey []byte) error {
	if err := s.LoadOrGenerateKeyPair(); err != nil {
		return fmt.Errorf("load key pair: %w", err)
	}

	clientPub, err := ecdh.X25519().NewPublicKey(clientPubKey)
	if err != nil {
		return fmt.Errorf("parse client pubkey: %w", err)
	}

	code, err := crypto.DeriveConfirmationCode(s.serverKP.PublicKey(), clientPub)
	if err != nil {
		return fmt.Errorf("derive confirmation code: %w", err)
	}
	slog.Info("pairing confirmation", "code", code)
	// TODO: display code for user confirmation, send to client

	// Enable LAN upgrade (full E2E channel TODO)
	conn.SetChannel(nil)

	return nil
}
