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
	"strings"
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
// active at a time.
type VoiceBridge struct {
	srv    *Server
	apiKey string

	mu          sync.Mutex
	client      *grok.Client
	voiceWS     *websocket.Conn // the connected browser/iOS client
	voiceCtx    context.Context
	audioLogged bool

	// grokReady is set in OnSessionReady once session.updated has been
	// acknowledged by xAI. Audio frames received before this flag is
	// true would otherwise be processed under the API's default
	// turn_detection (server VAD), defeating our ManualCommit config
	// and causing Grok to auto-respond to noise before the user has
	// even said anything.
	grokReady bool

	// pendingAudio holds frames that arrive before grokReady — flushed
	// (in receive order) from OnSessionReady so the first ~1.2s of the
	// user's first utterance after a fresh session isn't lost to the
	// handshake window.
	pendingAudio [][]byte

	// resetIdle is set per-connection by HandleVoiceWS so the Grok
	// callbacks (which live longer than a local closure stack) can
	// poke the idle timer when something interesting happens.
	resetIdle func()

	// tasks tracks dispatched delegate() calls so task_status() can
	// report on them and so the completion path can find the
	// originating call metadata.
	tasksMu sync.Mutex
	tasks   map[string]*pendingTask

	// log persists the Grok conversation across sessions so context
	// survives idle timeouts, server restarts, and page reloads. Each
	// new Grok session replays the recent tail before user audio starts
	// flowing so the model picks up where the previous session left off.
	log         *GrokLog
	assistantMu sync.Mutex
	assistantBuf strings.Builder // accumulates response.output_audio_transcript.delta
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

// HandleVoiceWS handles /ws/voice connections. The protocol:
//   - Binary frames: raw 24kHz mono PCM16 audio (both directions)
//   - Text frames: JSON control messages
//     - {"type":"start"} — begin/resume voice session
//     - {"type":"stop"} — end voice session
//     - {"type":"inject","text":"..."} — inject text for Grok to speak
func (vb *VoiceBridge) HandleVoiceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("voice: accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20) // 1 MB

	// The browser's context cancels the moment the request handler returns
	// (i.e. the WS is closed). Grok needs a longer-lived context so we can
	// run a final Commit on PTT release before tearing down.
	grokCtx, grokCancel := context.WithCancel(context.Background())
	defer grokCancel()

	ctx := r.Context()

	vb.mu.Lock()
	// Defensive: a previous session may have leaked (Grok died but the
	// bridge never cleared the pointer). Tear it down before opening a new one.
	stale := vb.client
	vb.client = nil
	vb.voiceWS = conn
	vb.voiceCtx = ctx
	vb.audioLogged = false
	vb.grokReady = false // re-armed in OnSessionReady once session.updated arrives
	vb.pendingAudio = nil
	vb.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}

	// Per-connection audio frame counter for periodic logging. Lets us
	// confirm audio is actually flowing across turn boundaries — the old
	// "first frame only" log was useless after the first utterance.
	audioFrameCount := 0
	_ = audioFrameCount // silence unused before first use in loop below

	// Idle timer: defensively close the WS after voiceIdleTimeout of no
	// audio frames, commits, or response.done events. Guards against an
	// abandoned tab holding a session open.
	idleMu := sync.Mutex{}
	idleTimer := time.AfterFunc(voiceIdleTimeout, func() {
		slog.Info("voice: idle timeout, closing")
		_ = conn.Close(websocket.StatusNormalClosure, "idle timeout")
	})
	defer idleTimer.Stop()
	resetIdle := func() {
		idleMu.Lock()
		idleTimer.Reset(voiceIdleTimeout)
		idleMu.Unlock()
	}

	vb.mu.Lock()
	vb.resetIdle = resetIdle
	vb.mu.Unlock()
	defer func() {
		vb.mu.Lock()
		vb.resetIdle = nil
		vb.mu.Unlock()
	}()

	// Per-connection cleanup: best-effort commit so trailing audio gets
	// transcribed even if the client closed without an explicit stop,
	// then close the Grok session. Always clears vb.client so the next
	// client connection starts from a clean slate.
	defer func() {
		vb.mu.Lock()
		if vb.voiceWS == conn {
			vb.voiceWS = nil
		}
		client := vb.client
		vb.client = nil
		vb.mu.Unlock()
		if client != nil {
			_ = client.Commit(grokCtx)
			_ = client.Close()
			slog.Info("voice: Grok session closed")
		}
	}()

	slog.Info("voice: client connected")

	// Send ready status.
	vb.sendJSON(conn, ctx, map[string]any{
		"type":   "status",
		"status": "connected",
	})

	// Open a fresh Grok session for this connection.
	if err := vb.startGrokSession(grokCtx); err != nil {
		slog.Error("voice: grok session failed", "err", err)
		vb.sendJSON(conn, ctx, map[string]any{
			"type":  "error",
			"error": err.Error(),
		})
		return
	}

	// Read loop.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			slog.Info("voice: client disconnected")
			return
		}

		switch mt {
		case websocket.MessageBinary:
			// Raw PCM audio from client. If the Grok session isn't
			// fully configured yet (turn_detection still on xAI's
			// default), buffer the frame instead of forwarding —
			// OnSessionReady flushes the backlog so the first second
			// of the user's first utterance isn't lost to handshake.
			resetIdle()
			audioFrameCount++
			logThisFrame := audioFrameCount == 1 || audioFrameCount%50 == 0
			vb.mu.Lock()
			if !vb.grokReady {
				// Copy because the WS read buffer is reused.
				vb.pendingAudio = append(vb.pendingAudio, append([]byte(nil), data...))
				bufLen := len(vb.pendingAudio)
				vb.mu.Unlock()
				if logThisFrame {
					slog.Info("voice: buffering audio — Grok not ready yet",
						"frame", audioFrameCount, "queued", bufLen)
				}
				continue
			}
			client := vb.client
			vb.mu.Unlock()
			if client != nil {
				if logThisFrame {
					slog.Info("voice: forwarding audio to grok",
						"frame", audioFrameCount, "bytes", len(data))
				}
				if err := client.SendAudio(grokCtx, data); err != nil {
					slog.Warn("voice: audio forward failed", "err", err)
				}
			} else if logThisFrame {
				slog.Warn("voice: dropping audio — no Grok client",
					"frame", audioFrameCount, "bytes", len(data))
			}

		case websocket.MessageText:
			var msg struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "commit":
				// End-of-utterance signal from PTT release. Commit the audio
				// buffer and request a response, keeping the Grok session
				// alive for the next utterance.
				resetIdle()
				vb.mu.Lock()
				client := vb.client
				vb.mu.Unlock()
				if client != nil {
					if err := client.CommitAndRespond(grokCtx); err != nil {
						slog.Warn("voice: commit failed", "err", err)
					}
				}
			case "stop":
				// Legacy end-of-session signal: commit and tear down.
				vb.mu.Lock()
				client := vb.client
				vb.mu.Unlock()
				if client != nil {
					if err := client.Commit(grokCtx); err != nil {
						slog.Debug("voice: commit failed", "err", err)
					}
				}
				vb.stopGrokSession()
			case "inject":
				vb.mu.Lock()
				client := vb.client
				vb.mu.Unlock()
				if client != nil && msg.Text != "" {
					client.InjectAssistantText(grokCtx, msg.Text)
				}
			}
		}
	}
}

