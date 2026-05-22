package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/claudia"
	"github.com/marcelocantos/claudia/grok"
)

// pendingTask tracks a delegate() call that's in flight against a
// worker. The Grok session is notified of completion via SendSystemNote
// once the worker emits its response (or the task times out / errors).
type pendingTask struct {
	id        string
	agent     string
	task      string
	startedAt time.Time
}

// voiceIdleTimeout closes the Grok session after this much wall-clock
// time with no activity. Activity = audio frame from client, an
// explicit commit, or response.done from Grok. The window is wide
// enough to absorb a thinking pause between back-and-forth utterances
// (~1.2s Grok handshake is too long to pay per turn) but short enough
// that an abandoned tab doesn't hold a session open indefinitely.
const voiceIdleTimeout = 30 * time.Second

// VoiceBridge manages the Grok Realtime session and bridges audio
// between a connected client and Grok. Only one voice session is
// active at a time. The protocol logic for a connection lives in
// voiceFSM (docs/voice-fsm.md); VoiceBridge holds process-wide
// resources (xAI API key, registry handle, task map, JSONL log) and
// the current FSM pointer so out-of-band events (worker completion,
// JSONL replay) can find the active connection.
type VoiceBridge struct {
	srv    *Server
	apiKey string

	mu       sync.Mutex
	voiceWS  *websocket.Conn // the connected browser/iOS client
	voiceCtx context.Context
	fsm      *voiceFSM // active FSM, or nil when no connection
	conn     *voiceConn // active connection's transport bundle

	// tasks tracks dispatched delegate() calls so task_status() can
	// report on them and so the completion path can find the
	// originating call metadata.
	tasksMu sync.Mutex
	tasks   map[string]*pendingTask

	// log persists the Grok conversation across sessions so context
	// survives idle timeouts, server restarts, and page reloads. Each
	// new Grok session replays the recent tail before user audio starts
	// flowing so the model picks up where the previous session left off.
	log *GrokLog
}

// voiceConn bundles the per-connection state the FSM's deps adapter
// needs to reach. Lives in HandleVoiceWS's scope; pointer cached on
// VoiceBridge for worker-completion lookup.
type voiceConn struct {
	conn    *websocket.Conn
	connCtx context.Context
	grokCtx context.Context
	client  *grok.Client

	idleMu    sync.Mutex
	idleTimer *time.Timer
}

func (vc *voiceConn) resetIdle() {
	vc.idleMu.Lock()
	if vc.idleTimer != nil {
		vc.idleTimer.Reset(voiceIdleTimeout)
	}
	vc.idleMu.Unlock()
}

// grokReplayTurns is how many recent log entries are replayed into a
// fresh Grok session. Keep this bounded — every replayed item is input
// for the next response.create, so the cost and latency of session
// start scales linearly with this number.
const grokReplayTurns = 20

func newTaskID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "t-" + hex.EncodeToString(b[:])
}

// NewVoiceBridge creates a voice bridge with the given xAI API key.
// The Grok conversation log lives at ~/.jevons/grok/conversation.jsonl
// and persists across server restarts; failure to open it is logged
// but non-fatal — the bridge still functions, just without persistence.
func NewVoiceBridge(srv *Server, apiKey string) *VoiceBridge {
	logPath := filepath.Join(os.Getenv("HOME"), ".jevons", "grok", "conversation.jsonl")
	log, err := NewGrokLog(logPath)
	if err != nil {
		slog.Warn("voice: grok log unavailable — running without persistence", "path", logPath, "err", err)
	} else {
		slog.Info("voice: grok log opened", "path", logPath)
	}
	return &VoiceBridge{
		srv:    srv,
		apiKey: apiKey,
		tasks:  make(map[string]*pendingTask),
		log:    log,
	}
}

