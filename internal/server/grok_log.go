// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// GrokLogEntry is one line of the Grok conversation JSONL. Role
// follows Grok's own taxonomy (user / assistant / system) so the log
// can be replayed verbatim into a fresh Grok session via
// conversation.item.create. Application-specific distinctions
// (modality, originating worker, task id) live in Meta.
type GrokLogEntry struct {
	Timestamp time.Time      `json:"timestamp"`
	Role      string         `json:"role"`              // "user" | "assistant" | "system"
	Content   string         `json:"content"`           // plain text body
	Meta      map[string]any `json:"meta,omitempty"`    // modality, agent, task_id, kind, ...
}

// GrokLog is an append-only JSONL of Grok conversation events. One
// instance per persistent Jevons conversation. Concurrent appends are
// serialised; reads are independent of the write handle.
type GrokLog struct {
	path string

	mu sync.Mutex
	f  *os.File
}

// NewGrokLog opens (or creates) the JSONL at path for append. The
// parent directory is created if necessary.
func NewGrokLog(path string) (*GrokLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("grok log: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("grok log: open: %w", err)
	}
	return &GrokLog{path: path, f: f}, nil
}

// Path returns the absolute file path of the log.
func (g *GrokLog) Path() string { return g.path }

// Append serialises and appends one entry. Safe for concurrent use.
// If Timestamp is zero, it's set to the current time.
func (g *GrokLog) Append(e GrokLogEntry) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("grok log: marshal: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, err := g.f.Write(data); err != nil {
		return fmt.Errorf("grok log: write: %w", err)
	}
	if _, err := g.f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("grok log: write newline: %w", err)
	}
	return nil
}

// Close releases the file handle.
func (g *GrokLog) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.f == nil {
		return nil
	}
	err := g.f.Close()
	g.f = nil
	return err
}

// Tail returns the last n entries from the log, oldest first. Opens
// the file independently of the write handle, so reads do not block
// concurrent appends. Returns fewer than n if the log is shorter.
func (g *GrokLog) Tail(n int) ([]GrokLogEntry, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(g.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("grok log: open for read: %w", err)
	}
	defer f.Close()

	// Ring buffer over the file lines. Cheap for n <= a few hundred.
	ring := make([]GrokLogEntry, 0, n)
	scanner := bufio.NewScanner(f)
	// Allow generous lines — completion notes can carry kilobytes of
	// worker output. 1 MiB ceiling is plenty for our envelope.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var e GrokLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			// Skip malformed lines rather than abort — log file may
			// have been hand-edited or partially-written.
			continue
		}
		if len(ring) < n {
			ring = append(ring, e)
		} else {
			copy(ring, ring[1:])
			ring[n-1] = e
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("grok log: scan: %w", err)
	}
	return ring, nil
}