// InjectResponse sends a Claude response into the Grok session to be
// spoken aloud. Called from the jevon response callback.
func (vb *VoiceBridge) InjectResponse(text string) {
	vb.mu.Lock()
	client := vb.client
	ctx := vb.voiceCtx
	vb.mu.Unlock()

	if client == nil || ctx == nil {
		return
	}

	if err := client.InjectAssistantText(ctx, text); err != nil {
		slog.Error("voice: inject response failed", "err", err)
	}
}

func (vb *VoiceBridge) startGrokSession(ctx context.Context) error {
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
			// Forward Grok's audio to the connected client.
			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			vb.mu.Unlock()
			if ws != nil && wsCtx != nil {
				if err := ws.Write(wsCtx, websocket.MessageBinary, pcm); err != nil {
					slog.Debug("voice: client audio write failed", "err", err)
				}
			}
		},

		OnTranscript: func(text string) {
			// Accumulate the delta — flushed to the log at OnTranscriptDone
			// so a single assistant turn becomes one JSONL entry.
			vb.assistantMu.Lock()
			vb.assistantBuf.WriteString(text)
			vb.assistantMu.Unlock()

			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			vb.mu.Unlock()
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type": "assistant_transcript",
					"text": text,
				})
			}
		},

		OnTranscriptDone: func() {
			vb.assistantMu.Lock()
			turnText := vb.assistantBuf.String()
			vb.assistantBuf.Reset()
			vb.assistantMu.Unlock()
			if turnText != "" {
				vb.logAppend(GrokLogEntry{Role: "assistant", Content: turnText})
			}

			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			vb.mu.Unlock()
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type": "assistant_transcript_done",
				})
			}
		},

		OnUserTranscript: func(text string) {
			slog.Info("voice: user said", "text", text)
			vb.logAppend(GrokLogEntry{
				Role:    "user",
				Content: text,
				Meta:    map[string]any{"modality": "voice"},
			})
			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			vb.mu.Unlock()
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type": "user_transcript",
					"text": text,
				})
			}
		},

		OnFunctionCall: vb.handleFunctionCall,

		OnSessionReady: func() {
			slog.Info("voice: Grok session ready")
			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			client := vb.client
			vb.mu.Unlock()
			// Replay history into the freshly-configured session BEFORE
			// the user audio buffer is committed, so the conversation
			// order is: history items → new user audio → response.
			if client != nil {
				vb.replayLog(ctx, client)
			}
			// Flush any audio that arrived during the ~1.2s session
			// handshake. Hold the lock across the flush + grokReady
			// flip so concurrent read-loop frames can't sneak in
			// out of order: they'll block on the lock, observe
			// grokReady=true after the flush, and forward their frame
			// behind the backlog.
			vb.mu.Lock()
			backlog := vb.pendingAudio
			vb.pendingAudio = nil
			if len(backlog) > 0 && client != nil {
				slog.Info("voice: flushing buffered audio", "frames", len(backlog))
				for _, chunk := range backlog {
					if err := client.SendAudio(ctx, chunk); err != nil {
						slog.Warn("voice: backlog forward failed", "err", err)
						break
					}
				}
			}
			vb.grokReady = true
			vb.mu.Unlock()
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type":   "status",
					"status": "ready",
				})
			}
		},

		OnResponseDone: func() {
			slog.Debug("voice: Grok response done")
			vb.mu.Lock()
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			reset := vb.resetIdle
			vb.mu.Unlock()
			if reset != nil {
				// Restart the idle window from "ready for next utterance",
				// not from end-of-user-audio.
				reset()
			}
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type":   "status",
					"status": "ready",
				})
			}
		},

		OnError: func(err error) {
			slog.Error("voice: grok error", "err", err)
			// Grok's read loop has terminated; the client is unusable.
			// Clear it so the audio-forward path stops shipping to a corpse
			// and the next /ws/voice connection opens a fresh session.
			vb.mu.Lock()
			vb.client = nil
			ws := vb.voiceWS
			wsCtx := vb.voiceCtx
			vb.mu.Unlock()
			if ws != nil {
				vb.sendJSON(ws, wsCtx, map[string]any{
					"type":  "error",
					"error": err.Error(),
				})
			}
		},
	})
	if err != nil {
		return err
	}

	vb.mu.Lock()
	vb.client = client
	vb.mu.Unlock()
	// Replay happens from OnSessionReady once session.updated has been
	// acknowledged — until then audio is dropped and any item-create
	// would race the session config.
	return nil
}

