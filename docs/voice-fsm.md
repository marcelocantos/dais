# Voice FSM (🎯T18 cleanup)

## Why this paper

In the course of building Grok-as-overseer voice (🎯T18 phase 1a–1c) we
hit six distinct race bugs in roughly as many days. Each was patched
locally, and each patch interacted weirdly with the next bug. The
underlying problem isn't any one of those races — it's that voice has
two cooperating state machines (a browser-side one and an xAI-side
one) that have never been formally modelled in our code. We've been
inferring state from incident reports.

This paper writes the state machine down so the implementation has
exactly one source of truth and illegal transitions are explicit
errors rather than implicit wedges.

## Cast of actors and their data flows

```
┌──────────────────┐  binary PCM frames        ┌─────────────────┐  binary PCM ┌──────────┐
│ Browser (PTT UI) │ ─────────────────────────►│ jevonsd /ws/voice│────────────►│ xAI Real │
│                  │  {type:"ptt_down"} etc.   │                  │  JSON cmds  │ time WS  │
│                  │◄───────────────────────── │                  │◄───────────►│          │
│   chat UI        │  {type:"state"} etc.      │                  │  JSON evts  │          │
└──────────────────┘                           └─────────────────┘             └──────────┘
        │                                                                            │
        │ user speaks / presses Ctrl                                                  │
        │                                            xAI internal flow:              │
        │                                            audio_buffer.append             │
        │                                                ↓                           │
        │                                            audio_buffer.commit             │
        │                                                ↓                           │
        │                                            (async transcription)           │
        │                                                ↓                           │
        │                                            conversation.item.added         │
        │                                            (with transcript)               │
        │                                                ↓                           │
        │                                            response.create  ←──── client driven
        │                                                ↓                           │
        │                                            response.* stream               │
        │                                                ↓                           │
        │                                            response.done                   │
```

The critical observation: the **audio buffer → conversation.item**
step is asynchronous. xAI ACKs the commit immediately (sub-100 ms) but
the transcribed item arrives 200–600 ms later. If we send
`response.create` between those two events, the response is generated
against a conversation that does not yet contain the user's
just-committed audio. That's the most painful bug in the current
implementation.

## Proposed states (server-side)

The server-side FSM is the canonical one. The browser is a thin
client: it reports user intent and renders state the server has told
it about.

| State | Means | Legal next states |
|---|---|---|
| `OPENING` | WS up, `session.update` sent, awaiting `session.updated` | `IDLE`, `CLOSED` |
| `IDLE` | Session configured. No audio in flight. Awaiting user input. | `RECORDING`, `RESPONDING` (text input), `CLOSED` |
| `RECORDING` | User holding PTT; audio frames being forwarded to xAI's input buffer | `COMMITTING`, `IDLE` (silent release), `CLOSED` |
| `COMMITTING` | `input_audio_buffer.commit` sent; awaiting `conversation.item.input_audio_transcription.completed` | `RESPONDING`, `IDLE` (transcription failure), `CLOSED` |
| `RESPONDING` | `response.create` sent; awaiting `response.done` | `IDLE`, `CLOSED` |
| `CLOSED` | terminal — voice WS torn down | — |

