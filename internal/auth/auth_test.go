// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"

	"github.com/marcelocantos/jevons/internal/auth"
)

// TestNewCA_Generate verifies that NewCA creates keys on first call.
func TestNewCA_Generate(t *testing.T) {
	ca, err := auth.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	if ca == nil {
		t.Fatal("expected non-nil CA")
	}
}

// TestNewCA_LoadFromDisk verifies that a second NewCA call loads the persisted CA.
func TestNewCA_LoadFromDisk(t *testing.T) {
	dir := t.TempDir()

	ca1, err := auth.NewCA(dir)
	if err != nil {
		t.Fatalf("first NewCA: %v", err)
	}

	ca2, err := auth.NewCA(dir)
	if err != nil {
		t.Fatalf("second NewCA: %v", err)
	}

	// Certificates issued by both CAs should be accepted by each other's TLS
	// config, because they share the same underlying CA cert.
	clientPub, clientPriv := newEd25519(t)

	certPEM1, err := ca1.IssueCert(clientPub, "dev-reload-test")
	if err != nil {
		t.Fatalf("IssueCert ca1: %v", err)
	}
	certPEM2, err := ca2.IssueCert(clientPub, "dev-reload-test")
	if err != nil {
		t.Fatalf("IssueCert ca2: %v", err)
	}

	// Parse both certs; their issuers must be identical.
	c1 := parseCert(t, certPEM1)
	c2 := parseCert(t, certPEM2)
	_ = clientPriv

	if c1.Issuer.CommonName != c2.Issuer.CommonName {
		t.Errorf("issuer CN mismatch: %q vs %q", c1.Issuer.CommonName, c2.Issuer.CommonName)
	}
}

// TestIssueCert verifies CN and ExtKeyUsageClientAuth on issued certificates.
func TestIssueCert(t *testing.T) {
	ca, err := auth.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}

	clientPub, _ := newEd25519(t)

	const deviceID = "test-device-42"
	certPEM, err := ca.IssueCert(clientPub, deviceID)
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}

	cert := parseCert(t, certPEM)

	if cert.Subject.CommonName != deviceID {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, deviceID)
	}

	found := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageClientAuth {
			found = true
			break
		}
	}
	if !found {
		t.Error("issued cert missing ExtKeyUsageClientAuth")
	}
}

// TestTLSConfig_mTLSSuccess verifies a full mTLS handshake with a valid client cert.
func TestTLSConfig_mTLSSuccess(t *testing.T) {
	ca, serverCfg, addr, done := startTLSServer(t)
	defer done()

	clientTLSCert := issueClientCert(t, ca)
	rootPool := serverCfg.ClientCAs

	clientCfg := &tls.Config{
		Certificates: []tls.Certificate{clientTLSCert},
		RootCAs:      rootPool,
		ServerName:   "127.0.0.1",
	}
	conn, err := tls.Dial("tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("dial with valid client cert: %v", err)
	}
	conn.Close()
}

// TestTLSConfig_mTLSNoClientCert verifies that a client without a cert is rejected.
func TestTLSConfig_mTLSNoClientCert(t *testing.T) {
	_, serverCfg, addr, done := startTLSServer(t)
	defer done()

	clientCfg := &tls.Config{
		// No client certificate.
		RootCAs:    serverCfg.ClientCAs,
		ServerName: "127.0.0.1",
	}
	conn, err := tls.Dial("tcp", addr, clientCfg)
	if err != nil {
		// Dial-time rejection is fine.
		return
	}
	defer conn.Close()
	// In TLS 1.3 the server sends certificate_required post-handshake.
	// Force it to arrive by writing something and then reading.
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected read/write to fail without client cert, but it succeeded")
	}
}

// --- helpers ---

// newEd25519 generates a fresh Ed25519 key pair.
func newEd25519(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return pub, priv
}

// parseCert decodes a PEM cert and parses it.
func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block in cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// startTLSServer starts a TLS listener and returns the CA, server config, address,
// and a cleanup function. The server drives the handshake and immediately closes.
func startTLSServer(t *testing.T) (*auth.CA, *tls.Config, string, func()) {
	t.Helper()
	ca, err := auth.NewCA(t.TempDir())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	serverCfg, err := ca.TLSConfig([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tlsConn := c.(*tls.Conn)
				if err := tlsConn.Handshake(); err != nil {
					return
				}
				// Drain so the client can detect the close.
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
			}(conn)
		}
	}()
	return ca, serverCfg, ln.Addr().String(), func() { ln.Close() }
}

// issueClientCert creates an Ed25519 key, issues a client cert from the CA,
// and returns a tls.Certificate ready for use.
func issueClientCert(t *testing.T, ca *auth.CA) tls.Certificate {
	t.Helper()
	pub, priv := newEd25519(t)
	certPEM, err := ca.IssueCert(pub, "test-client")
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return tlsCert
}
