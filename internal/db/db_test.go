package db

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestTranscriptRoundTrip(t *testing.T) {
	d := newTestDB(t)

	if err := d.AppendTranscript("user", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := d.AppendTranscript("jevon", "hi there"); err != nil {
		t.Fatal(err)
	}

	entries, err := d.LoadTranscript()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Role != "user" || entries[0].Text != "hello" {
		t.Errorf("entry[0] = {%q, %q}, want {user, hello}", entries[0].Role, entries[0].Text)
	}
	if entries[1].Role != "jevon" || entries[1].Text != "hi there" {
		t.Errorf("entry[1] = {%q, %q}, want {jevon, hi there}", entries[1].Role, entries[1].Text)
	}
}

func TestLoadTranscriptEmpty(t *testing.T) {
	d := newTestDB(t)
	entries, err := d.LoadTranscript()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestKVRoundTrip(t *testing.T) {
	d := newTestDB(t)

	if got := d.Get("missing"); got != "" {
		t.Errorf("Get(missing) = %q, want empty", got)
	}

	if err := d.Set("key1", "value1"); err != nil {
		t.Fatal(err)
	}
	if got := d.Get("key1"); got != "value1" {
		t.Errorf("Get(key1) = %q, want value1", got)
	}

	// Upsert.
	if err := d.Set("key1", "value2"); err != nil {
		t.Fatal(err)
	}
	if got := d.Get("key1"); got != "value2" {
		t.Errorf("Get(key1) after upsert = %q, want value2", got)
	}
}

func TestRawLog(t *testing.T) {
	d := newTestDB(t)
	if err := d.AppendRawLog("jevon", `{"type":"text"}`); err != nil {
		t.Fatal(err)
	}
}