Note: this models *one* in-flight turn at a time. Barge-in (user
speaking over Grok's response) and async worker completions are
discussed separately below.

## Events

External events that drive transitions:

### From browser

| Event | Body | Notes |
|---|---|---|
| `ws_open` | — | Implicit; triggers `OPENING` |
| `ptt_down` | — | User pressed Ctrl past arm delay |
| `audio_frame` | binary | Mic PCM, valid only in `RECORDING` (else dropped) |
| `ptt_up` | `{heard_speech: bool}` | User released; flag is the browser-side VAD verdict |
| `text_input` | `{text: string}` | Future (Phase 1d): typed prompt |
| `ws_close` | — | Implicit; triggers `CLOSED` |

### From xAI

| Event | Maps to |
|---|---|
| `session.updated` | session_ready |
| `input_audio_buffer.committed` | commit_ack (no transition; awaited by `COMMITTING`) |
| `conversation.item.input_audio_transcription.completed` | transcript_done |
| `conversation.item.input_audio_transcription.failed` | transcript_failed |
| `response.created` | response_started (no transition; observed in `RESPONDING`) |
| `response.output_audio_transcript.delta` | streamed text out (relayed to browser) |
| `response.output_audio.delta` | streamed audio out (relayed to browser) |
| `response.done` | response_done |
| `error` | error |

### Internal

| Event | Trigger |
|---|---|
| `idle_timeout` | 30 s with no activity (only valid in `IDLE`) |
| `worker_completion` | A previously-dispatched `delegate(...)` finished. Synthesised system note + response request. Initiates `IDLE → RESPONDING` |

## Transition table

```
                              ┌──── ptt_down ───►┐
                              │                  │
                              │                  ▼
                  ┌──────────►┌─────┐         ┌────────────┐
   session_ready ─┘           │IDLE │         │ RECORDING  │
                              │     │◄────────│            │
                              │     │ ptt_up  └─────┬──────┘
                              │     │ (silent)      │
                              │     │  clear        │ ptt_up
                              │     │               │ (speech)
                              │     │               │  → commit
                              │     │               ▼
                              │     │         ┌────────────┐
                              │     │         │ COMMITTING │
                              │     │ trans   │            │
                              │     │ failed  └─────┬──────┘
                              │     │               │ transcript
                              │     │               │ _done
                              │     │               │ → response.create
                              │     │               ▼
                              │     │         ┌────────────┐
                              │     │◄────────│ RESPONDING │
                              │     │ response└────────────┘
                              └─────┘  _done

                  + any state + error/ws_close → CLOSED
                  + IDLE + idle_timeout → CLOSED
                  + IDLE + worker_completion → RESPONDING
                                              (system note + response.create)
                  + IDLE + text_input → COMMITTING_TEXT → RESPONDING  (Phase 1d)
```

## Things this eliminates by construction

- **commit/response race.** `response.create` is only emitted in the
  `COMMITTING → RESPONDING` transition, which fires on
  `transcript_done`. Impossible to race the transcription.
- **server_vad mid-handshake.** `audio_frame` arriving in `OPENING`
  is buffered, not forwarded, because forwarding is only a
  side-effect of being in `RECORDING`.
- **Empty-buffer commits.** `commit` only fires on
  `RECORDING → COMMITTING`, which is only entered on
  `ptt_up{heard_speech: true}`. Silent release goes
  `RECORDING → IDLE` via `clear`.
- **Zombie xAI clients.** `CLOSED` is terminal and is reached from
  any state via `error` or `ws_close`. The transition unconditionally
  closes the xAI WS and clears all per-connection state.
- **Replay-vs-fresh-turn confusion.** Replay runs in `OPENING` only;
  by the time we reach `IDLE` the conversation already has the
  history. New audio in `RECORDING` always appears AFTER it.

## Things it doesn't yet handle (deferred)

- **Barge-in.** PTT down while in `RESPONDING` could either be
  rejected (browser shows a "wait" indicator) or cancel the
  response and start a new turn. Defer; reject for now.
- **Multiple in-flight delegations.** Worker completions can arrive
  during any state. If state is `RESPONDING`, queue and dispatch
  after `response_done`. If `IDLE`, fire immediately. If `RECORDING`,
  defer — the user is in the middle of a new turn.
- **Text input** (Phase 1d) needs its own micro-transition. Probably
  `IDLE + text_input → RESPONDING` with the text appended as a
  `conversation.item.create` of role=user then `response.create`.

## Browser FSM

The browser holds a smaller FSM driven by UI events and the server's
`{type:"state",...}` messages. States:

| State | Means | Triggered by |
|---|---|---|
| `disconnected` | No voice WS | initial, ws close |
| `connecting` | WS open, server in `OPENING` | server `state: opening` |
| `idle` | Ready for user input | server `state: idle` |
| `recording` | Ctrl held; mic capturing; VAD watching | server `state: recording` (initiated by browser sending ptt_down) |
| `committing` | Server processing user's audio | server `state: committing` |
| `responding` | Grok generating | server `state: responding` |
| `error` | Server reported an error | server `state: closed` with err |

The browser-side VAD lives at the input gate of `recording`: it
counts `heardSpeech` and sets the flag on the `{type:"ptt_up"}` it
sends. The server doesn't need to know about local VAD; it just
trusts the boolean.

## Message protocol (revised)

Before this refactor: `{type:"commit"|"clear"|"stop"|"inject"}` over
/ws/voice; status events back as `{type:"status",status:"..."}`. The
browser also computes VAD and sends only "speech" frames; it manages
its own UI state.

After: stricter, fewer message types, server is canonical.

**Browser → Server (`/ws/voice`):**

```
binary frames                 — PCM16 24 kHz mono, sent only while RECORDING
{type:"ptt_down"}             — user engaged PTT
{type:"ptt_up", heard_speech} — user released; flag from browser VAD
{type:"text", text}           — typed prompt (Phase 1d)
```

**Server → Browser (`/ws/voice`):**

```
binary frames                 — Grok response audio, only during RESPONDING
{type:"state", state, err?}   — canonical state transition
{type:"user_transcript", text, modality}
{type:"assistant_transcript", text}        — streaming delta
{type:"assistant_transcript_done"}
```

The chat panel is still tailed by the JSONL persistence layer (we
don't push individual turns over the voice WS — they're persisted
server-side and rendered from the JSONL).

## Implementation outline (Go)

```go
package server

type VoiceState int

const (
    StateOpening VoiceState = iota
    StateIdle
    StateRecording
    StateCommitting
    StateResponding
    StateClosed
)

// voiceFSM owns all per-connection voice state. There is one per
// open /ws/voice connection. All mutations go through Transition;
// callers can only request a transition by sending an event.
type voiceFSM struct {
    mu    sync.Mutex
    state VoiceState

    // dependencies (set on construction):
    grok        *grok.Client
    sendToBrowser func(any) error
    log         *GrokLog
    // ...
}

func (f *voiceFSM) Handle(ev voiceEvent) error {
    f.mu.Lock()
    defer f.mu.Unlock()
    next, action, ok := transitionTable[stateEvent{f.state, ev.kind}]
    if !ok {
        return fmt.Errorf("voice: illegal event %s in state %s", ev.kind, f.state)
    }
    if err := action(f, ev); err != nil {
        return err
    }
    f.state = next
    return f.broadcastState()
}
```

The transition table is data, not control flow. New states or
transitions are table edits; the dispatcher loop stays the same.

A small test suite (`voice_fsm_test.go`) exercises the table:

- Happy path: full PTT cycle reaches `IDLE` again.
- Silent release: `RECORDING → IDLE`, no `commit` sent.
- Mid-response PTT: rejected with explicit error event.
- session.updated arrives mid-RECORDING: ignored (already past
  `OPENING`).
- Error in any state: cleanly closes.

## Migration plan

1. Add the FSM behind voice.go's existing handler; emit no new
   messages yet, just observe.
2. Switch each existing condition (grokReady, pendingAudio,
   isResponding) over to FSM state checks.
3. Once the FSM is the source of truth, change the message
   protocol on both sides to match (browser ptt_down/up rather than
   commit/clear) and delete the old fields.
4. Update the three harnesses (xai-loop, jevons-loop, browser-loop)
   to use the new browser↔server protocol.
5. Land as a single PR; the voice path is small enough that
   partial migration is more confusing than the cutover.

No production behaviour changes from steps 1–2; step 3 is the actual
cut. Tests at every step.
