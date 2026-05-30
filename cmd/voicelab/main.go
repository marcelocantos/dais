// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// voicelab is a throwaway desktop CLI for iterating on Grok Realtime
// voice interaction quality. The loop core lives in
// internal/voicelab — this main is just the malgo-backed live host:
// mic in, speaker out, transcripts on stdout.
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
	"syscall"

	"github.com/marcelocantos/jevons/internal/voicelab"
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

	dev, err := voicelab.NewMalgoDevice()
	if err != nil {
		fatal("audio device: %v", err)
	}
	defer dev.Close()

	loop := &voicelab.Loop{
		APIKey:       apiKey,
		Voice:        *voice,
		SystemPrompt: *systemPrompt,
		Source:       dev.Source(),
		Sink:         dev.Sink(),
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
			slog.Error("voicelab", "err", err)
		},
		OnSessionReady: func() {
			fmt.Fprintln(os.Stderr, "voicelab: session ready — start talking. Ctrl-C to quit.")
		},
	}

	if err := loop.Run(ctx); err != nil && ctx.Err() == nil {
		fatal("loop: %v", err)
	}
	fmt.Fprintln(os.Stderr, "\nvoicelab: shutting down")
}

// loadKeychainKey pulls a secret from the macOS Keychain using the same
// account/service convention jevonsd uses (`-a jevons -s <service>`).
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
