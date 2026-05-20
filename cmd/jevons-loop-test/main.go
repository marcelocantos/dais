// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// jevons-loop-test exercises jevonsd's /ws/voice endpoint with
// synthesised PCM audio. It replaces the browser entirely while
// keeping the server-side voice bridge intact. If multi-turn
// transcription holds up here but fails from a real browser, the
// bug is in the browser audio pipeline (mic capture, downsampling,
// AudioContext state). If it fails here too, the bug is in jevonsd.
//
// Protocol: connects to ws://<host>/ws/voice, waits for status=ready,
// then for each turn:
//
//   1. Streams a `say`-synthesised utterance as binary PCM frames
//      (2048 bytes per frame, paced at the natural audio rate).
//   2. Sends {"type":"commit"} — the same end-of-utterance signal
//      the browser sends on Ctrl-release.
//   3. Watches for the {user_transcript, assistant_transcript_done}
//      sequence and times out if it doesn't arrive.
//
// Run with: ./bin/jevonsd up, then ./jevons-loop-test -n 5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	sampleRate = 24000 // jevonsd & xAI expect 24 kHz PCM16 mono
	frameBytes = 2048  // matches the browser pipeline's chunk size
	frameMS    = 1000 * frameBytes / 2 / sampleRate
)

func main() {
	host := flag.String("host", "localhost:13705", "jevonsd address (host:port)")
	turns := flag.Int("n", 5, "number of turns to attempt")
	text := flag.String("text", "Testing jevons turn number", "phrase prefix (turn number appended)")
	verbose := flag.Bool("v", false, "dump every WS event payload")
	gap := flag.Duration("gap", 1*time.Second, "delay between turns")
	flag.Parse()

	slog.SetLogLoggerLevel(slog.LevelDebug)

	wsURL := (&url.URL{Scheme: "ws", Host: *host, Path: "/ws/voice"}).String()
	slog.Info("dialling jevonsd", "url", wsURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		slog.Error("dial failed", "err", err)
		os.Exit(1)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	mux := newEventMux(*verbose)
	go mux.run(ctx, conn)

	// Wait for the bridge to come up the chain and Grok to be ready.
	if _, ok := mux.waitStatus(ctx, "ready", 20*time.Second); !ok {
		slog.Error("never received status=ready from jevonsd")
		os.Exit(1)
	}
	slog.Info("jevonsd voice bridge ready")

	results := make([]turnResult, 0, *turns)
	for t := 1; t <= *turns; t++ {
		r := runTurn(ctx, conn, mux, t, fmt.Sprintf("%s %d", *text, t))
		results = append(results, r)
		if *gap > 0 && t < *turns {
			time.Sleep(*gap)
		}
	}

	printReport(results)
	for _, r := range results {
		if !r.passed {
			os.Exit(1)
		}
	}
}

type turnResult struct {
	turn         int
	transcript   string
	assistantTxt string
	gotUserTxt   bool
	gotAsstDone  bool
	passed       bool
}

func runTurn(ctx context.Context, conn *websocket.Conn, mux *eventMux, turn int, phrase string) turnResult {
	r := turnResult{turn: turn}
	slog.Info("turn start", "turn", turn, "phrase", phrase)

	audio, err := synthesise(phrase)
	if err != nil {
		slog.Error("synthesise failed", "turn", turn, "err", err)
		return r
	}
	slog.Info("audio synthesised", "turn", turn, "bytes", len(audio), "approx_ms", len(audio)/2*1000/sampleRate)

	// Drain stale events so we only catch ones produced for this turn.
	mux.drain()

	// Stream PCM as binary frames, paced like a real mic.
	for off := 0; off < len(audio); off += frameBytes {
		end := off + frameBytes
		if end > len(audio) {
			end = len(audio)
		}
		wctx, c := context.WithTimeout(ctx, 5*time.Second)
		err := conn.Write(wctx, websocket.MessageBinary, audio[off:end])
		c()
		if err != nil {
			slog.Error("audio write failed", "turn", turn, "err", err)
			return r
		}
		time.Sleep(time.Duration(frameMS) * time.Millisecond)
	}

	// End-of-utterance signal — the same JSON the browser sends on Ctrl release.
	if err := sendJSON(ctx, conn, map[string]any{"type": "commit"}); err != nil {
		slog.Error("commit failed", "turn", turn, "err", err)
		return r
	}

	// Watch for the structural events. 45s deadline covers Grok handshake
	// for the first turn and worker delegation latency.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok := mux.next(ctx, time.Until(deadline))
		if !ok {
			break
		}
		switch ev.Type {
		case "user_transcript":
			if t, _ := ev.Raw["text"].(string); t != "" {
				r.transcript = t
				r.gotUserTxt = true
			}
		case "assistant_transcript":
			if t, _ := ev.Raw["text"].(string); t != "" {
				r.assistantTxt += t
			}
		case "assistant_transcript_done":
			r.gotAsstDone = true
			r.passed = r.gotUserTxt
			slog.Info("turn complete", "turn", turn,
				"user_transcript", r.transcript,
				"assistant_chars", len(r.assistantTxt),
				"passed", r.passed)
			return r
		case "status":
			// status=ready arrives at session start AND after each response.
			// Treat post-response status=ready as a turn-end signal in case
			// the assistant didn't speak (no transcript_done).
			if s, _ := ev.Raw["status"].(string); s == "ready" && r.gotUserTxt && !r.gotAsstDone {
				r.passed = true
				slog.Info("turn complete (status=ready, no asst transcript)", "turn", turn,
					"user_transcript", r.transcript, "passed", r.passed)
				return r
			}
		case "error":
			slog.Error("server error event", "turn", turn, "data", ev.Raw)
		}
	}
	slog.Warn("turn timed out", "turn", turn,
		"user_transcript", r.transcript,
		"asst_done", r.gotAsstDone)
	return r
}

