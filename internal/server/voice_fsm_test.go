// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"errors"
	"testing"

	"github.com/marcelocantos/claudia/grok"
)

// recordingDeps captures every side-effect call so tests can assert
// on them. It implements voiceDeps.
type recordingDeps struct {
	audioSent       [][]byte
	commits         int
	clears          int
	responseReqs    []grok.ResponseModalities
	systemNotes     []string
	browserMsgs     []any
	loggedUser      []string
	loggedAssistant []string
	loggedSystem    []string

	// failNext: if non-nil, returned by the next dep call and then cleared.
	failNext error
}

func (r *recordingDeps) maybeFail() error {
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	return nil
}

func (r *recordingDeps) SendAudioToGrok(pcm []byte) error {
	if err := r.maybeFail(); err != nil {
		return err
	}
	r.audioSent = append(r.audioSent, append([]byte(nil), pcm...))
	return nil
}

func (r *recordingDeps) CommitGrok() error {
	if err := r.maybeFail(); err != nil {
		return err
	}
	r.commits++
	return nil
}

func (r *recordingDeps) ClearGrokBuffer() error {
	if err := r.maybeFail(); err != nil {
		return err
	}
	r.clears++
	return nil
}

func (r *recordingDeps) RequestGrokResponse(m grok.ResponseModalities) error {
	if err := r.maybeFail(); err != nil {
		return err
	}
	r.responseReqs = append(r.responseReqs, m)
	return nil
}

func (r *recordingDeps) InjectSystemNote(text string, m grok.ResponseModalities) error {
	if err := r.maybeFail(); err != nil {
		return err
	}
	r.systemNotes = append(r.systemNotes, text)
	return nil
}

func (r *recordingDeps) NotifyBrowser(payload any) {
	r.browserMsgs = append(r.browserMsgs, payload)
}

func (r *recordingDeps) LogUser(text, modality string) {
	r.loggedUser = append(r.loggedUser, text)
}

func (r *recordingDeps) LogAssistant(text string) {
	r.loggedAssistant = append(r.loggedAssistant, text)
}

func (r *recordingDeps) LogSystem(text string, meta map[string]any) {
	r.loggedSystem = append(r.loggedSystem, text)
}

// Helper: run a sequence of events, expecting each to succeed.
func runOK(t *testing.T, f *voiceFSM, evs ...voiceEvent) {
	t.Helper()
	for i, ev := range evs {
		if err := f.Handle(ev); err != nil {
			t.Fatalf("step %d (%s): %v", i, ev.kind, err)
		}
	}
}

func TestHappyPath(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)

	if got, want := f.State(), stateOpening; got != want {
		t.Fatalf("initial state %s, want %s", got, want)
	}

	// xAI session.updated → IDLE
	runOK(t, f, voiceEvent{kind: evSessionReady})
	if got, want := f.State(), stateIdle; got != want {
		t.Fatalf("after session_ready %s, want %s", got, want)
	}

	// First audio frame → RECORDING (and forwarded)
	runOK(t, f, voiceEvent{kind: evAudioFrame, audio: []byte{1, 2, 3}})
	if got, want := f.State(), stateRecording; got != want {
		t.Fatalf("after first frame %s, want %s", got, want)
	}
	if len(deps.audioSent) != 1 {
		t.Fatalf("expected 1 audio forward, got %d", len(deps.audioSent))
	}

	// More frames stay in RECORDING
	runOK(t, f,
		voiceEvent{kind: evAudioFrame, audio: []byte{4, 5}},
		voiceEvent{kind: evAudioFrame, audio: []byte{6, 7}})
	if len(deps.audioSent) != 3 {
		t.Fatalf("expected 3 audio forwards, got %d", len(deps.audioSent))
	}

	// PTT release with speech → COMMITTING (commit sent)
	runOK(t, f, voiceEvent{kind: evPTTUpSpeech})
	if got, want := f.State(), stateCommitting; got != want {
		t.Fatalf("after ptt_up_speech %s, want %s", got, want)
	}
	if deps.commits != 1 {
		t.Fatalf("expected 1 commit, got %d", deps.commits)
	}
	if len(deps.responseReqs) != 0 {
		t.Fatalf("response.create must NOT fire yet — that's the bug this FSM exists to prevent")
	}

	// Transcript completion → RESPONDING (response.create fires)
	runOK(t, f, voiceEvent{kind: evTranscriptDone, transcript: "Hello world"})
	if got, want := f.State(), stateResponding; got != want {
		t.Fatalf("after transcript_done %s, want %s", got, want)
	}
	if len(deps.responseReqs) != 1 {
		t.Fatalf("expected 1 response.create after transcript_done, got %d", len(deps.responseReqs))
	}
	if got, want := deps.loggedUser, []string{"Hello world"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("user log = %v, want %v", got, want)
	}

	// Assistant streams text via AppendAssistantDelta
	f.AppendAssistantDelta("Hi")
	f.AppendAssistantDelta(" there")

	// Response done → IDLE (and assistant text logged)
	runOK(t, f, voiceEvent{kind: evResponseDone})
	if got, want := f.State(), stateIdle; got != want {
		t.Fatalf("after response_done %s, want %s", got, want)
	}
	if got := deps.loggedAssistant; len(got) != 1 || got[0] != "Hi there" {
		t.Fatalf("assistant log = %v, want [Hi there]", got)
	}
}

