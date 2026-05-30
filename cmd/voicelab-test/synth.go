// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// synth invokes macOS `say` to produce a WAV file at 24 kHz PCM16
// mono, then strips the RIFF/WAVE header and returns the raw PCM body.
// The harness pre-renders all utterances at startup so test runs
// aren't bottlenecked on TTS latency.
//
// Why this format: it matches voicelab.SampleRate exactly so the
// harness path is free of resampling — what `say` produces is bit-
// identical to what Grok expects on the wire.
func synth(text, scratchDir string) ([]byte, error) {
	if err := os.MkdirAll(scratchDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir scratch: %w", err)
	}
	wavPath := filepath.Join(scratchDir, "utterance.wav")
	cmd := exec.Command("say",
		"--data-format=LEI16@24000",
		"--file-format=WAVE",
		"-o", wavPath,
		text,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("say failed: %w (output: %s)", err, string(out))
	}
	wav, err := os.ReadFile(wavPath)
	if err != nil {
		return nil, fmt.Errorf("read wav: %w", err)
	}
	return parseWAVData(wav)
}

// parseWAVData skips over the RIFF/WAVE/fmt chunks to find the "data"
// chunk and returns its payload. We accept any WAV that matches our
// expected format (PCM16 mono 24 kHz) and reject the rest loudly —
// silent format mismatches would corrupt downstream audio.
func parseWAVData(wav []byte) ([]byte, error) {
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a RIFF/WAVE file")
	}
	pos := 12
	var (
		fmtSeen   bool
		channels  uint16
		rate      uint32
		bitsPer   uint16
	)
	for pos+8 <= len(wav) {
		chunkID := string(wav[pos : pos+4])
		chunkSize := binary.LittleEndian.Uint32(wav[pos+4 : pos+8])
		body := pos + 8
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, fmt.Errorf("fmt chunk too short: %d", chunkSize)
			}
			format := binary.LittleEndian.Uint16(wav[body : body+2])
			if format != 1 { // 1 = PCM
				return nil, fmt.Errorf("wav format %d, expected 1 (PCM)", format)
			}
			channels = binary.LittleEndian.Uint16(wav[body+2 : body+4])
			rate = binary.LittleEndian.Uint32(wav[body+4 : body+8])
			bitsPer = binary.LittleEndian.Uint16(wav[body+14 : body+16])
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return nil, fmt.Errorf("data chunk before fmt")
			}
			if channels != 1 || rate != 24000 || bitsPer != 16 {
				return nil, fmt.Errorf("wav format %d ch %d Hz %d-bit, expected mono/24000/16", channels, rate, bitsPer)
			}
			end := min(body+int(chunkSize), len(wav))
			return wav[body:end], nil
		}
		// Chunks are word-aligned: skip an extra byte on odd sizes.
		next := body + int(chunkSize)
		if chunkSize%2 == 1 {
			next++
		}
		pos = next
	}
	return nil, fmt.Errorf("no data chunk found")
}

// silencePCM returns durationMs of zero-padded PCM16 mono 24 kHz, used
// as the post-utterance silence tail that triggers Grok's server-side
// VAD commit. 1200 ms is comfortably past the 800 ms default
// silence_duration_ms; tweak if it ever wedges.
func silencePCM(durationMs int) []byte {
	samples := durationMs * 24000 / 1000
	return make([]byte, samples*2)
}
