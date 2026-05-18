package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

	// resetIdle is set per-connection by HandleVoiceWS so the Grok
	// callbacks (which live longer than a local closure stack) can
	// poke the idle timer when something interesting happens.
	resetIdle func()

	// tasks tracks dispatched delegate() calls so task_status() can
	// report on them and so the completion path can find the
	// originating call metadata.
	tasksMu sync.Mutex
	tasks   map[string]*pendingTask
}

func newTaskID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "t-" + hex.EncodeToString(b[:])
}

// NewVoiceBridge creates a voice bridge with the given xAI API key.
func NewVoiceBridge(srv *Server, apiKey string) *VoiceBridge {
	return &VoiceBridge{
		srv:    srv,
		apiKey: apiKey,
		tasks:  make(map[string]*pendingTask),
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
	vb.mu.Unlock()
	if stale != nil {
		_ = stale.Close()
	}

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
			// Raw PCM audio from client — forward to Grok.
			resetIdle()
			vb.mu.Lock()
			client := vb.client
			vb.mu.Unlock()
			if client != nil {
				if !vb.audioLogged {
					slog.Info("voice: forwarding audio to grok", "bytes", len(data))
					vb.audioLogged = true
				}
				if err := client.SendAudio(grokCtx, data); err != nil {
					slog.Warn("voice: audio forward failed", "err", err)
				}
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
			// Broadcast user transcript to web UI for display only.
			// Don't send to Claude — only send_to_jevons tool calls go there.
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