// HandleVoiceWS handles /ws/voice connections. Protocol enforced by
// voiceFSM (docs/voice-fsm.md):
//
//   Browser → server:
//     - Binary frames    : raw 24 kHz mono PCM16 audio
//     - {"type":"commit"}: PTT release, browser VAD detected speech
//     - {"type":"clear"} : PTT release, browser VAD detected nothing
//
//   Server → browser:
//     - Binary frames                       : Grok's spoken audio
//     - {"type":"state", "state":...}       : FSM transitions
//     - {"type":"user_transcript", ...}     : user STT result
//     - {"type":"assistant_transcript",...} : streaming assistant text
//     - {"type":"assistant_transcript_done"}
//     - {"type":"error", "error": ...}
func (vb *VoiceBridge) HandleVoiceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("voice: accept failed", "err", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)

	// The HTTP request context cancels when the handler returns. Grok
	// needs a longer-lived context — the FSM's evClose path drives the
	// final Commit/Close.
	grokCtx, grokCancel := context.WithCancel(context.Background())
	defer grokCancel()
	ctx := r.Context()

	vc := &voiceConn{conn: conn, connCtx: ctx, grokCtx: grokCtx}
	deps := &bridgeDeps{bridge: vb, vc: vc}
	fsm := newVoiceFSM(deps)
	deps.fsm = fsm // back-ref for timer callbacks that dispatch events

	// Register the FSM on the bridge so out-of-band events (worker
	// completions) can find it. Tear down any prior session that
	// leaked.
	vb.mu.Lock()
	if stale := vb.conn; stale != nil && stale.client != nil {
		_ = stale.client.Close()
	}
	vb.voiceWS = conn
	vb.voiceCtx = ctx
	vb.fsm = fsm
	vb.conn = vc
	vb.mu.Unlock()
	defer func() {
		deps.cancelCommitTimeout()
		_ = fsm.Handle(voiceEvent{kind: evClose})
		vb.mu.Lock()
		if vb.fsm == fsm {
			vb.fsm = nil
		}
		if vb.conn == vc {
			vb.conn = nil
		}
		if vb.voiceWS == conn {
			vb.voiceWS = nil
		}
		client := vc.client
		vc.client = nil
		vb.mu.Unlock()
		if client != nil {
			_ = client.Close()
			slog.Info("voice: Grok session closed")
		}
	}()

	// Idle timer fires evIdleTimeout. Legal only in stateIdle; other
	// states' transitions reset it via vc.resetIdle().
	vc.idleTimer = time.AfterFunc(voiceIdleTimeout, func() {
		slog.Info("voice: idle timeout")
		if err := fsm.Handle(voiceEvent{kind: evIdleTimeout}); err != nil {
			slog.Debug("voice: idle timeout in non-idle state", "err", err)
		}
		// Force the WS shut regardless — closing the conn ends the
		// read loop and triggers full cleanup.
		_ = conn.Close(websocket.StatusNormalClosure, "idle timeout")
	})
	defer vc.idleTimer.Stop()

	slog.Info("voice: client connected")
	vb.sendJSON(conn, ctx, map[string]any{"type": "status", "status": "connected"})

	// Open the xAI session. Callbacks are wired to dispatch FSM events.
	if err := vb.startGrokSession(grokCtx, fsm, vc); err != nil {
		slog.Error("voice: grok session failed", "err", err)
		_ = fsm.Handle(voiceEvent{kind: evError, err: err})
		vb.sendJSON(conn, ctx, map[string]any{"type": "error", "error": err.Error()})
		return
	}

	// Read loop. Every event is translated into an FSM event; the FSM
	// owns all protocol decisions.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			slog.Info("voice: client disconnected")
			return
		}
		vc.resetIdle()

		switch mt {
		case websocket.MessageBinary:
			audio := append([]byte(nil), data...) // copy: Read buffer is reused
			if err := fsm.Handle(voiceEvent{kind: evAudioFrame, audio: audio}); err != nil {
				slog.Warn("voice: audio frame rejected", "err", err)
			}

		case websocket.MessageText:
			var msg struct{ Type string }
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			var kind voiceEventKind
			switch msg.Type {
			case "commit":
				kind = evPTTUpSpeech
			case "clear":
				kind = evPTTUpSilence
			default:
				slog.Debug("voice: ignoring unknown message type", "type", msg.Type)
				continue
			}
			if err := fsm.Handle(voiceEvent{kind: kind}); err != nil {
				slog.Warn("voice: control event rejected", "type", msg.Type, "err", err)
			}
		}
	}
}

