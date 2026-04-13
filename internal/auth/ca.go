// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	caKeyFile  = "ca-key.pem"
	caCertFile = "ca-cert.pem"

	caValidity = 10 * 365 * 24 * time.Hour
)

// CA holds a certificate authority's key and self-signed certificate.
type CA struct {
	key  ed25519.PrivateKey
	cert *x509.Certificate
	// certDER is the raw DER encoding used when building cert pools.
	certDER []byte
}

// NewCA loads an existing CA from dataDir or generates a new one and persists it.
func NewCA(dataDir string) (*CA, error) {
	keyPath := filepath.Join(dataDir, caKeyFile)
	certPath := filepath.Join(dataDir, caCertFile)

	if _, err := os.Stat(keyPath); err == nil {
		// Both files should exist; load them.
		ca, err := loadCA(keyPath, certPath)
		if err != nil {
			return nil, fmt.Errorf("auth: load CA: %w", err)
		}
		slog.Info("loaded CA from disk", "dir", dataDir)
		return ca, nil
	}

	// Generate a new CA.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: create data dir: %w", err)
	}

	priv, err := generateEd25519Key()
	if err != nil {
		return nil, fmt.Errorf("auth: generate CA key: %w", err)
	}
	pub := priv.Public()

	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("auth: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Jevons CA"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(caValidity),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, fmt.Errorf("auth: self-sign CA cert: %w", err)
	}

	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("auth: parse CA cert: %w", err)
	}

	ca := &CA{key: priv, cert: parsed, certDER: certDER}

	if err := ca.persist(keyPath, certPath); err != nil {
		return nil, err
	}

	slog.Info("generated new CA", "dir", dataDir)
	return ca, nil
}

// loadCA reads key and cert PEM files from disk.
func loadCA(keyPath, certPath string) (*CA, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read cert: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("no PEM block in key file")
	}
	privAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	priv, ok := privAny.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("expected ed25519 private key, got %T", privAny)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("no PEM block in cert file")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}

	return &CA{key: priv, cert: cert, certDER: certBlock.Bytes}, nil
}

// persist writes the CA key and cert to disk.
func (ca *CA) persist(keyPath, certPath string) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		return fmt.Errorf("auth: marshal CA key: %w", err)
	}

	if err := writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return fmt.Errorf("auth: write CA key: %w", err)
	}
	if err := writePEM(certPath, "CERTIFICATE", ca.certDER, 0o644); err != nil {
		return fmt.Errorf("auth: write CA cert: %w", err)
	}
	return nil
}

// writePEM encodes data as a PEM block and writes it to path with the given mode.
func writePEM(path, blockType string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: data})
}

// randomSerial returns a random 128-bit certificate serial number.
func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
