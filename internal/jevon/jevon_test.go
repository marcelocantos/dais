package jevon

import (
	"testing"
	"time"
)

func TestFormatPrompt(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
		want   string
	}{
		{
			name:   "empty",
			events: nil,
			want:   "",
		},
		{
			name: "user message",
			events: []Event{
				{Kind: EventUserMessage, Text: "build the login page"},
			},
			want: "[USER] build the login page\n",
		},
		{
			name: "worker completed",
			events: []Event{
				{Kind: EventWorkerCompleted, WorkerName: "auth", WorkerID: "abc", Detail: "login page built"},
			},
			want: "[WORKER auth (abc)] Completed: login page built\n",
		},
		{
			name: "worker failed",
			events: []Event{
				{Kind: EventWorkerFailed, WorkerName: "auth", WorkerID: "abc", Detail: "compile error"},
			},
			want: "[WORKER auth (abc)] Failed: compile error\n",
		},
		{
			name: "worker started",
			events: []Event{
				{Kind: EventWorkerStarted, WorkerName: "auth", WorkerID: "abc"},
			},
			want: "[WORKER auth (abc)] Started\n",
		},
		{
			name: "mixed batch",
			events: []Event{
				{Kind: EventUserMessage, Text: "how's it going?"},
				{Kind: EventWorkerCompleted, WorkerName: "db", WorkerID: "x1", Detail: "migration done"},
				{Kind: EventWorkerFailed, WorkerName: "api", WorkerID: "x2", Detail: "timeout"},
			},
			want: "[USER] how's it going?\n" +
				"[WORKER db (x1)] Completed: migration done\n" +
				"[WORKER api (x2)] Failed: timeout\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatPrompt(tt.events)
			if got != tt.want {
				t.Errorf("FormatPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"HOME=/home/test",
		"CLAUDECODEOTHER=2",
	}
	got := filterEnv(env, "CLAUDECODE")
	want := []string{"PATH=/usr/bin", "HOME=/home/test", "CLAUDECODEOTHER=2"}
	if len(got) != len(want) {
		t.Fatalf("filterEnv: got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("filterEnv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNewAndEnqueue(t *testing.T) {
	j := New(Config{WorkDir: "/tmp", Model: "sonnet"})
	if j.cfg.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q, want /tmp", j.cfg.WorkDir)
	}
	if j.cfg.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", j.cfg.Model)
	}

	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	j.Enqueue(Event{Kind: EventUserMessage, Text: "hello", Timestamp: ts})

	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.queue) != 1 {
		t.Fatalf("queue length = %d, want 1", len(j.queue))
	}
	if j.queue[0].Text != "hello" {
		t.Errorf("queued text = %q, want hello", j.queue[0].Text)
	}
	if j.queue[0].Timestamp != ts {
		t.Errorf("timestamp was overwritten, got %v", j.queue[0].Timestamp)
	}
}

func TestEnqueueSetsTimestamp(t *testing.T) {
	j := New(Config{WorkDir: "/tmp"})
	before := time.Now()
	j.Enqueue(Event{Kind: EventUserMessage, Text: "hi"})
	after := time.Now()

	j.mu.Lock()
	defer j.mu.Unlock()
	ts := j.queue[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Errorf("auto-timestamp %v not in [%v, %v]", ts, before, after)
	}
}

func TestNewWithClaudeID(t *testing.T) {
	j := New(Config{ClaudeID: "abc-123"})
	if j.claudeID != "abc-123" {
		t.Errorf("claudeID = %q, want abc-123", j.claudeID)
	}
}