// commitTranscriptTimeout bounds how long the FSM waits in
// stateCommitting after sending input_audio_buffer.commit. xAI's
// transcription is async; in normal operation it lands in 200–600 ms,
// but observed silently-stuck commits (no completed AND no failed
// event) have wedged the bridge in production. 6 s is comfortably
// past the worst legitimate latency we've measured and short enough
// that subsequent PTT attempts aren't lost for long while the FSM
// is wedged in COMMITTING.
const commitTranscriptTimeout = 6 * time.Second

// bridgeDeps adapts the bridge's per-connection resources (xAI client,
// browser WS, JSONL log) to the voiceDeps interface the FSM consumes.
// All side effects route through one place; the FSM stays pure logic.
type bridgeDeps struct {
	bridge *VoiceBridge
	vc     *voiceConn
	fsm    *voiceFSM // set after FSM construction so timer callbacks can dispatch

	commitTimerMu sync.Mutex
	commitTimer   *time.Timer
}

func (d *bridgeDeps) SendAudioToGrok(pcm []byte) error {
	if d.vc.client == nil {
		return nil
	}
	return d.vc.client.SendAudio(d.vc.grokCtx, pcm)
}

func (d *bridgeDeps) CommitGrok() error {
	d.scheduleCommitTimeout()
	if d.vc.client == nil {
		return nil
	}
	return d.vc.client.Commit(d.vc.grokCtx)
}

func (d *bridgeDeps) ClearGrokBuffer() error {
	d.cancelCommitTimeout()
	if d.vc.client == nil {
		return nil
	}
	return d.vc.client.ClearBuffer(d.vc.grokCtx)
}

func (d *bridgeDeps) RequestGrokResponse(m grok.ResponseModalities) error {
	d.cancelCommitTimeout()
	if d.vc.client == nil {
		return nil
	}
	return d.vc.client.RequestResponse(d.vc.grokCtx, m)
}

// scheduleCommitTimeout starts (or replaces) the stale-commit timer.
// If xAI doesn't return a transcription event within
// commitTranscriptTimeout, the timer fires evTranscriptFailed so the
// FSM falls back to IDLE rather than wedging forever.
func (d *bridgeDeps) scheduleCommitTimeout() {
	d.commitTimerMu.Lock()
	defer d.commitTimerMu.Unlock()
	if d.commitTimer != nil {
		d.commitTimer.Stop()
	}
	d.commitTimer = time.AfterFunc(commitTranscriptTimeout, func() {
		slog.Warn("voice: transcript timeout — xAI silent after commit; unsticking FSM")
		if d.fsm != nil {
			_ = d.fsm.Handle(voiceEvent{kind: evTranscriptFailed})
		}
	})
}

func (d *bridgeDeps) cancelCommitTimeout() {
	d.commitTimerMu.Lock()
	defer d.commitTimerMu.Unlock()
	if d.commitTimer != nil {
		d.commitTimer.Stop()
		d.commitTimer = nil
	}
}

func (d *bridgeDeps) InjectSystemNote(text string, m grok.ResponseModalities) error {
	if d.vc.client == nil {
		return nil
	}
	return d.vc.client.SendSystemNote(d.vc.grokCtx, text, m)
}

func (d *bridgeDeps) NotifyBrowser(payload any) {
	d.bridge.sendJSON(d.vc.conn, d.vc.connCtx, payload)
}

func (d *bridgeDeps) LogUser(text, modality string) {
	d.bridge.logAppend(GrokLogEntry{
		Role:    "user",
		Content: text,
		Meta:    map[string]any{"modality": modality},
	})
}

