// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// generateEd25519Key generates a new Ed25519 private key.
func generateEd25519Key() (ed25519.PrivateKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return priv, nil
}
