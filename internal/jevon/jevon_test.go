package jevon

import (
	"testing"
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

func TestNewConfig(t *testing.T) {
	j := New(Config{WorkDir: "/tmp", Model: "sonnet"})
	if j.cfg.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q, want /tmp", j.cfg.WorkDir)
	}
	if j.cfg.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet", j.cfg.Model)
	}
}
