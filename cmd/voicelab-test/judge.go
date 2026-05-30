// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// judgeVerdict is the strict-JSON shape the judge prompt asks for.
type judgeVerdict struct {
	OK    bool   `json:"ok"`
	Notes string `json:"notes"`
}

// judge spawns a one-shot `claude -p` to grade a single round-trip.
// Returns the verdict + raw response (for debugging). Deliberately
// strict: malformed JSON is treated as a non-fatal "unknown" rather
// than crashing the whole suite — one weird judge response shouldn't
// cancel a 20-case run.
func judge(claudeBin string, rubric, utterance, userTranscript, grokResponse string) (judgeVerdict, string, error) {
	prompt := fmt.Sprintf(`You are evaluating a voice-AI test result. Apply the rubric strictly and respond with one line of JSON: {"ok": true|false, "notes": "<one short sentence>"}. No prose outside the JSON, no markdown fences.

Rubric:
%s

User said (intended utterance): %q
Voice-AI heard (its transcription of the audio): %q
Voice-AI responded (its spoken reply, transcribed): %q`,
		rubric, utterance, userTranscript, grokResponse,
	)
	cmd := exec.Command(claudeBin, "-p", prompt)
	out, err := cmd.Output()
	if err != nil {
		return judgeVerdict{}, "", fmt.Errorf("claude -p failed: %w", err)
	}
	raw := strings.TrimSpace(string(out))

	// Tolerate a model that drifts into markdown fences despite the
	// instruction. Strip a leading ```json / ``` and trailing ```.
	cleaned := raw
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var v judgeVerdict
	if err := json.Unmarshal([]byte(cleaned), &v); err != nil {
		return judgeVerdict{Notes: "judge produced non-JSON output"}, raw, nil
	}
	return v, raw, nil
}