func (vb *VoiceBridge) stopGrokSession() {
	vb.mu.Lock()
	client := vb.client
	vb.client = nil
	vb.mu.Unlock()

	if client != nil {
		client.Close()
		slog.Info("voice: Grok session closed")
	}
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

	// Persist the worker's full output to the log as a system entry.
	// The chat panel renders this collapsed-by-default with the worker
	// name as a header (UI work lands in 1d); Grok sees it as part of
	// its conversation context on replay.
	logRole := "system"
	logKind := "task_complete"
	logContent := result
	if taskErr != nil {
		logKind = "task_failed"
		logContent = fmt.Sprintf("error: %v", taskErr)
	}
	vb.logAppend(GrokLogEntry{
		Role:    logRole,
		Content: logContent,
		Meta: map[string]any{
			"kind":     logKind,
			"agent":    pt.agent,
			"task_id":  pt.id,
			"task":     pt.task,
		},
	})

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

	vb.mu.Lock()
	client := vb.client
	wsOpen := vb.voiceWS != nil
	vb.mu.Unlock()
	if client == nil {
		// Grok session was torn down (idle timeout, error). The note
		// is lost for this run; future JSONL persistence (1c) will
		// replay it on next reconnect.
		slog.Warn("voice: completion note dropped — no Grok session", "task_id", pt.id)
		return
	}

	// Modality: if the voice WS is still attached, the user may want
	// to hear the response. Otherwise (text-only client, or none),
	// reply in text only — Grok still produces a transcript that can
	// land in the chat panel once 1c lands.
	modalities := grok.ModalitiesText
	if wsOpen {
		modalities = grok.ModalitiesTextAudio
	}

	if err := client.SendSystemNote(context.Background(), note, modalities); err != nil {
		slog.Error("voice: failed to inject completion note", "task_id", pt.id, "err", err)
	}
}
