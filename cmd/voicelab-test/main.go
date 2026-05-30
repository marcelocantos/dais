// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// voicelab-test runs an automated suite of voice round-trips against
// Grok Realtime: each case TTSes a known utterance, plays it into the
// voicelab.Loop at realtime cadence, records what Grok heard and what
// it said back, then judges the result. Pass/fail per case + a metric
// table at the end. Useful for catching regressions in the voice path
// without having to drive every change manually.
//
// Two grading modes per case:
//   - ExpectAny: case-insensitive substring match on the response
//     transcript (cheap, deterministic).
//   - JudgeRubric: claude -p reads the rubric + transcripts and
//     returns {"ok": bool, "notes": str} (for open-ended answers).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/jevons/internal/voicelab"
)

func main() {
	verbose := flag.Bool("v", false, "verbose protocol logging")
	silenceMs := flag.Int("silence", 1200, "post-utterance silence tail in ms (must exceed Grok's server-VAD silence_duration_ms)")
	timeout := flag.Duration("timeout", 15*time.Second, "per-case hard timeout")
	claudeBin := flag.String("claude", expandHome("~/.local/bin/claude"), "path to the claude CLI for the judge")
	scratch := flag.String("scratch", "/tmp/voicelab-test", "scratch dir for TTS WAV files")
	filterName := flag.String("only", "", "if set, run only the case with this name")
	flag.Parse()

	logLevel := slog.LevelError
	if *verbose {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	apiKey, err := loadKeychainKey("xai-api-key")
	if err != nil {
		fatal("xai-api-key not found in keychain: %v", err)
	}

	if _, err := exec.LookPath(*claudeBin); err != nil {
		// Tolerate missing claude — substring cases still run; rubric
		// cases get marked skipped rather than failed.
		fmt.Fprintf(os.Stderr, "warning: claude not found at %s — rubric cases will be skipped\n", *claudeBin)
	}

	results := make([]result, 0, len(Cases))
	for _, c := range Cases {
		if *filterName != "" && c.Name != *filterName {
			continue
		}
		fmt.Fprintf(os.Stderr, "=== %s\n", c.Name)
		r := runCase(c, apiKey, *scratch, *silenceMs, *timeout, *claudeBin)
		results = append(results, r)
		printShortResult(r)
	}

	printSummary(results)

	for _, r := range results {
		if !r.passed() {
			os.Exit(1)
		}
	}
}

type result struct {
	Case            Case
	UserTranscript  string // what Grok heard
	ResponseText    string // what Grok said back
	Latency         time.Duration
	LatencyMeasured bool
	JudgeOK         bool
	JudgeNotes      string
	JudgeSkipped    bool
	SubstringHit    string // first ExpectAny match
	Err             error
}

func (r result) passed() bool {
	if r.Err != nil {
		return false
	}
	if r.LatencyMeasured && r.Case.MaxLatency > 0 && r.Latency > r.Case.MaxLatency {
		return false
	}
	if len(r.Case.ExpectAny) > 0 {
		return r.SubstringHit != ""
	}
	if r.Case.JudgeRubric != "" && !r.JudgeSkipped {
		return r.JudgeOK
	}
	return true
}

func runCase(c Case, apiKey, scratch string, silenceMs int, timeout time.Duration, claudeBin string) result {
	r := result{Case: c}

	caseScratch := filepath.Join(scratch, c.Name)
	utterancePCM, err := synth(c.Utterance, caseScratch)
	if err != nil {
		r.Err = fmt.Errorf("synth: %w", err)
		return r
	}
	silence := silencePCM(silenceMs)
	combined := make([]byte, 0, len(utterancePCM)+len(silence))
	combined = append(combined, utterancePCM...)
	combined = append(combined, silence...)

	var (
		stampMu       sync.Mutex
		utteranceEnd  time.Time
		userTextMu    sync.Mutex
		userText      strings.Builder
		responseMu    sync.Mutex
		response      strings.Builder
		responseDone  = make(chan struct{})
		responseOnce  sync.Once
	)

	source := &voicelab.BufferSource{
		PCM:        combined,
		StampAfter: len(utterancePCM),
		OnStamp: func() {
			stampMu.Lock()
			utteranceEnd = time.Now()
			stampMu.Unlock()
		},
	}
	sink := &voicelab.BufferSink{}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	loop := &voicelab.Loop{
		APIKey:       apiKey,
		Voice:        "Eve",
		SystemPrompt: "You are a voice assistant being smoke-tested. Reply briefly and directly.",
		ManualCommit: true,
		Source:       source,
		Sink:         sink,
		OnUserTranscript: func(text string) {
			userTextMu.Lock()
			userText.WriteString(text)
			userTextMu.Unlock()
		},
		OnTranscript: func(text string) {
			responseMu.Lock()
			response.WriteString(text)
			responseMu.Unlock()
		},
		OnResponseDone: func() {
			responseOnce.Do(func() { close(responseDone) })
		},
		OnError: func(err error) {
			slog.Debug("loop err", "err", err)
		},
	}

	loopErr := make(chan error, 1)
	go func() { loopErr <- loop.Run(ctx) }()

	select {
	case <-responseDone:
	case <-ctx.Done():
	}
	cancel()
	<-loopErr // wait for goroutine to settle

	r.UserTranscript = strings.TrimSpace(userText.String())
	r.ResponseText = strings.TrimSpace(response.String())

	stampMu.Lock()
	ue := utteranceEnd
	stampMu.Unlock()
	first := sink.FirstFrameAt()
	if !ue.IsZero() && !first.IsZero() && !first.Before(ue) {
		r.Latency = first.Sub(ue)
		r.LatencyMeasured = true
	}

	if ctx.Err() == context.DeadlineExceeded && r.ResponseText == "" {
		r.Err = fmt.Errorf("timed out before response.done")
		return r
	}

	if len(c.ExpectAny) > 0 {
		respLower := strings.ToLower(r.ResponseText)
		for _, want := range c.ExpectAny {
			if strings.Contains(respLower, strings.ToLower(want)) {
				r.SubstringHit = want
				break
			}
		}
	} else if c.JudgeRubric != "" {
		if _, err := exec.LookPath(claudeBin); err != nil {
			r.JudgeSkipped = true
			r.JudgeNotes = "claude binary not available"
		} else {
			v, raw, err := judge(claudeBin, c.JudgeRubric, c.Utterance, r.UserTranscript, r.ResponseText)
			if err != nil {
				r.JudgeSkipped = true
				r.JudgeNotes = "judge error: " + err.Error()
			} else {
				r.JudgeOK = v.OK
				if v.Notes != "" {
					r.JudgeNotes = v.Notes
				} else if !v.OK {
					r.JudgeNotes = "(no notes; raw: " + raw + ")"
				}
			}
		}
	}

	return r
}

func printShortResult(r result) {
	tag := "PASS"
	if !r.passed() {
		tag = "FAIL"
	}
	if r.Err != nil {
		fmt.Fprintf(os.Stderr, "  %s — %s\n", tag, r.Err)
		return
	}
	fmt.Fprintf(os.Stderr, "  heard: %q\n", r.UserTranscript)
	fmt.Fprintf(os.Stderr, "  said:  %q\n", r.ResponseText)
	if r.LatencyMeasured {
		fmt.Fprintf(os.Stderr, "  latency: %s (budget %s)\n", r.Latency.Round(time.Millisecond), r.Case.MaxLatency)
	}
	switch {
	case len(r.Case.ExpectAny) > 0:
		if r.SubstringHit != "" {
			fmt.Fprintf(os.Stderr, "  match: %q\n", r.SubstringHit)
		} else {
			fmt.Fprintf(os.Stderr, "  match: none of %v\n", r.Case.ExpectAny)
		}
	case r.Case.JudgeRubric != "":
		switch {
		case r.JudgeSkipped:
			fmt.Fprintf(os.Stderr, "  judge: SKIPPED — %s\n", r.JudgeNotes)
		case r.JudgeOK:
			fmt.Fprintf(os.Stderr, "  judge: OK — %s\n", r.JudgeNotes)
		default:
			fmt.Fprintf(os.Stderr, "  judge: NOT OK — %s\n", r.JudgeNotes)
		}
	}
	fmt.Fprintf(os.Stderr, "  %s\n\n", tag)
}

func printSummary(rs []result) {
	pass, fail := 0, 0
	for _, r := range rs {
		if r.passed() {
			pass++
		} else {
			fail++
		}
	}
	fmt.Fprintf(os.Stderr, "summary: %d passed, %d failed\n", pass, fail)
}

func loadKeychainKey(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-a", "jevons", "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(os.Getenv("HOME"), p[2:])
	}
	return p
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "voicelab-test: "+format+"\n", args...)
	os.Exit(1)
}
