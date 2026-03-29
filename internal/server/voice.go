package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/marcelocantos/jevon/internal/grok"
	"github.com/marcelocantos/jevon/internal/jevon"
)

// VoiceBridge manages the Grok Realtime session and bridges audio
// between a connected client and Grok. Only one voice session is
// active at a time.
type VoiceBridge struct {
	srv    *Server
	apiKey string

	mu     sync.Mutex
	client *grok.Client
	voiceWS *websocket.Conn // the connected browser/iOS client
	voiceCtx context.Context
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

	ctx := r.Context()

	vb.mu.Lock()
	vb.voiceWS = conn
	vb.voiceCtx = ctx
	vb.mu.Unlock()

	slog.Info("voice: client connected")

	// Send ready status.
	vb.sendJSON(conn, ctx, map[string]any{
		"type":   "status",
		"status": "connected",
	})

	// Auto-start the Grok session on connect.
	if err := vb.ensureGrokSession(ctx); err != nil {
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
			vb.mu.Lock()
			vb.voiceWS = nil
			vb.mu.Unlock()
			return
		}

		switch mt {
		case websocket.MessageBinary:
			// Raw PCM audio from client — forward to Grok.
			vb.mu.Lock()
			client := vb.client
			vb.mu.Unlock()
			if client != nil {
				if err := client.SendAudio(ctx, data); err != nil {
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
			case "stop":
				vb.stopGrokSession()
			case "inject":
				vb.mu.Lock()
				client := vb.client
				vb.mu.Unlock()
				if client != nil && msg.Text != "" {
					client.InjectAssistantText(ctx, msg.Text)
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

func (vb *VoiceBridge) ensureGrokSession(ctx context.Context) error {
	vb.mu.Lock()
	if vb.client != nil {
		vb.mu.Unlock()
		return nil
	}
	vb.mu.Unlock()

	slog.Info("voice: connecting to Grok Realtime")

	client, err := grok.Connect(ctx, grok.Config{
		APIKey: vb.apiKey,
		Voice:  "Eve",
		SystemPrompt: `You are Jevon, a personal AI assistant. You are the voice
interface for a multi-agent system. When the user asks you to do
something that requires code, file operations, research, or any
substantive work, use the send_to_jevon tool to delegate it.

For simple conversational exchanges (greetings, clarifications,
opinions), respond directly without delegating.

When you receive results back from delegated work, summarise them
conversationally for the user. Be concise and natural.`,

		Tools: []grok.Tool{
			{
				Type:        "function",
				Name:        "send_to_jevon",
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
			// Broadcast what Grok is saying as text too.
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

		OnUserTranscript: func(text string) {
			slog.Info("voice: user said", "text", text)
			// Broadcast user transcript to web UI.
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
			// Also record in the main transcript.
			vb.srv.HandleUserMessage(text)
		},

		OnFunctionCall: func(name string, args json.RawMessage) (string, error) {
			if name != "send_to_jevon" {
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
			vb.srv.jevon.Enqueue(jevon.Event{
				Kind: jevon.EventUserMessage,
				Text: params.Message,
			})

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

		OnError: func(err error) {
			slog.Error("voice: grok error", "err", err)
			vb.mu.Lock()
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
