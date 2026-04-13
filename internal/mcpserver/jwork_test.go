// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestHandleJworkMissingText(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{}

	result, err := s.handleJwork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for missing text")
	}
}

func TestHandleJworkMaxDepth(t *testing.T) {
	s := &Server{}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"text":  "do something",
		"depth": float64(maxWorkerDepth),
	}

	result, err := s.handleJwork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Error("expected error result for max depth exceeded")
	}
}

func TestHandleJworkBelowMaxDepth(t *testing.T) {
	s := &Server{}
	// Use a pre-cancelled context so the worker fails immediately
	// without actually spawning claude.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := mcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"text":  "do something",
		"depth": float64(maxWorkerDepth - 1),
	}

	result, err := s.handleJwork(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// Should fail with dispatch/context error, not depth error.
	if result.IsError {
		text := extractText(result)
		if contains(text, "maximum worker depth") {
			t.Error("should not reject at depth maxWorkerDepth-1")
		}
	}
}

func TestExtractHeading(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantText  string
		wantDepth int
	}{
		{"h1", "# Hello", "Hello", 1},
		{"h2", "## Section", "Section", 2},
		{"h3", "### Sub-section", "Sub-section", 3},
		{"h6", "###### Deep", "Deep", 6},
		{"not heading", "regular text", "", 0},
		{"no space", "#nospace", "", 0},
		{"empty", "", "", 0},
		{"code", "```go", "", 0},
		{"leading space", "  ## Indented", "Indented", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, depth := extractHeading(tt.line)
			if text != tt.wantText || depth != tt.wantDepth {
				t.Errorf("extractHeading(%q) = (%q, %d), want (%q, %d)",
					tt.line, text, depth, tt.wantText, tt.wantDepth)
			}
		})
	}
}

func TestHeartbeatThrottling(t *testing.T) {
	hb := &heartbeatTracker{}

	// First emit should always succeed.
	if !hb.shouldEmit(1) {
		t.Error("first emit at level 1 should succeed")
	}

	// Immediate second emit at same level should be throttled.
	if hb.shouldEmit(1) {
		t.Error("immediate second emit at level 1 should be throttled")
	}

	// Different level should succeed independently.
	if !hb.shouldEmit(2) {
		t.Error("first emit at level 2 should succeed")
	}

	// After the throttle interval, same level should succeed again.
	hb.mu.Lock()
	hb.lastByLvl[1] = time.Now().Add(-headingThrottleInterval - time.Millisecond)
	hb.mu.Unlock()

	if !hb.shouldEmit(1) {
		t.Error("emit at level 1 should succeed after throttle interval")
	}
}

func TestDelegationGuidanceInjected(t *testing.T) {
	// When nextDepth >= maxWorkerDepth, delegation guidance should be prepended.
	// We verify the constant is non-empty and contains the key instruction.
	if delegationGuidance == "" {
		t.Error("delegation guidance should not be empty")
	}
	if !contains(delegationGuidance, "maximum delegation depth") {
		t.Error("delegation guidance should mention maximum delegation depth")
	}
}

// helpers

func extractText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
