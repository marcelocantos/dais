// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/marcelocantos/claudia/grok"
)

// voiceState is the canonical lifecycle for one /ws/voice connection.
// Designed in docs/voice-fsm.md. Transitions are explicit; illegal
// events are surfaced as errors rather than silent no-ops.
type voiceState int

const (
	stateOpening voiceState = iota
	stateIdle
	stateRecording
	stateCommitting
	stateResponding
	stateClosed
)

func (s voiceState) String() string {
	switch s {
	case stateOpening:
		return "opening"
	case stateIdle:
		return "idle"
	case stateRecording:
		return "recording"
	case stateCommitting:
		return "committing"
	case stateResponding:
		return "responding"
	case stateClosed:
		return "closed"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// voiceEventKind enumerates every event the FSM can observe.
type voiceEventKind int

const (
	evSessionReady voiceEventKind = iota // xAI session.updated
	evAudioFrame                         // binary frame from browser
	evPTTUpSpeech                        // {type:"commit"} — browser VAD said yes
	evPTTUpSilence                       // {type:"clear"}  — browser VAD said no
	evTranscriptDone                     // xAI conversation.item.input_audio_transcription.completed
	evTranscriptFailed                   // xAI conversation.item.input_audio_transcription.failed
	evResponseDone                       // xAI response.done
	evWorkerCompletion                   // async delegate result
	evError                              // xAI error / WS error
	evIdleTimeout                        // 30s inactivity
	evClose                              // browser disconnect / explicit close
)

func (k voiceEventKind) String() string {
	switch k {
	case evSessionReady:
		return "session_ready"
	case evAudioFrame:
		return "audio_frame"
	case evPTTUpSpeech:
		return "ptt_up_speech"
	case evPTTUpSilence:
		return "ptt_up_silence"
	case evTranscriptDone:
		return "transcript_done"
	case evTranscriptFailed:
		return "transcript_failed"
	case evResponseDone:
		return "response_done"
	case evWorkerCompletion:
		return "worker_completion"
	case evError:
		return "error"
	case evIdleTimeout:
		return "idle_timeout"
	case evClose:
		return "close"
	}
	return fmt.Sprintf("event(%d)", int(k))
}

// voiceEvent is one input to the FSM. Most kinds need no payload; the
// few that do (audio, transcript, worker results) carry it inline.
type voiceEvent struct {
	kind        voiceEventKind
	audio       []byte
	transcript  string
	workerNote  string // pre-formatted system-note text for worker completions
	modalities  grok.ResponseModalities
	err         error
}

// voiceDeps carries every side effect the FSM is allowed to cause.
// Splitting these out makes the FSM unit-testable without a real
// xAI WS, a real browser, or a real JSONL file.
type voiceDeps interface {
	// xAI side effects
	SendAudioToGrok(pcm []byte) error
	CommitGrok() error
	ClearGrokBuffer() error
	RequestGrokResponse(modalities grok.ResponseModalities) error
	InjectSystemNote(text string, modalities grok.ResponseModalities) error

	// Browser side effects
	NotifyBrowser(payload any)

	// Persistence
	LogUser(text string, modality string)
	LogAssistant(text string)
	LogSystem(text string, meta map[string]any)
}

// voiceFSM owns voice-protocol state for one connection. Goroutine-
// safe: callers send events from any goroutine; Handle serialises.
type voiceFSM struct {
	mu     sync.Mutex
	state  voiceState
	deps   voiceDeps

	// pendingAudio buffers frames that arrive in stateOpening; flushed
	// on transition to stateIdle so the user's first words aren't lost
	// to the ~1.2s xAI handshake.
	pendingAudio [][]byte

	// pendingWorkerNotes queues delegate completions that arrived while
	// not in stateIdle; drained when we return to stateIdle.
	pendingWorkerNotes []pendingNote

	// assistantBuf accumulates streaming assistant transcript deltas
	// across one response; flushed (logged + cleared) at evResponseDone.
	assistantBuf string

	// responseModality is the modality to use for the next response
	// (set by the caller when reception of a turn starts; voice in
	// → text+audio out; text in → text only). Defaults to text+audio.
	responseModality grok.ResponseModalities
}

type pendingNote struct {
	text       string
	modalities grok.ResponseModalities
}

// newVoiceFSM creates an FSM in stateOpening. The caller must drive
// it to stateIdle via evSessionReady once xAI confirms the session.
func newVoiceFSM(deps voiceDeps) *voiceFSM {
	return &voiceFSM{
		state:            stateOpening,
		deps:             deps,
		responseModality: grok.ModalitiesTextAudio,
	}
}

// State returns the current state (for observability and tests).
func (f *voiceFSM) State() voiceState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Handle processes one event. Returns nil on a legal transition (or
// legal no-op), an error on illegal events. Side effects are applied
// synchronously via f.deps before the state changes.
func (f *voiceFSM) Handle(ev voiceEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handleLocked(ev)
}

func (f *voiceFSM) handleLocked(ev voiceEvent) error {
	// evClose and evError are universal — handled before the table so
	// every state can reach stateClosed.
	switch ev.kind {
	case evClose:
		return f.transition(stateClosed, ev, nil)
	case evError:
		slog.Error("voice fsm: error event", "state", f.state, "err", ev.err)
		return f.transition(stateClosed, ev, nil)
	}

	switch f.state {
	case stateOpening:
		return f.handleOpening(ev)
	case stateIdle:
		return f.handleIdle(ev)
	case stateRecording:
		return f.handleRecording(ev)
	case stateCommitting:
		return f.handleCommitting(ev)
	case stateResponding:
		return f.handleResponding(ev)
	case stateClosed:
		// Anything in stateClosed is a no-op (idempotent shutdown).
		return nil
	}
	return fmt.Errorf("voice fsm: unknown state %d", f.state)
}

// --- per-state handlers ------------------------------------------------

func (f *voiceFSM) handleOpening(ev voiceEvent) error {
	switch ev.kind {
	case evSessionReady:
		// Flush pending audio (gathered during the xAI handshake) so
		// it appears in the conversation buffer in receive order.
		// Drop on errors — the next round of audio frames will work.
		for _, frame := range f.pendingAudio {
			if err := f.deps.SendAudioToGrok(frame); err != nil {
				slog.Warn("voice fsm: backlog forward failed", "err", err)
				break
			}
		}
		flushed := len(f.pendingAudio)
		f.pendingAudio = nil
		if flushed > 0 {
			slog.Info("voice fsm: flushed pending audio", "frames", flushed)
		}
		return f.transition(stateIdle, ev, nil)

	case evAudioFrame:
		// Audio before session.updated would be processed under xAI's
		// default turn_detection (server_vad). Buffer instead.
		f.pendingAudio = append(f.pendingAudio, append([]byte(nil), ev.audio...))
		return nil

	default:
		return f.illegal(ev)
	}
}

func (f *voiceFSM) handleIdle(ev voiceEvent) error {
	switch ev.kind {
	case evAudioFrame:
		// First audio frame after IDLE — user has pressed PTT and
		// started talking. Forward immediately and transition to
		// RECORDING. Subsequent frames stay in RECORDING.
		if err := f.deps.SendAudioToGrok(ev.audio); err != nil {
			slog.Warn("voice fsm: audio forward failed", "err", err)
		}
		return f.transition(stateRecording, ev, nil)

	case evPTTUpSilence:
		// User pressed/released without speech (or speech that
		// VAD didn't recognise) — no buffer to clear, no transition.
		return nil

	case evPTTUpSpeech:
		// Commit without preceding audio is odd but harmless; we
		// could still have buffered frames in xAI from a prior
		// transition. Treat as a normal commit.
		return f.commitAndAwaitTranscript()

	case evWorkerCompletion:
		return f.dispatchWorkerNote(ev)

	case evIdleTimeout:
		return f.transition(stateClosed, ev, nil)

	case evSessionReady:
		// Stray duplicate (e.g. session.updated arriving twice) — ignore.
		return nil

	default:
		return f.illegal(ev)
	}
}

func (f *voiceFSM) handleRecording(ev voiceEvent) error {
	switch ev.kind {
	case evAudioFrame:
		if err := f.deps.SendAudioToGrok(ev.audio); err != nil {
			slog.Warn("voice fsm: audio forward failed", "err", err)
		}
		return nil

	case evPTTUpSpeech:
		return f.commitAndAwaitTranscript()

	case evPTTUpSilence:
		if err := f.deps.ClearGrokBuffer(); err != nil {
			slog.Warn("voice fsm: clear failed", "err", err)
		}
		return f.transition(stateIdle, ev, nil)

	case evWorkerCompletion:
		// Queue — we don't want to interrupt an in-progress utterance.
		f.queueWorkerNote(ev)
		return nil

	default:
		return f.illegal(ev)
	}
}

func (f *voiceFSM) handleCommitting(ev voiceEvent) error {
	switch ev.kind {
	case evTranscriptDone:
		// THE critical transition this whole FSM exists to enforce:
		// request the response only after the transcribed user item
		// is in the conversation, so xAI generates against the
		// up-to-date context.
		if ev.transcript != "" {
			f.deps.NotifyBrowser(map[string]any{
				"type": "user_transcript",
				"text": ev.transcript,
			})
			f.deps.LogUser(ev.transcript, "voice")
		}
		if err := f.deps.RequestGrokResponse(f.responseModality); err != nil {
			slog.Error("voice fsm: response.create failed", "err", err)
			return f.transition(stateClosed, ev, err)
		}
		return f.transition(stateResponding, ev, nil)

	case evTranscriptFailed:
		// xAI couldn't transcribe the audio (silence, noise, etc).
		// Drop back to idle without generating a response.
		slog.Info("voice fsm: transcript failed — returning to idle")
		return f.transition(stateIdle, ev, nil)

	case evAudioFrame:
		// Frames arriving during COMMITTING are early audio for the
		// NEXT turn. Buffer until we're back to RECORDING.
		f.pendingAudio = append(f.pendingAudio, append([]byte(nil), ev.audio...))
		return nil

	case evWorkerCompletion:
		f.queueWorkerNote(ev)
		return nil

	default:
		return f.illegal(ev)
	}
}

func (f *voiceFSM) handleResponding(ev voiceEvent) error {
	switch ev.kind {
	case evResponseDone:
		if f.assistantBuf != "" {
			f.deps.LogAssistant(f.assistantBuf)
			f.assistantBuf = ""
		}
		if err := f.transition(stateIdle, ev, nil); err != nil {
			return err
		}
		// Drain any worker completions that came in while we were
		// responding. Each one re-enters RESPONDING; we leave further
		// drain to the next response.done.
		if len(f.pendingWorkerNotes) > 0 {
			note := f.pendingWorkerNotes[0]
			f.pendingWorkerNotes = f.pendingWorkerNotes[1:]
			return f.dispatchWorkerNote(voiceEvent{
				kind:       evWorkerCompletion,
				workerNote: note.text,
				modalities: note.modalities,
			})
		}
		return nil

	case evWorkerCompletion:
		// Queue — current response is in flight.
		f.queueWorkerNote(ev)
		return nil

	case evAudioFrame:
		// Buffer for the next turn (barge-in not supported here).
		f.pendingAudio = append(f.pendingAudio, append([]byte(nil), ev.audio...))
		return nil

	default:
		return f.illegal(ev)
	}
}

// --- helpers -----------------------------------------------------------

// AppendAssistantDelta records a streaming assistant transcript chunk.
// Called from the xAI OnTranscript callback while in stateResponding.
// Doesn't drive a transition; just accumulates for the JSONL log.
func (f *voiceFSM) AppendAssistantDelta(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == stateResponding {
		f.assistantBuf += text
	}
}

// SetResponseModality lets the bridge select text-only vs text+audio
// for the next response. Default is text+audio (voice path).
func (f *voiceFSM) SetResponseModality(m grok.ResponseModalities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responseModality = m
}

func (f *voiceFSM) commitAndAwaitTranscript() error {
	if err := f.deps.CommitGrok(); err != nil {
		slog.Warn("voice fsm: commit failed", "err", err)
		return f.transition(stateIdle, voiceEvent{kind: evPTTUpSpeech}, nil)
	}
	return f.transition(stateCommitting, voiceEvent{kind: evPTTUpSpeech}, nil)
}

func (f *voiceFSM) dispatchWorkerNote(ev voiceEvent) error {
	if ev.workerNote == "" {
		return nil
	}
	mod := ev.modalities
	if len(mod) == 0 {
		mod = f.responseModality
	}
	if err := f.deps.InjectSystemNote(ev.workerNote, mod); err != nil {
		slog.Error("voice fsm: inject system note failed", "err", err)
		return nil
	}
	// Note: the caller (completeTask) already persisted this event to
	// the JSONL log with full metadata. Don't log again here — that's
	// what produced the duplicate "raw result + wrapped note" pairs in
	// the transcript.
	return f.transition(stateResponding, ev, nil)
}

func (f *voiceFSM) queueWorkerNote(ev voiceEvent) {
	f.pendingWorkerNotes = append(f.pendingWorkerNotes, pendingNote{
		text:       ev.workerNote,
		modalities: ev.modalities,
	})
}

// transition performs a state change and broadcasts the new state to
// the browser. The action (any side effects) MUST happen before this
// is called; transition only updates the state field and notifies.
func (f *voiceFSM) transition(next voiceState, ev voiceEvent, exitErr error) error {
	if f.state == next {
		// Side-effect-only events (e.g. audio frame in RECORDING) call
		// transition(state, ev, nil) as a no-op; skip the broadcast.
		return nil
	}
	prev := f.state
	f.state = next
	slog.Debug("voice fsm: transition",
		"from", prev, "to", next, "event", ev.kind)
	payload := map[string]any{"type": "state", "state": next.String()}
	if exitErr != nil {
		payload["err"] = exitErr.Error()
	}
	f.deps.NotifyBrowser(payload)
	return exitErr
}

// illegal returns a descriptive error for an event that has no
// transition out of the current state. Callers typically log it as
// a warning rather than treating it as fatal — the protocol stays
// alive; only one event is rejected.
func (f *voiceFSM) illegal(ev voiceEvent) error {
	return fmt.Errorf("voice fsm: illegal event %s in state %s", ev.kind, f.state)
}
