// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package qr

import (
	"bytes"
	"strings"
	"testing"
)

func TestServerURL(t *testing.T) {
	url, err := ServerURL(13705)
	if err != nil {
		t.Fatalf("ServerURL: %v", err)
	}
	if !strings.HasPrefix(url, "jevon://") {
		t.Errorf("expected jevon:// prefix, got %q", url)
	}
	if !strings.HasSuffix(url, ":13705") {
		t.Errorf("expected :13705 suffix, got %q", url)
	}
}

func TestPrint(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, "jevon://192.168.1.42:13705")
	out := buf.String()
	if !strings.Contains(out, "jevon://192.168.1.42:13705") {
		t.Errorf("expected URL in output, got %q", out)
	}
	// Should contain Unicode block characters.
	if !strings.ContainsAny(out, "\u2580\u2584\u2588") {
		t.Errorf("expected QR block characters in output")
	}
}
