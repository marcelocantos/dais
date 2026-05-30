// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package voicelab is the core voice-loop driver shared by the
// `voicelab` and `voicelab-test` binaries. Production hosts (the CLI)
// plug in a malgo-backed AudioSource + AudioSink for live mic and
// speaker. The test harness plugs in BufferSource (synthetic TTS PCM
// from `say`) + BufferSink (records audio Grok emits, for downstream
// metric extraction and LLM judging).
//
// PCM contract on both interfaces: little-endian signed 16-bit mono at
// 24 kHz. This matches Grok Realtime's wire format so the loop never
// resamples either direction.
package voicelab

import (
	"context"
	"fmt"

	"github.com/marcelocantos/claudia/grok"
)

// SampleRate is the PCM sample rate, in Hz, on both Source and Sink.
// Matches Grok Realtime's configured input/output format.
const SampleRate = 24000

// BytesPerSample is 2 (signed 16-bit, little-endian, mono).
const BytesPerSample = 2

// AudioSource emits PCM16 mono 24 kHz frames on the returned channel.
// The channel is closed when input is exhausted. Implementations must
// not produce nil or zero-length frames.
type AudioSource interface {
	Frames() <-chan []byte
	Close() error
}

// AudioSink receives PCM16 mono 24 kHz frames for playback or capture.
// Write must be safe to call from arbitrary goroutines; the typical
// caller is grok.Client's readLoop dispatching OnAudio.
type AudioSink interface {
	Write(pcm []byte) error
	Close() error
}

// Loop drives a single Grok Realtime session, pumping audio from Source
// to Grok and from Grok to Sink. Callbacks in Config compose with the
// loop's own: any caller-supplied OnAudio is invoked *after* the sink
// write (so the loop's audio plumbing is unaffected by caller code).
type Loop struct {
	APIKey       string
	Voice        string  // "" → grok default
	SystemPrompt string  // "" → grok default
	Source       AudioSource
	Sink         AudioSink

	// OnUserTranscript / OnTranscript / OnTranscriptDone / OnResponseDone
	// / OnError / OnSessionReady mirror grok.Config and let the host
	// observe the protocol without subclassing.
	OnUserTranscript func(text string)
	OnTranscript     func(text string)
	OnTranscriptDone func()
	OnResponseDone   func()
	OnError          func(err error)
	OnSessionReady   func()

	// ManualCommit disables server-side VAD. When true, Run() calls
	// client.CommitAndRespond() automatically the moment the source
	// drains, then waits for ctx cancel. Use this for the test harness
	// where utterance boundaries are known up front (no need for VAD).
	// Live hosts (mic input) want this false so server VAD detects
	// pauses naturally.
	ManualCommit bool
}

// Run connects to Grok, starts the pump, and blocks until the source
// closes its frame channel or ctx is cancelled. Errors from SendAudio
// terminate the loop and propagate.
func (l *Loop) Run(ctx context.Context) error {
	cfg := grok.Config{
		APIKey:       l.APIKey,
		Voice:        l.Voice,
		SystemPrompt: l.SystemPrompt,
		ManualCommit: l.ManualCommit,
		OnAudio: func(pcm []byte) {
			if err := l.Sink.Write(pcm); err != nil {
				if l.OnError != nil {
					l.OnError(fmt.Errorf("sink write: %w", err))
				}
			}
		},
		OnUserTranscript: l.OnUserTranscript,
		OnTranscript:     l.OnTranscript,
		OnTranscriptDone: l.OnTranscriptDone,
		OnResponseDone:   l.OnResponseDone,
		OnError:          l.OnError,
		OnSessionReady:   l.OnSessionReady,
	}

	client, err := grok.Connect(ctx, cfg)
	if err != nil {
		return fmt.Errorf("grok connect: %w", err)
	}
	defer client.Close()

	frames := l.Source.Frames()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case buf, ok := <-frames:
			if !ok {
				// Source drained. In ManualCommit mode the caller
				// promised there's no VAD to fire, so we explicitly
				// commit + request a response. Either way the
				// WebSocket must stay open until the host cancels ctx
				// (it does so after observing OnResponseDone or hitting
				// its own timeout) — closing here would race Grok's
				// reply.
				if l.ManualCommit {
					if err := client.CommitAndRespond(ctx); err != nil {
						return fmt.Errorf("commit and respond: %w", err)
					}
				}
				<-ctx.Done()
				return ctx.Err()
			}
			if err := client.SendAudio(ctx, buf); err != nil {
				return fmt.Errorf("send audio: %w", err)
			}
		}
	}
}