func TestSilentReleaseClearsBuffer(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f, voiceEvent{kind: evSessionReady})

	// Press, no speech, release with silence flag
	runOK(t, f, voiceEvent{kind: evAudioFrame, audio: []byte{0}})
	if f.State() != stateRecording {
		t.Fatalf("not in recording")
	}
	runOK(t, f, voiceEvent{kind: evPTTUpSilence})
	if got, want := f.State(), stateIdle; got != want {
		t.Fatalf("after ptt_up_silence %s, want %s", got, want)
	}
	if deps.clears != 1 {
		t.Fatalf("expected 1 clear, got %d", deps.clears)
	}
	if deps.commits != 0 {
		t.Fatalf("commit must not fire on silent release, got %d", deps.commits)
	}
	if len(deps.responseReqs) != 0 {
		t.Fatalf("response.create must not fire on silent release")
	}
}

func TestAudioInOpeningIsBuffered(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)

	// Audio arriving before session.updated is buffered, not forwarded.
	runOK(t, f,
		voiceEvent{kind: evAudioFrame, audio: []byte{1}},
		voiceEvent{kind: evAudioFrame, audio: []byte{2}},
		voiceEvent{kind: evAudioFrame, audio: []byte{3}})
	if len(deps.audioSent) != 0 {
		t.Fatalf("audio must not be forwarded in stateOpening, got %d sent", len(deps.audioSent))
	}

	// session.updated → IDLE, backlog flushed in order.
	runOK(t, f, voiceEvent{kind: evSessionReady})
	if f.State() != stateIdle {
		t.Fatalf("not in idle after session_ready")
	}
	if got := deps.audioSent; len(got) != 3 || got[0][0] != 1 || got[1][0] != 2 || got[2][0] != 3 {
		t.Fatalf("backlog flush order wrong: %v", got)
	}
}

func TestTranscriptFailedReturnsToIdle(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f,
		voiceEvent{kind: evSessionReady},
		voiceEvent{kind: evAudioFrame, audio: []byte{1}},
		voiceEvent{kind: evPTTUpSpeech},
	)
	if f.State() != stateCommitting {
		t.Fatalf("expected committing, got %s", f.State())
	}
	runOK(t, f, voiceEvent{kind: evTranscriptFailed})
	if got, want := f.State(), stateIdle; got != want {
		t.Fatalf("after transcript_failed %s, want %s", got, want)
	}
	if len(deps.responseReqs) != 0 {
		t.Fatalf("response.create must not fire on transcript failure")
	}
}

func TestIllegalPTTDuringResponse(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f,
		voiceEvent{kind: evSessionReady},
		voiceEvent{kind: evAudioFrame, audio: []byte{1}},
		voiceEvent{kind: evPTTUpSpeech},
		voiceEvent{kind: evTranscriptDone, transcript: "x"},
	)
	if f.State() != stateResponding {
		t.Fatalf("expected responding, got %s", f.State())
	}
	// ptt_up while responding is illegal (the user couldn't have
	// pressed PTT during a response with a well-behaved UI; if we
	// see it, surface the error rather than corrupting state).
	err := f.Handle(voiceEvent{kind: evPTTUpSpeech})
	if err == nil {
		t.Fatalf("expected illegal-transition error")
	}
}

