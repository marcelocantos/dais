package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/claudia/grok"
)

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
}

// NewVoiceBridge creates a voice bridge with the given xAI API key.
func NewVoiceBridge(srv *Server, apiKey string) *VoiceBridge {
	return &VoiceBridge{
		srv:    srv,
		apiKey: apiKey,
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
		Voice:  "Eve",
		// Push-to-talk: the user is the VAD. Suppress Grok's server-side
		// VAD so a mid-phrase pause doesn't auto-commit half the utterance.
		// The bridge calls CommitAndRespond on the client's "commit" message.
		ManualCommit: true,
		SystemPrompt: `You are Jevon, a personal AI assistant. You are the voice
interface for a multi-agent system. When the user asks you to do
something that requires code, file operations, research, or any
substantive work, use the send_to_jevons tool to delegate it.

For simple conversational exchanges (greetings, clarifications,
opinions), respond directly without delegating.

When you receive results back from delegated work, summarise them
conversationally for the user. Be concise and natural.`,

		Tools: []grok.Tool{
			{
				Type:        "function",
				Name:        "send_to_jevons",
				Description: "Send a message to the Jevon agent system for processing. Use this for any request that requires code, file operations, research, or substantive work. The message should be a clear natural language description of what the user wants.",
				Parameters: json.RawMessage(`{
					"type": "object",
					"properties": {
						"message": {
							"type": "string",
							"description": "The user's request, rephrased as a clear instruction for the agent system"
						}
					},
					"required": ["message"]
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

		OnFunctionCall: func(name string, args json.RawMessage) (string, error) {
			if name != "send_to_jevons" {
				return `{"error":"unknown function"}`, nil
			}

			var params struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(args, &params); err != nil {
				return "", err
			}

			slog.Info("voice: delegating to jevon", "message", params.Message)

			// Send to jevon asynchronously. The response will be
			// injected back into the Grok session when it arrives
			// (via the OnJevonResponse callback wired in main.go).
			vb.srv.HandleUserMessage(params.Message)

			return `{"status":"sent","note":"The request has been sent to the agent system. The response will arrive shortly — I'll read it to you when it does."}`, nil
		},

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
