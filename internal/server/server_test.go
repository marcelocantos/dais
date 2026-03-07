package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/jevon/internal/db"
	"github.com/marcelocantos/jevon/internal/jevon"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	jev := jevon.New(jevon.Config{WorkDir: t.TempDir()})
	return New(jev, database, "test-v0.0.1")
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t)

	mux := http.NewServeMux()
	s.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if body["version"] != "test-v0.0.1" {
		t.Errorf("version = %v, want test-v0.0.1", body["version"])
	}
}

func TestTranscriptLoadedOnConstruction(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Seed transcript before creating server.
	database.AppendTranscript("user", "hello")
	database.AppendTranscript("jevon", "hi there")

	jev := jevon.New(jevon.Config{WorkDir: t.TempDir()})
	s := New(jev, database, "v0")
	database.Close()

	if len(s.transcript) != 2 {
		t.Fatalf("transcript length = %d, want 2", len(s.transcript))
	}
	if s.transcript[0].Role != "user" || s.transcript[0].Text != "hello" {
		t.Errorf("transcript[0] = {%q, %q}, want {user, hello}",
			s.transcript[0].Role, s.transcript[0].Text)
	}
}