// synthesise produces raw 24kHz PCM16 mono bytes for `phrase` using
// macOS `say`. Identical to the xAI harness so we can compare runs.
func synthesise(phrase string) ([]byte, error) {
	if _, err := exec.LookPath("say"); err != nil {
		return nil, fmt.Errorf("`say` not available — macOS only: %w", err)
	}
	tmp, err := os.CreateTemp("", "jevons-loop-*.wav")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	out, err := exec.Command("say", "-o", tmp.Name(),
		"--data-format=LEI16@24000", phrase).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("say failed: %w (%s)", err, string(out))
	}
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	if len(data) < 44 {
		return nil, fmt.Errorf("wav too short: %d bytes", len(data))
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

// eventMux drains the jevonsd voice WS, distinguishing JSON events (the
// test cares about) from binary audio frames (Grok's spoken response —
// we discard them but acknowledge by reading).
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
		mt, data, err := conn.Read(ctx)
		if err != nil {
			slog.Info("read loop ended", "err", err)
			m.mu.Lock()
			m.cond.Broadcast()
			m.mu.Unlock()
			return
		}
		if mt == websocket.MessageBinary {
			// Grok audio response — drain and discard.
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("json unmarshal failed", "err", err, "data", string(data))
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
		for _, ch := range m.waiters[t] {
			select {
			case ch <- ev:
			default:
			}
		}
		delete(m.waiters, t)
		m.queue = append(m.queue, ev)
		m.cond.Broadcast()
		m.mu.Unlock()
	}
}

// waitStatus blocks until a status event with the given status value
// arrives, or the deadline expires.
func (m *eventMux) waitStatus(ctx context.Context, want string, dur time.Duration) (event, bool) {
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		ev, ok := m.next(ctx, time.Until(deadline))
		if !ok {
			return event{}, false
		}
		if ev.Type == "status" {
			if s, _ := ev.Raw["status"].(string); s == want {
				return ev, true
			}
		}
	}
	return event{}, false
}

func (m *eventMux) drain() {
	m.mu.Lock()
	m.queue = nil
	m.mu.Unlock()
}

func (m *eventMux) next(ctx context.Context, dur time.Duration) (event, bool) {
	deadline := time.Now().Add(dur)
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.queue) == 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return event{}, false
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(remaining):
				m.mu.Lock()
				m.cond.Broadcast()
				m.mu.Unlock()
			case <-done:
			}
		}()
		m.cond.Wait()
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

func printReport(results []turnResult) {
	fmt.Println()
	fmt.Println("=== jevons-loop-test report ===")
	fmt.Printf("%-6s %-9s %-9s %-7s %s\n", "turn", "user_txt", "asst_done", "passed", "transcript")
	for _, r := range results {
		fmt.Printf("%-6d %-9v %-9v %-7v %q\n",
			r.turn, r.gotUserTxt, r.gotAsstDone, r.passed, r.transcript)
	}
	fmt.Println()
}
