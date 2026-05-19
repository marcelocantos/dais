// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// xai-loop-test reproduces the multi-turn PTT lockup we hit in jevonsd's
// voice bridge against the xAI Grok Realtime API, with the application
// layer (browser, jevonsd, claudia.Agent) entirely out of the picture.
//
// For each of N turns it: streams a canned PCM16/24kHz/mono utterance
// (synthesised via macOS `say` so the harness is reproducible without
// recorded fixtures), sends input_audio_buffer.commit + response.create,
// waits for response.done, and logs every event received from xAI. The
// final report shows PASS/FAIL per turn so we can see exactly which
// turn drops its item creation.
//
// Run with: XAI_API_KEY=... go run ./cmd/xai-loop-test -n 5
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	xaiURL      = "wss://api.x.ai/v1/realtime"
	sampleRate  = 24000          // xAI Realtime input format
	frameBytes  = 2048           // matches jevonsd's voice bridge chunking
	frameMS     = 1000 * frameBytes / 2 / sampleRate // PCM16 mono → bytes/sec = 2 * sampleRate
)

func main() {
	turns := flag.Int("n", 5, "number of turns to attempt")
	text := flag.String("text", "Testing turn number", "phrase prefix sent each turn (turn number appended)")
	verbose := flag.Bool("v", false, "log every WS event payload, not just structural milestones")
	clearBetween := flag.Bool("clear-between-turns", false, "send input_audio_buffer.clear after each response.done")
	flag.Parse()

	apiKey := loadAPIKey()
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "xAI API key not found — set XAI_API_KEY env var or store in Keychain as 'xai-api-key'")
		os.Exit(2)
	}

	slog.SetLogLoggerLevel(slog.LevelDebug)

	results := runHarness(*turns, *text, *verbose, *clearBetween, apiKey)
	printReport(results)
	for _, r := range results {
		if !r.passed {
			os.Exit(1)
		}
	}
}

type turnResult struct {
	turn         int
	committed    bool
	transcript   string
	gotItemAdded bool
	gotResponse  bool
	gotDone      bool
	passed       bool
}

func runHarness(turns int, baseText string, verbose, clearBetween bool, apiKey string) []turnResult {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	slog.Info("dialling xAI Realtime", "url", xaiURL)
	conn, _, err := websocket.Dial(ctx, xaiURL, &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Authorization": {"Bearer " + apiKey},
		},
	})
	if err != nil {
		slog.Error("dial failed", "err", err)
		return nil
	}
	defer conn.CloseNow()

	conn.SetReadLimit(4 << 20)

	// Same session shape as jevonsd's bridge: manual commit, 24 kHz PCM.
	session := map[string]any{
		"voice": "leo",
		"audio": map[string]any{
			"input":  map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": sampleRate}},
			"output": map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": sampleRate}},
		},
		"instructions":   "You are a test fixture. Always reply with exactly the word OK.",
		"turn_detection": nil,
	}
	if err := sendJSON(ctx, conn, map[string]any{"type": "session.update", "session": session}); err != nil {
		slog.Error("session.update failed", "err", err)
		return nil
	}

	// Event multiplexer. Tests subscribe by type; readLoop unblocks them.
	mux := newEventMux(verbose)
	go mux.run(ctx, conn)
	// Wait for session.updated before sending audio so xAI processes our
	// turn_detection: null setting first.
	if _, ok := mux.wait(ctx, "session.updated", 10*time.Second); !ok {
		slog.Error("session.updated never arrived")
		return nil
	}
	slog.Info("session.updated received — ready")

	results := make([]turnResult, 0, turns)
	for t := 1; t <= turns; t++ {
		r := runTurn(ctx, conn, mux, t, fmt.Sprintf("%s %d", baseText, t))
		results = append(results, r)
		if clearBetween {
			_ = sendJSON(ctx, conn, map[string]any{"type": "input_audio_buffer.clear"})
		}
	}
	return results
}

func runTurn(ctx context.Context, conn *websocket.Conn, mux *eventMux, turn int, phrase string) turnResult {
	r := turnResult{turn: turn}
	slog.Info("turn start", "turn", turn, "phrase", phrase)

	audio, err := synthesise(phrase)
	if err != nil {
		slog.Error("synthesise failed", "err", err)
		return r
	}
	slog.Info("audio synthesised", "turn", turn, "bytes", len(audio), "approx_ms", len(audio)/2*1000/sampleRate)

	// Stream the audio in real-time-ish chunks. The exact timing
	// probably doesn't matter for xAI, but it matches what jevonsd does.
	for off := 0; off < len(audio); off += frameBytes {
		end := off + frameBytes
		if end > len(audio) {
			end = len(audio)
		}
		chunk := audio[off:end]
		if err := sendJSON(ctx, conn, map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(chunk),
		}); err != nil {
			slog.Error("append failed", "turn", turn, "err", err)
			return r
		}
		time.Sleep(time.Duration(frameMS) * time.Millisecond)
	}

	// Commit + request response. Same sequence jevonsd's CommitAndRespond uses.
	if err := sendJSON(ctx, conn, map[string]any{"type": "input_audio_buffer.commit"}); err != nil {
		slog.Error("commit failed", "turn", turn, "err", err)
		return r
	}
	if err := sendJSON(ctx, conn, map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"modalities": []string{"text", "audio"},
		},
	}); err != nil {
		slog.Error("response.create failed", "turn", turn, "err", err)
		return r
	}

	// Watch for the expected sequence with a 30-second deadline per turn.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok := mux.next(ctx, deadline.Sub(time.Now()))
		if !ok {
			break
		}
		switch ev.Type {
		case "input_audio_buffer.committed":
			r.committed = true
		case "conversation.item.input_audio_transcription.completed":
			if t, ok := ev.Raw["transcript"].(string); ok {
				r.transcript = t
			}
		case "conversation.item.added":
			r.gotItemAdded = true
		case "response.created":
			r.gotResponse = true
		case "response.done":
			r.gotDone = true
			r.passed = r.committed && r.gotItemAdded && r.gotResponse
			slog.Info("turn complete", "turn", turn,
				"committed", r.committed,
				"transcript", r.transcript,
				"item", r.gotItemAdded,
				"response", r.gotResponse,
				"done", r.gotDone,
				"passed", r.passed)
			return r
		case "error":
			if e, _ := ev.Raw["error"].(map[string]any); e != nil {
				slog.Error("xAI error", "turn", turn, "err", e["message"])
			}
		}
	}
	slog.Warn("turn timed out", "turn", turn,
		"committed", r.committed,
		"transcript", r.transcript,
		"item", r.gotItemAdded,
		"response", r.gotResponse,
		"done", r.gotDone)
	return r
}

