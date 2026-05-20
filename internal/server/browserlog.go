// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// handleBrowserLog accepts log events from the browser and forwards
// them to slog with a "browser:" prefix. This puts client-side
// diagnostics in the same log stream as server-side events — useful
// when correlating browser state (PTT engagement, audio-context
// transitions, voice-WS readyState) with server-side events (Grok
// commits, response.done timings).
//
// Request body:
//
//	{
//	  "level":  "info" | "warn" | "error" | "debug",
//	  "msg":    "ptt engage",
//	  "fields": { "key": "value", ... }   // optional
//	}
//
// Always returns 204 — log delivery is best-effort, never blocking
// the browser.
func (s *Server) handleBrowserLog(w http.ResponseWriter, r *http.Request) {
	var entry struct {
		Level  string         `json:"level"`
		Msg    string         `json:"msg"`
		Fields map[string]any `json:"fields,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		// Drop malformed entries silently — we don't want to surface
		// browser-side bugs as 4xx noise.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	args := make([]any, 0, 2*len(entry.Fields))
	for k, v := range entry.Fields {
		args = append(args, k, v)
	}
	msg := "browser: " + entry.Msg
	switch entry.Level {
	case "error":
		slog.Error(msg, args...)
	case "warn":
		slog.Warn(msg, args...)
	case "debug":
		slog.Debug(msg, args...)
	default:
		slog.Info(msg, args...)
	}
	w.WriteHeader(http.StatusNoContent)
}