func (d *bridgeDeps) LogAssistant(text string) {
	d.bridge.logAppend(GrokLogEntry{Role: "assistant", Content: text})
}

func (d *bridgeDeps) LogSystem(text string, meta map[string]any) {
	d.bridge.logAppend(GrokLogEntry{Role: "system", Content: text, Meta: meta})
}

func (vb *VoiceBridge) startGrokSession(ctx context.Context, fsm *voiceFSM, vc *voiceConn) error {
	slog.Info("voice: connecting to Grok Realtime")

	client, err := grok.Connect(ctx, grok.Config{
		APIKey: vb.apiKey,
		// Voice options (xAI Grok Realtime): eve (F, upbeat — default),
		// ara (F, warm), rex (M, professional), sal (neutral, smooth),
		// leo (M, authoritative). Picked leo as the closest fit to
		// "Alfred / dignified butler" — try rex if leo lands too heavy.
		Voice: "leo",
		// Push-to-talk: the user is the VAD. Suppress Grok's server-side
		// VAD so a mid-phrase pause doesn't auto-commit half the utterance.
		// The bridge calls CommitAndRespond on the client's "commit" message.
		ManualCommit: true,
		SystemPrompt: `You are Jevons, the overseer of a team of AI workers.
Each worker is an autonomous agent with its own tools and project
scope. Your job is to converse with the user, dispatch work to the
right worker, and weave their results back into the conversation.

Your name is "Jevons" (with an s) — never "Jevon".

How to behave:

- DO NOT greet the user when a session starts. Wait silently for
  the user to speak or type first. No "Hi, I'm Jevons" openers.
- For chit-chat, greetings, clarifications, opinions, and simple
  factual questions you can answer yourself, respond directly. Do
  not delegate trivia.
- For any substantive work — code, file edits, research, target
  management, anything that needs a specialised tool or knowledge of
  a specific project — call the "delegate" tool. Pick the right
  worker for the job; use "list_agents" if you're unsure who's
  available.
- "jevons" is your default project worker for general work on the
  jevons project itself. "jevons-po" handles target / product-owner
  questions (bullseye targets, project status, prioritisation).
  Other workers may be registered for specific projects.

Delegation is ASYNCHRONOUS. When you call delegate, the tool returns
immediately with a task_id. Acknowledge the dispatch briefly to the
user ("on it", "looking into that") and continue the conversation
normally — DO NOT wait silently for the result. When the worker
finishes, you will receive a system message containing the result.
At that point, summarise the result conversationally and naturally
for the user, as if relaying news from a colleague. You may have
several tasks in flight at once; treat each completion notification
independently.

Be concise. The user is often hands-free in a car — keep responses
short and verbal-friendly. Avoid markdown, bullet lists, or anything
that doesn't read well aloud.`,

		Tools: []grok.Tool{
			{
				Type:        "function",
				Name:        "delegate",
				Description: "Dispatch a task to a named worker agent. Returns immediately with a task_id; the worker's result will arrive asynchronously as a system message. Use this for any substantive work that requires code, file access, research, or specialised tools. Acknowledge the dispatch briefly to the user and continue the conversation; do not wait silently.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"agent_name": {
							"type": "string",
							"description": "Name of the worker agent to dispatch to. Use list_agents if unsure."
						},
						"task": {
							"type": "string",
							"description": "The task for the worker, phrased as a clear natural-language instruction. Include any context the worker would not have from its own session."
						}
					},
					"required": ["agent_name", "task"]
				}`),
			},
			{
				Type:        "function",
				Name:        "list_agents",
				Description: "List the worker agents available for delegation, with their working directories and current status.",
				Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			},
			{
				Type:        "function",
				Name:        "task_status",
				Description: "Check the status of a previously-dispatched task by task_id. Useful if the user asks whether something is still running.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"task_id": {"type": "string"}
					},
					"required": ["task_id"]
				}`),
			},
		},

		OnAudio: func(pcm []byte) {
			// Grok response audio: forward as a binary frame to the
			// browser. No FSM event — it's just a side-effect of the
			// current response stream.
			if err := vc.conn.Write(vc.connCtx, websocket.MessageBinary, pcm); err != nil {
				slog.Debug("voice: client audio write failed", "err", err)
			}
		},

		OnTranscript: func(text string) {
			// Streaming assistant text delta. FSM accumulates for the
			// log; we relay to the browser for live display.
			fsm.AppendAssistantDelta(text)
			vb.sendJSON(vc.conn, vc.connCtx, map[string]any{
				"type": "assistant_transcript",
				"text": text,
			})
		},

		OnTranscriptDone: func() {
			// Streaming text turn finished. Not a state transition;
			// response.done is the FSM-relevant event.
			vb.sendJSON(vc.conn, vc.connCtx, map[string]any{
				"type": "assistant_transcript_done",
			})
		},

		OnUserTranscript: func(text string) {
			slog.Info("voice: user said", "text", text)
			// This is the FSM's signal to transition COMMITTING →
			// RESPONDING. The transition action sends response.create
			// AFTER the user item is in the conversation, eliminating
			// the commit/response race.
			if err := fsm.Handle(voiceEvent{
				kind:       evTranscriptDone,
				transcript: text,
			}); err != nil {
				slog.Warn("voice: transcript event rejected", "err", err)
			}
		},

		OnFunctionCall: vb.handleFunctionCall,

		OnSessionReady: func() {
			slog.Info("voice: Grok session ready")
			// Replay history INTO THE CONVERSATION (not as a response
			// trigger) so context survives reconnects. Runs before the
			// FSM transitions to IDLE, so the conversation has history
			// in place before any audio commits.
			if vc.client != nil {
				vb.replayLog(ctx, vc.client)
			}
			// Drive the FSM out of OPENING. The transition action
			// flushes the pendingAudio backlog in receive order.
			if err := fsm.Handle(voiceEvent{kind: evSessionReady}); err != nil {
				slog.Warn("voice: session_ready event rejected", "err", err)
			}
			// Tell the browser the session is hot.
			vb.sendJSON(vc.conn, vc.connCtx, map[string]any{
				"type":   "status",
				"status": "ready",
			})
		},

		OnResponseDone: func() {
			slog.Debug("voice: Grok response done")
			vc.resetIdle()
			if err := fsm.Handle(voiceEvent{kind: evResponseDone}); err != nil {
				slog.Warn("voice: response_done event rejected", "err", err)
			}
			vb.sendJSON(vc.conn, vc.connCtx, map[string]any{
				"type":   "status",
				"status": "ready",
			})
		},

		OnError: func(err error) {
			slog.Error("voice: grok error", "err", err)
			// Drive the FSM to CLOSED. The browser is also informed.
			_ = fsm.Handle(voiceEvent{kind: evError, err: err})
			vb.sendJSON(vc.conn, vc.connCtx, map[string]any{
				"type":  "error",
				"error": err.Error(),
			})
		},
	})
	if err != nil {
		return err
	}
	vc.client = client
	// Replay + pendingAudio flush happen from OnSessionReady when
	// session.updated arrives — until then audio frames are buffered
	// by the FSM (state OPENING) and any item-create would race the
	// session config.
	return nil
}

