// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package main

import "time"

// Case is one test scenario: a synthetic utterance fed into the voice
// loop with expectations on the response.
//
// Exactly one of ExpectAny or JudgeRubric should be set. ExpectAny is
// a cheap deterministic check ("did the response contain '4' or
// 'four'?") for cases with a known answer; JudgeRubric defers to
// claude -p for cases where the answer space is open ("did this
// reply make sense as a response to '<utterance>'?").
type Case struct {
	Name       string
	Utterance  string
	MaxLatency time.Duration

	// Deterministic check: pass if any of these substrings appears
	// (case-insensitive) anywhere in the response transcript.
	ExpectAny []string

	// LLM-judged check: prompt fragment appended to the judge rubric.
	// Phrased as a yes/no question about the response.
	JudgeRubric string
}

// Cases is the seed suite. Keep utterances short and unambiguous —
// the goal is to exercise the loop, not stress Grok's reasoning.
var Cases = []Case{
	{
		Name:       "arithmetic",
		Utterance:  "What is two plus two?",
		MaxLatency: 3 * time.Second,
		ExpectAny:  []string{"4", "four"},
	},
	{
		Name:       "greeting",
		Utterance:  "Hello, how are you today?",
		MaxLatency: 3 * time.Second,
		JudgeRubric: "Pass if the reply acknowledges the greeting in a natural conversational way (any tone, any length). Fail if it ignores the greeting or responds with a non-sequitur.",
	},
	{
		Name:       "short_fact",
		Utterance:  "Tell me one short fact about Mars.",
		MaxLatency: 4 * time.Second,
		JudgeRubric: "Pass if the reply states a factual claim about Mars (the planet). Fail if it refuses, asks a clarifying question, or talks about something other than Mars.",
	},
	{
		Name:       "follow_up_intent",
		Utterance:  "Can you set a timer for five minutes?",
		MaxLatency: 4 * time.Second,
		JudgeRubric: "Pass if the reply either (a) confirms setting the timer, or (b) clearly explains that it cannot set timers and offers something useful. Fail if the response is confused or off-topic.",
	},
}
