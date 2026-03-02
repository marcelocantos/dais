package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsUUID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"1b55f3b5-f771-42ea-883f-aa8a683ddf75", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"ABCDEF01-2345-6789-abcd-ef0123456789", true},
		{"not-a-uuid", false},
		{"1b55f3b5f77142ea883faa8a683ddf75", false}, // no hyphens
		{"1b55f3b5-f771-42ea-883f-aa8a683ddf7", false}, // too short
		{"1b55f3b5-f771-42ea-883f-aa8a683ddf755", false}, // too long
		{"1b55f3b5-f771-42ea-883g-aa8a683ddf75", false}, // invalid hex char
	}
	for _, tt := range tests {
		if got := IsUUID(tt.input); got != tt.want {
			t.Errorf("IsUUID(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDecodeProjectDir(t *testing.T) {
	got := decodeProjectDir("-Users-marcelo-work-github-com-marcelocantos-dais")
	want := "/Users/marcelo/work/github/com/marcelocantos/dais"
	if got != want {
		t.Errorf("decodeProjectDir = %q, want %q", got, want)
	}
}

func TestScanAndGet(t *testing.T) {
	// Create a temp directory structure mimicking ~/.claude/projects/.
	base := t.TempDir()

	projDir := filepath.Join(base, "-Users-test-myproject")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a valid session JSONL file.
	uuid := "1b55f3b5-f771-42ea-883f-aa8a683ddf75"
	jsonl := `{"type":"system","cwd":"/Users/test/myproject","gitBranch":"master","sessionId":"1b55f3b5-f771-42ea-883f-aa8a683ddf75"}
{"type":"user","cwd":"/Users/test/myproject","gitBranch":"master","message":{"role":"user","content":"hello"}}
`
	jsonlPath := filepath.Join(projDir, uuid+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a subagent directory (should be skipped).
	subDir := filepath.Join(projDir, uuid)
	if err := os.MkdirAll(filepath.Join(subDir, "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a memory directory (should be skipped).
	memDir := filepath.Join(base, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create an old session that should be filtered by maxAge.
	oldUUID := "00000000-0000-0000-0000-000000000000"
	oldPath := filepath.Join(projDir, oldUUID+".jsonl")
	if err := os.WriteFile(oldPath, []byte(`{"cwd":"/old"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(oldPath, oldTime, oldTime)

	scanner := NewScanner(base)

	// Test Scan with maxAge filter.
	t.Run("Scan with maxAge", func(t *testing.T) {
		results, err := scanner.Scan(7 * 24 * time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0].UUID != uuid {
			t.Errorf("UUID = %q, want %q", results[0].UUID, uuid)
		}
		if results[0].WorkDir != "/Users/test/myproject" {
			t.Errorf("WorkDir = %q, want %q", results[0].WorkDir, "/Users/test/myproject")
		}
		if results[0].GitBranch != "master" {
			t.Errorf("GitBranch = %q, want %q", results[0].GitBranch, "master")
		}
	})

	// Test Scan without maxAge (all sessions).
	t.Run("Scan all", func(t *testing.T) {
		results, err := scanner.Scan(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 {
			t.Fatalf("got %d results, want 2", len(results))
		}
	})

	// Test Get.
	t.Run("Get existing", func(t *testing.T) {
		info, err := scanner.Get(uuid)
		if err != nil {
			t.Fatal(err)
		}
		if info == nil {
			t.Fatal("Get returned nil")
		}
		if info.WorkDir != "/Users/test/myproject" {
			t.Errorf("WorkDir = %q, want %q", info.WorkDir, "/Users/test/myproject")
		}
	})

	// Test Get non-existent.
	t.Run("Get missing", func(t *testing.T) {
		info, err := scanner.Get("ffffffff-ffff-ffff-ffff-ffffffffffff")
		if err != nil {
			t.Fatal(err)
		}
		if info != nil {
			t.Errorf("expected nil for missing UUID, got %+v", info)
		}
	})
}
