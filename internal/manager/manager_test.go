package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/discovery"
	"github.com/marcelocantos/dais/internal/session"
)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testScanner(t *testing.T) *discovery.Scanner {
	t.Helper()
	return discovery.NewScanner(t.TempDir())
}

func TestGetInMemory(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))

	// Inject a session directly.
	s := session.New(session.Config{
		ID:       "test-uuid-0000-0000-0000-000000000001",
		Name:     "test session",
		WorkDir:  "/tmp",
		Model:    "opus",
		ClaudeID: "test-uuid-0000-0000-0000-000000000001",
	})
	m.sessions["test-uuid-0000-0000-0000-000000000001"] = s

	got := m.Get("test-uuid-0000-0000-0000-000000000001")
	if got != s {
		t.Error("Get returned different session")
	}
}

func TestGetFromDiscovery(t *testing.T) {
	// Set up discovery directory with a session JSONL file.
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	uuid := "1b55f3b5-f771-42ea-883f-aa8a683ddf75"
	jsonl := `{"cwd":"/Users/test/project","gitBranch":"master"}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	// Get should discover and lazily activate.
	got := m.Get(uuid)
	if got == nil {
		t.Fatal("Get returned nil for discoverable session")
	}
	if got.ID() != uuid {
		t.Errorf("ID = %q, want %q", got.ID(), uuid)
	}
	if got.Name() != "/Users/test/project" {
		t.Errorf("Name = %q, want %q", got.Name(), "/Users/test/project")
	}

	// Second Get should return same instance.
	got2 := m.Get(uuid)
	if got2 != got {
		t.Error("second Get returned different session")
	}
}

func TestGetNotFound(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	got := m.Get("ffffffff-ffff-ffff-ffff-ffffffffffff")
	if got != nil {
		t.Error("expected nil for unknown UUID")
	}
}

func TestGetNonUUID(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	got := m.Get("not-a-uuid")
	if got != nil {
		t.Error("expected nil for non-UUID ID")
	}
}

func TestListMergesDiscoveredAndActive(t *testing.T) {
	base := t.TempDir()
	projDir := filepath.Join(base, "-Users-test-project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	uuid1 := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	uuid2 := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	for _, uuid := range []string{uuid1, uuid2} {
		jsonl := `{"cwd":"/Users/test/project","gitBranch":"master"}` + "\n"
		if err := os.WriteFile(filepath.Join(projDir, uuid+".jsonl"), []byte(jsonl), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	scanner := discovery.NewScanner(base)
	m := New("opus", "/tmp", testDB(t), scanner)

	// Activate one session.
	m.Get(uuid1)

	list := m.List(true)
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list))
	}
}

func TestKill(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))

	s := session.New(session.Config{
		ID:       "deadbeef-dead-beef-dead-beefdeadbeef",
		Name:     "doomed",
		WorkDir:  "/tmp",
		ClaudeID: "deadbeef-dead-beef-dead-beefdeadbeef",
	})
	m.sessions["deadbeef-dead-beef-dead-beefdeadbeef"] = s

	if err := m.Kill("deadbeef-dead-beef-dead-beefdeadbeef"); err != nil {
		t.Fatalf("kill failed: %v", err)
	}
	if s.Status() != session.StatusStopped {
		t.Errorf("expected status %q, got %q", session.StatusStopped, s.Status())
	}
	if m.Get("deadbeef-dead-beef-dead-beefdeadbeef") != nil {
		// Should re-discover from disk if scanner has it, but our test scanner is empty.
	}
}

func TestKillNotFound(t *testing.T) {
	m := New("opus", "/tmp", testDB(t), testScanner(t))
	if err := m.Kill("nonexistent"); err == nil {
		t.Error("expected error killing nonexistent session")
	}
}