func (vb *VoiceBridge) sendJSON(conn *websocket.Conn, ctx context.Context, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	conn.Write(ctx, websocket.MessageText, data)
}

// logAppend writes an entry to the persistent Grok conversation log.
// Failures are logged but never propagated — losing log persistence
// must not break the live voice flow.
func (vb *VoiceBridge) logAppend(e GrokLogEntry) {
	if vb.log == nil {
		return
	}
	if err := vb.log.Append(e); err != nil {
		slog.Warn("voice: grok log append failed", "err", err)
	}
}

// replayLog injects the last grokReplayTurns of conversation history
// into the freshly-opened Grok session so context survives session
// boundaries. Items are inserted via conversation.item.create with no
// response.create — Grok absorbs the history silently and is ready
// for the user's next input.
func (vb *VoiceBridge) replayLog(ctx context.Context, client *grok.Client) {
	if vb.log == nil {
		return
	}
	entries, err := vb.log.Tail(grokReplayTurns)
	if err != nil {
		slog.Warn("voice: grok log tail failed", "err", err)
		return
	}
	if len(entries) == 0 {
		return
	}
	slog.Info("voice: replaying conversation history", "turns", len(entries))
	for _, e := range entries {
		if err := client.InjectConversationItem(ctx, e.Role, e.Content); err != nil {
			slog.Warn("voice: replay inject failed", "role", e.Role, "err", err)
			return
		}
	}
}