func TestErrorClosesFromAnyState(t *testing.T) {
	for _, start := range []voiceState{stateOpening, stateIdle, stateRecording, stateCommitting, stateResponding} {
		deps := &recordingDeps{}
		f := newVoiceFSM(deps)
		f.state = start
		err := f.Handle(voiceEvent{kind: evError, err: errors.New("boom")})
		if err != nil {
			t.Fatalf("from %s: %v", start, err)
		}
		if f.State() != stateClosed {
			t.Fatalf("from %s: expected closed, got %s", start, f.State())
		}
	}
}

func TestIdleTimeoutClosesOnlyInIdle(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f, voiceEvent{kind: evSessionReady})

	// Idle timeout in stateIdle: closes
	runOK(t, f, voiceEvent{kind: evIdleTimeout})
	if f.State() != stateClosed {
		t.Fatalf("idle_timeout in idle must close")
	}

	// Verify idle_timeout is illegal in other states.
	for _, start := range []voiceState{stateOpening, stateRecording, stateCommitting, stateResponding} {
		deps := &recordingDeps{}
		f := newVoiceFSM(deps)
		f.state = start
		err := f.Handle(voiceEvent{kind: evIdleTimeout})
		if err == nil {
			t.Fatalf("from %s: idle_timeout should be illegal", start)
		}
	}
}

func TestWorkerCompletionInIdleFiresImmediately(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f, voiceEvent{kind: evSessionReady})

	runOK(t, f, voiceEvent{
		kind:       evWorkerCompletion,
		workerNote: "task complete",
		modalities: grok.ModalitiesTextAudio,
	})
	if f.State() != stateResponding {
		t.Fatalf("worker completion in idle should transition to responding, got %s", f.State())
	}
	if len(deps.systemNotes) != 1 || deps.systemNotes[0] != "task complete" {
		t.Fatalf("system note not injected")
	}
}

func TestWorkerCompletionDuringRespondingQueuesUntilDone(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f,
		voiceEvent{kind: evSessionReady},
		voiceEvent{kind: evAudioFrame, audio: []byte{1}},
		voiceEvent{kind: evPTTUpSpeech},
		voiceEvent{kind: evTranscriptDone, transcript: "x"},
	)
	if f.State() != stateResponding {
		t.Fatalf("setup")
	}

	// Worker completes while Grok is responding → queue.
	runOK(t, f, voiceEvent{
		kind:       evWorkerCompletion,
		workerNote: "worker done",
		modalities: grok.ModalitiesTextAudio,
	})
	if len(deps.systemNotes) != 0 {
		t.Fatalf("system note must not fire while responding, got %v", deps.systemNotes)
	}

	// Response done → drains the queued worker completion → back to responding.
	runOK(t, f, voiceEvent{kind: evResponseDone})
	if f.State() != stateResponding {
		t.Fatalf("after response_done with queued worker, want responding, got %s", f.State())
	}
	if len(deps.systemNotes) != 1 || deps.systemNotes[0] != "worker done" {
		t.Fatalf("queued system note not dispatched after response_done: %v", deps.systemNotes)
	}
}

func TestClosedIsTerminal(t *testing.T) {
	deps := &recordingDeps{}
	f := newVoiceFSM(deps)
	runOK(t, f, voiceEvent{kind: evClose})
	if f.State() != stateClosed {
		t.Fatalf("after close: %s", f.State())
	}
	// Further events are silent no-ops.
	for _, ev := range []voiceEvent{
		{kind: evAudioFrame, audio: []byte{1}},
		{kind: evPTTUpSpeech},
		{kind: evTranscriptDone, transcript: "x"},
		{kind: evResponseDone},
	} {
		if err := f.Handle(ev); err != nil {
			t.Fatalf("post-close %s: %v", ev.kind, err)
		}
	}
	if f.State() != stateClosed {
		t.Fatalf("state drifted from closed: %s", f.State())
	}
}
