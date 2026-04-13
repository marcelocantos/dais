package mcpserver

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		input string
		max   int
		want  string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello world", 5, "hello\n... (truncated)"},
		{"empty", "", 5, ""},
		{"one_over", "abcdef", 5, "abcde\n... (truncated)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.max)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
			}
		})
	}
}

func TestHandleSessionStatusMissingID(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := s.handleSessionStatus(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestHandleSendCommandMissingID(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"text": "hello"}

	result, err := s.handleSendCommand(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestHandleSendCommandMissingText(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"id": "some-uuid"}

	result, err := s.handleSendCommand(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing text")
	}
}

func TestHandleKillSessionMissingID(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := s.handleKillSession(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing id")
	}
}

func TestHandleActiveWorkNoScanner(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := s.handleActiveWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result when scanner is nil")
	}
}

func TestFormatPRs(t *testing.T) {
	tests := []struct {
		name string
		prs  []prInfo
		want string
	}{
		{"empty", nil, "-"},
		{"single open", []prInfo{{Number: 42, State: "OPEN"}}, "#42 open"},
		{"two open", []prInfo{{Number: 1, State: "OPEN"}, {Number: 2, State: "OPEN"}}, "2 open"},
		{"mixed", []prInfo{{Number: 1, State: "OPEN"}, {Number: 2, State: "MERGED"}}, "#1 open"},
		{"all closed", []prInfo{{Number: 1, State: "MERGED"}}, "0 open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatPRs(tt.prs)
			if got != tt.want {
				t.Errorf("formatPRs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"over", "hello world", 5, "hell…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateStr(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

func TestCollectGitRepos(t *testing.T) {
	// Non-existent base returns nil.
	repos := collectGitRepos("/nonexistent/path/that/does/not/exist")
	if repos != nil {
		t.Errorf("expected nil for nonexistent base, got %v", repos)
	}
}
