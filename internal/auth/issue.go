// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"time"
)

const clientCertValidity = 365 * 24 * time.Hour

// IssueCert signs a client certificate for the given public key.
// The deviceID is embedded as the certificate's Common Name.
// The returned PEM bytes are ready to be sent to the client device.
func (ca *CA) IssueCert(clientPubKey crypto.PublicKey, deviceID string) ([]byte, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, fmt.Errorf("auth: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: deviceID},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(clientCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, clientPubKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("auth: sign client cert: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), nil
}
