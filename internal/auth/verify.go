// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

const serverCertValidity = 365 * 24 * time.Hour

// IssueServerCert issues a server certificate signed by the CA for the given
// hostnames and/or IP addresses.
func (ca *CA) IssueServerCert(hosts []string) (tls.Certificate, error) {
	// Generate an Ed25519 key for the server certificate.
	serverPriv, err := generateEd25519Key()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: generate server key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: generate serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "jevonsd"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(serverCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, serverPriv.Public(), ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: sign server cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(serverPriv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: marshal server key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: build TLS cert: %w", err)
	}
	return tlsCert, nil
}

// TLSConfig returns a *tls.Config configured for mTLS. The server presents a
// certificate signed by the CA and requires clients to do the same.
func (ca *CA) TLSConfig(hosts []string) (*tls.Config, error) {
	serverCert, err := ca.IssueServerCert(hosts)
	if err != nil {
		return nil, err
	}

	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca.cert)

	return &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
