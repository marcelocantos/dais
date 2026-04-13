// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/marcelocantos/claudia"
)

const (
	// maxWorkerDepth is the maximum nesting depth for jwork calls.
	// Workers at this depth cannot spawn further workers.
	maxWorkerDepth = 3

	// headingThrottleInterval is the minimum time between progress
	// heartbeats for headings at the same depth.
	headingThrottleInterval = 2 * time.Second
)

// headingPattern matches markdown headings (# through ######).
var headingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

// registerJwork adds the jwork MCP tool to the server.
func (s *Server) registerJwork() {
	s.mcpSrv.AddTool(
		mcp.NewTool("jwork",
			mcp.WithDescription(
				"Dispatch a task to an on-demand Claude Code worker. "+
					"The worker is a fresh subprocess that runs the task to completion and returns the result. "+
					"Task description must be self-contained — no implicit context is injected."),
			mcp.WithString("text", mcp.Required(), mcp.Description("Task description. Must be self-contained — the worker has no prior context.")),
			mcp.WithString("cwd", mcp.Description("Working directory for the worker (defaults to the coordinator's default)")),
			mcp.WithString("model", mcp.Description("Model override (e.g. 'opus', 'sonnet')")),
			mcp.WithNumber("depth", mcp.Description("Current call depth (0 = top-level). Workers increment this when calling jwork themselves. Do not set manually.")),
		),
		s.handleJwork,
	)
}

func (s *Server) handleJwork(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	text, _ := args["text"].(string)
	cwd, _ := args["cwd"].(string)
	model, _ := args["model"].(string)
	depthF, _ := args["depth"].(float64)
	depth := int(depthF)

	if text == "" {
		return mcp.NewToolResultError("missing required parameter: text"), nil
	}
	if depth >= maxWorkerDepth {
		return mcp.NewToolResultError(fmt.Sprintf(
			"maximum worker depth (%d) reached — cannot spawn further workers. "+
				"Complete the task directly instead of delegating.", maxWorkerDepth)), nil
	}
	if cwd == "" {
		cwd = s.workerWD
	}
	// Expand ~ in cwd.
	if strings.HasPrefix(cwd, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home + cwd[1:]
		}
	}

	workerID := uuid.New().String()[:8]
	nextDepth := depth + 1

	slog.Info("jwork: dispatching worker",
		"worker", workerID,
		"depth", depth,
		"cwd", cwd,
		"model", model,
	)

	// Build the prompt. At higher depths, inject delegation guidance.
	prompt := text
	if nextDepth >= maxWorkerDepth {
		prompt = delegationGuidance + "\n\n" + text
	}

	task := claudia.NewTask(claudia.TaskConfig{
		ID:      workerID,
		Name:    fmt.Sprintf("jwork-d%d-%s", depth, workerID),
		WorkDir: cwd,
		Model:   model,
	})
	if s.db != nil {
		task.SetRawLog(func(line []byte) {
			if err := s.db.AppendRawLog(workerID, string(line)); err != nil {
				slog.Error("jwork: failed to persist raw log", "worker", workerID, "err", err)
			}
		})
	}

	events, err := task.RunTask(ctx, prompt)
	if err != nil {
		slog.Error("jwork: dispatch failed", "worker", workerID, "err", err)
		return mcp.NewToolResultError(fmt.Sprintf("worker dispatch failed: %v", err)), nil
	}

	// Collect result while extracting progress heartbeats.
	var textParts []string
	hb := &heartbeatTracker{}

	for ev := range events {
		switch ev.Type {
		case claudia.TaskEventText:
			textParts = append(textParts, ev.Content)
			// Check each line for markdown headings.
			for _, line := range strings.Split(ev.Content, "\n") {
				if heading, hdepth := extractHeading(line); heading != "" {
					if hb.shouldEmit(hdepth) {
						slog.Info("jwork: progress",
							"worker", workerID,
							"depth", depth,
							"heading_depth", hdepth,
							"heading", heading,
						)
						s.broadcastWorkerProgress(workerID, depth, heading)
					}
				}
			}
		case claudia.TaskEventError:
			slog.Warn("jwork: worker error", "worker", workerID, "error", ev.ErrorMsg)
		}
	}

	result := task.LastResult()
	if result == "" {
		result = strings.Join(textParts, "")
	}
	if result == "" {
		result = "Worker finished (no output)."
	}

	slog.Info("jwork: worker complete",
		"worker", workerID,
		"depth", depth,
		"result_len", len(result),
	)

	return mcp.NewToolResultText(truncate(result, 4000)), nil
}

// extractHeading parses a markdown heading line, returning the text
// and heading depth (1-6). Returns ("", 0) if not a heading.
func extractHeading(line string) (string, int) {
	m := headingPattern.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", 0
	}
	return m[2], len(m[1])
}

// heartbeatTracker throttles progress heartbeats by heading depth.
type heartbeatTracker struct {
	mu        sync.Mutex
	lastByLvl map[int]time.Time
}

func (h *heartbeatTracker) shouldEmit(level int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.lastByLvl == nil {
		h.lastByLvl = make(map[int]time.Time)
	}

	now := time.Now()
	if last, ok := h.lastByLvl[level]; ok && now.Sub(last) < headingThrottleInterval {
		return false
	}
	h.lastByLvl[level] = now
	return true
}

// broadcastWorkerProgress sends a progress heartbeat to the web UI
// and notifies the Jevon overseer.
func (s *Server) broadcastWorkerProgress(workerID string, depth int, heading string) {
	s.mu.Lock()
	fn := s.notifyJevon
	s.mu.Unlock()

	if fn != nil {
		fn(fmt.Sprintf("[jwork %s d%d] %s", workerID, depth, heading))
	}
}

const delegationGuidance = `IMPORTANT: You are at the maximum delegation depth. ` +
	`You MUST complete this task directly — do not attempt to delegate to sub-workers ` +
	`or call the jwork tool. If the task is too large, do what you can and clearly ` +
	`state what remains unfinished.`