// handleFunctionCall dispatches Grok tool calls. All tools return a
// JSON-encoded result string; on error the second return is non-nil
// and Grok sees a generic failure (we log the detail).
func (vb *VoiceBridge) handleFunctionCall(name string, args json.RawMessage) (string, error) {
	switch name {
	case "delegate":
		return vb.toolDelegate(args)
	case "list_agents":
		return vb.toolListAgents()
	case "task_status":
		return vb.toolTaskStatus(args)
	default:
		slog.Warn("voice: unknown function call", "name", name)
		return `{"error":"unknown function"}`, nil
	}
}

func (vb *VoiceBridge) toolDelegate(args json.RawMessage) (string, error) {
	var p struct {
		AgentName string `json:"agent_name"`
		Task      string `json:"task"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.AgentName == "" || p.Task == "" {
		return `{"error":"agent_name and task are required"}`, nil
	}

	agent := vb.srv.GetAgent(p.AgentName)
	if agent == nil {
		slog.Warn("voice: delegate to unknown agent", "agent", p.AgentName)
		return fmt.Sprintf(`{"error":"agent %q not registered"}`, p.AgentName), nil
	}

	id := newTaskID()
	pt := &pendingTask{
		id:        id,
		agent:     p.AgentName,
		task:      p.Task,
		startedAt: time.Now(),
	}
	vb.tasksMu.Lock()
	vb.tasks[id] = pt
	vb.tasksMu.Unlock()

	slog.Info("voice: delegating", "task_id", id, "agent", p.AgentName, "task", p.Task)
	go vb.runDelegatedTask(pt, agent)

	return fmt.Sprintf(`{"task_id":%q,"agent":%q,"status":"dispatched"}`, id, p.AgentName), nil
}

func (vb *VoiceBridge) toolListAgents() (string, error) {
	defs := vb.srv.RegistryAgents()
	type entry struct {
		Name    string `json:"name"`
		WorkDir string `json:"workdir,omitempty"`
		Status  string `json:"status"`
	}
	out := make([]entry, 0, len(defs))
	for _, d := range defs {
		status := "stopped"
		if a := vb.srv.GetAgent(d.Name); a != nil && a.Alive() {
			status = "running"
		}
		out = append(out, entry{Name: d.Name, WorkDir: d.WorkDir, Status: status})
	}
	b, err := json.Marshal(map[string]any{"agents": out})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (vb *VoiceBridge) toolTaskStatus(args json.RawMessage) (string, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	vb.tasksMu.Lock()
	pt := vb.tasks[p.TaskID]
	vb.tasksMu.Unlock()
	if pt == nil {
		return fmt.Sprintf(`{"task_id":%q,"status":"unknown"}`, p.TaskID), nil
	}
	return fmt.Sprintf(`{"task_id":%q,"agent":%q,"status":"in_flight","started_at":%q}`,
		pt.id, pt.agent, pt.startedAt.Format(time.RFC3339)), nil
}

// runDelegatedTask is the goroutine body for one delegated worker
// invocation. It blocks on the worker's response (or a 10-minute
// hard timeout), then injects a system-role completion note into the
// Grok session so the overseer can surface the result conversationally.
func (vb *VoiceBridge) runDelegatedTask(pt *pendingTask, agent *claudia.Agent) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	if err := agent.Send(pt.task); err != nil {
		vb.completeTask(pt, "", fmt.Errorf("send to worker failed: %w", err))
		return
	}

	result, err := agent.WaitForResponse(ctx)
	vb.completeTask(pt, result, err)
}

// completeTask records the outcome of a delegated task and injects
// a system-role notification into the Grok session.
func (vb *VoiceBridge) completeTask(pt *pendingTask, result string, taskErr error) {
	vb.tasksMu.Lock()
	delete(vb.tasks, pt.id)
	vb.tasksMu.Unlock()

	logKind := "task_complete"
	if taskErr != nil {
		logKind = "task_failed"
	}

	var note string
	if taskErr != nil {
		slog.Error("voice: delegated task failed", "task_id", pt.id, "agent", pt.agent, "err", taskErr)
		note = fmt.Sprintf(
			"Worker %q failed the task you dispatched (task_id %s).\n"+
				"Original task: %s\n"+
				"Error: %v\n\n"+
				"Tell the user the worker hit an error and what it was. Keep it brief and conversational.",
			pt.agent, pt.id, pt.task, taskErr)
	} else {
		slog.Info("voice: delegated task complete", "task_id", pt.id, "agent", pt.agent, "len", len(result))
		note = fmt.Sprintf(
			"Worker %q has finished the task you dispatched (task_id %s).\n"+
				"Original task: %s\n\n"+
				"Worker's full response follows. Read it, then surface its substance to the user "+
				"as a brief conversational summary (one or two sentences for voice; a short paragraph "+
				"if the user typed). Do NOT just acknowledge — actually convey the content. If the "+
				"user asked for a list, give them the list.\n\n"+
				"---\n%s\n---",
			pt.agent, pt.id, pt.task, result)
	}

	// Persist as a single system entry with rich metadata. The FSM
	// will dispatch the same `note` into Grok's conversation; it must
	// NOT log again or we get the duplicate-system-entry pattern that
	// 🎯T18-related transcripts revealed.
	vb.logAppend(GrokLogEntry{
		Role:    "system",
		Content: note,
		Meta: map[string]any{
			"kind":    logKind,
			"agent":   pt.agent,
			"task_id": pt.id,
			"task":    pt.task,
		},
	})

	// Broadcast a worker_note event to the chat panel so the user sees
	// the worker's output rendered distinctly (centred, dim, collapsible)
	// rather than mistaking it for an assistant turn (🎯T23). The
	// content is the raw worker output (success) or error message
	// (failure) — NOT the wrapped directive prose that Grok consumes.
	displayContent := result
	if taskErr != nil {
		displayContent = fmt.Sprintf("%v", taskErr)
	}
	vb.mu.Lock()
	ws := vb.voiceWS
	wsCtx := vb.voiceCtx
	vb.mu.Unlock()
	if ws != nil {
		vb.sendJSON(ws, wsCtx, map[string]any{
			"type":    "worker_note",
			"kind":    logKind,
			"agent":   pt.agent,
			"task_id": pt.id,
			"task":    pt.task,
			"content": displayContent,
		})
	}

	vb.mu.Lock()
	fsm := vb.fsm
	wsOpen := vb.voiceWS != nil
	vb.mu.Unlock()
	if fsm == nil {
		// No active connection — the note is preserved in the JSONL log
		// (already appended above), so on the next reconnect the
		// replay path surfaces it to Grok.
		slog.Warn("voice: completion deferred — no active session", "task_id", pt.id)
		return
	}

	modalities := grok.ModalitiesText
	if wsOpen {
		modalities = grok.ModalitiesTextAudio
	}

	// FSM owns the timing: if it's currently busy (RESPONDING /
	// COMMITTING / RECORDING), the event is queued until it returns
	// to IDLE. If it's IDLE the system note fires immediately.
	if err := fsm.Handle(voiceEvent{
		kind:       evWorkerCompletion,
		workerNote: note,
		modalities: modalities,
	}); err != nil {
		slog.Warn("voice: worker completion event rejected", "err", err)
	}
}
