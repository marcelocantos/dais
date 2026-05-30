// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// voicelab is a throwaway desktop CLI for iterating on Grok Realtime
// voice interaction quality. The whole point is a single process: mic
// in, Grok WebSocket, speaker out, transcripts on stdout. No web UI,
// no JS bridge, no transport layer. When the conversation feels right
// here, the algorithms (Grok protocol handling, FSM, VAD) port to
// whatever client we end up shipping (native iOS, WKWebView, both).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/gen2brain/malgo"
	"github.com/marcelocantos/claudia/grok"
)

const (
	// Grok Realtime expects (and emits) 24 kHz PCM16 mono. Configuring
	// the audio device to match the wire format keeps the data path
	// boring — no resampling either side.
	sampleRate = 24000
	channels   = 1
)

func main() {
	systemPrompt := flag.String("system", "You are jevons, a voice-first assistant. Keep replies brief and conversational.", "system prompt sent to Grok")
	voice := flag.String("voice", "Eve", "Grok TTS voice")
	verbose := flag.Bool("v", false, "verbose protocol logging")
	flag.Parse()

	logLevel := slog.LevelWarn
	if *verbose {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	apiKey, err := loadKeychainKey("xai-api-key")
	if err != nil {
		fatal("xai-api-key not found in keychain (expected `security add-generic-password -a jevons -s xai-api-key -w <key>`): %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Playback jitter buffer. Grok emits PCM in chunks aligned to
	// response framing, not to our device's frame size, so we accumulate
	// and let the audio callback drain as much as it can each tick.
	var pbMu sync.Mutex
	pbBuf := make([]byte, 0, sampleRate*2*2) // ~2 s headroom

	client, err := grok.Connect(ctx, grok.Config{
		APIKey:       apiKey,
		Voice:        *voice,
		SystemPrompt: *systemPrompt,
		OnAudio: func(pcm []byte) {
			pbMu.Lock()
			pbBuf = append(pbBuf, pcm...)
			pbMu.Unlock()
		},
		OnUserTranscript: func(text string) {
			fmt.Printf("\n> %s\n", strings.TrimSpace(text))
		},
		OnTranscript: func(text string) {
			fmt.Print(text)
		},
		OnTranscriptDone: func() {
			fmt.Println()
		},
		OnError: func(err error) {
			slog.Error("grok", "err", err)
		},
		OnSessionReady: func() {
			fmt.Fprintln(os.Stderr, "voicelab: session ready — start talking. Ctrl-C to quit.")
		},
	})
	if err != nil {
		fatal("grok connect: %v", err)
	}
	defer client.Close()

	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(msg string) {
		slog.Debug("malgo", "msg", msg)
	})
	if err != nil {
		fatal("malgo init: %v", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	deviceCfg := malgo.DefaultDeviceConfig(malgo.Duplex)
	deviceCfg.SampleRate = sampleRate
	deviceCfg.Capture.Format = malgo.FormatS16
	deviceCfg.Capture.Channels = channels
	deviceCfg.Playback.Format = malgo.FormatS16
	deviceCfg.Playback.Channels = channels
	deviceCfg.Alsa.NoMMap = 1

	// Capture frames go through a buffered channel so the audio thread
	// (which runs the device callback) never blocks on the Grok
	// WebSocket write. A goroutine drains the channel into SendAudio.
	captureCh := make(chan []byte, 64)

	callbacks := malgo.DeviceCallbacks{
		Data: func(out, in []byte, frameCount uint32) {
			// Capture: ship a copy to the channel. The malgo buffer is
			// reused across callbacks, so we must copy before handoff.
			if len(in) > 0 {
				buf := make([]byte, len(in))
				copy(buf, in)
				select {
				case captureCh <- buf:
				default:
					// Channel full — drop. Better than blocking the
					// audio thread; surfaces as a momentary mic gap.
					slog.Debug("voicelab: capture channel full, dropping frame")
				}
			}
			// Playback: drain jitter buffer into out, pad with silence.
			pbMu.Lock()
			n := copy(out, pbBuf)
			pbBuf = pbBuf[n:]
			pbMu.Unlock()
			for i := n; i < len(out); i++ {
				out[i] = 0
			}
		},
	}

	device, err := malgo.InitDevice(mctx.Context, deviceCfg, callbacks)
	if err != nil {
		fatal("malgo device init: %v", err)
	}
	defer device.Uninit()

	if err := device.Start(); err != nil {
		fatal("malgo device start: %v", err)
	}

	// Pump captured frames to Grok. Exits when the channel is closed
	// (on shutdown) or SendAudio errors out (connection lost).
	go func() {
		for buf := range captureCh {
			if err := client.SendAudio(ctx, buf); err != nil {
				if ctx.Err() == nil {
					slog.Error("voicelab: send audio failed", "err", err)
				}
				return
			}
		}
	}()

	<-ctx.Done()
	close(captureCh)
	fmt.Fprintln(os.Stderr, "\nvoicelab: shutting down")
}

// loadKeychainKey pulls a secret from the macOS Keychain using the same
// account/service convention jevonsd uses (`-a jevons -s <service>`),
// so existing keys work without extra setup.
func loadKeychainKey(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-a", "jevons", "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "voicelab: "+format+"\n", args...)
	os.Exit(1)
}