// synthesise produces 24kHz/PCM16/mono audio bytes for the given phrase
// using macOS `say`. WAV header is stripped (44 bytes for the standard
// LEI16 format `say` emits).
func synthesise(phrase string) ([]byte, error) {
	if _, err := exec.LookPath("say"); err != nil {
		return nil, fmt.Errorf("`say` not available — only macOS supported in this harness: %w", err)
	}
	tmp, err := os.CreateTemp("", "xai-loop-*.wav")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	cmd := exec.Command("say", "-o", tmp.Name(),
		"--data-format=LEI16@24000",
		phrase)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say failed: %w (%s)", err, string(out))
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	// Skip 44-byte WAV header.
	if len(data) < 44 {
		return nil, fmt.Errorf("wav file too short: %d bytes (%s)", len(data), filepath.Base(tmp.Name()))
	}
	return data[44:], nil
}

func sendJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// eventMux routes incoming events from xAI to (a) all-events queue for
// the runner to drain, and (b) blocking waiters keyed by event type.
type event struct {
	Type string
	Raw  map[string]any
}

type eventMux struct {
	verbose bool

	mu      sync.Mutex
	queue   []event
	cond    *sync.Cond
	waiters map[string][]chan event
}

func newEventMux(verbose bool) *eventMux {
	m := &eventMux{verbose: verbose, waiters: make(map[string][]chan event)}
	m.cond = sync.NewCond(&m.mu)
	return m
}

func (m *eventMux) run(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			slog.Error("read loop ended", "err", err)
			m.mu.Lock()
			m.cond.Broadcast()
			m.mu.Unlock()
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("unmarshal failed", "err", err)
			continue
		}
		t, _ := msg["type"].(string)
		if m.verbose {
			slog.Info("recv", "type", t, "raw", string(data))
		} else {
			slog.Debug("recv", "type", t)
		}
		ev := event{Type: t, Raw: msg}
		m.mu.Lock()
		// Wake any type-specific waiters.
		for _, ch := range m.waiters[t] {
			select {
			case ch <- ev:
			default:
			}
		}
		delete(m.waiters, t)
		// Enqueue for the runner.
		m.queue = append(m.queue, ev)
		m.cond.Broadcast()
		m.mu.Unlock()
	}
}

// wait blocks for the first event of a given type, or until the deadline.
func (m *eventMux) wait(ctx context.Context, t string, dur time.Duration) (event, bool) {
	m.mu.Lock()
	ch := make(chan event, 1)
	m.waiters[t] = append(m.waiters[t], ch)
	// Also scan the queue in case the event already landed.
	for _, q := range m.queue {
		if q.Type == t {
			m.mu.Unlock()
			return q, true
		}
	}
	m.mu.Unlock()
	select {
	case ev := <-ch:
		return ev, true
	case <-time.After(dur):
		return event{}, false
	case <-ctx.Done():
		return event{}, false
	}
}

// next returns the next queued event, blocking up to dur.
func (m *eventMux) next(ctx context.Context, dur time.Duration) (event, bool) {
	deadline := time.Now().Add(dur)
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.queue) == 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return event{}, false
		}
		// Use a timed wait via goroutine since sync.Cond has no timed Wait.
		done := make(chan struct{})
		var fired bool
		go func() {
			select {
			case <-time.After(remaining):
				m.mu.Lock()
				if !fired {
					m.cond.Broadcast()
				}
				m.mu.Unlock()
			case <-done:
			}
		}()
		m.cond.Wait()
		fired = true
		close(done)
		select {
		case <-ctx.Done():
			return event{}, false
		default:
		}
	}
	ev := m.queue[0]
	m.queue = m.queue[1:]
	return ev, true
}

// loadAPIKey tries macOS Keychain first (matching jevonsd), then env.
func loadAPIKey() string {
	if out, err := exec.Command("security", "find-generic-password",
		"-s", "xai-api-key", "-w").Output(); err == nil {
		k := string(out)
		// Strip trailing newline.
		for len(k) > 0 && (k[len(k)-1] == '\n' || k[len(k)-1] == '\r') {
			k = k[:len(k)-1]
		}
		if k != "" {
			return k
		}
	}
	return os.Getenv("XAI_API_KEY")
}

func printReport(results []turnResult) {
	fmt.Println()
	fmt.Println("=== xai-loop-test report ===")
	fmt.Printf("%-6s %-9s %-9s %-9s %-7s %s\n",
		"turn", "committed", "item", "response", "passed", "transcript")
	for _, r := range results {
		fmt.Printf("%-6d %-9v %-9v %-9v %-7v %q\n",
			r.turn, r.committed, r.gotItemAdded, r.gotResponse, r.passed, r.transcript)
	}
	fmt.Println()
}
