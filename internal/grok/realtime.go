// Package grok implements a client for the Grok Realtime voice API.
// It connects to wss://api.x.ai/v1/realtime and bridges full-duplex
// voice I/O with function calling for agent delegation.
package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Tool defines a function tool available to the Grok model.
// For custom functions: Type="function", Name, Description, Parameters.
type Tool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Config holds configuration for a Grok Realtime session.
type Config struct {
	APIKey string

	// OnAudio is called with base64-decoded PCM audio from Grok.
	OnAudio func(pcm []byte)

	// OnTranscript is called with text the model speaks.
	OnTranscript func(text string)

	// OnUserTranscript is called with transcribed user speech.
	OnUserTranscript func(text string)

	// OnFunctionCall is called when Grok invokes a tool.
	// The handler must return the result string.
	OnFunctionCall func(name string, args json.RawMessage) (string, error)

	// OnSessionReady is called when the session is configured.
	OnSessionReady func()

	// OnError is called on protocol errors.
	OnError func(err error)

	// Voice for TTS output. Default: "eve".
	Voice string

	// Tools available to the model.
	Tools []Tool

	// SystemPrompt for the session.
	SystemPrompt string
}

// Client manages a Grok Realtime WebSocket session.
type Client struct {
	cfg  Config
	conn *websocket.Conn

	mu     sync.Mutex
	closed bool

	// pendingCalls tracks function calls awaiting results.
	pendingCalls map[string]bool
}

// Connect establishes a Grok Realtime session.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("grok: API key required")
	}
	if cfg.Voice == "" {
		cfg.Voice = "Eve"
	}

	conn, _, err := websocket.Dial(ctx, "wss://api.x.ai/v1/realtime", &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Authorization": {"Bearer " + cfg.APIKey},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("grok: dial failed: %w", err)
	}

	// Allow large messages (audio chunks can be big).
	conn.SetReadLimit(4 << 20) // 4 MB

	c := &Client{
		cfg:          cfg,
		conn:         conn,
		pendingCalls: make(map[string]bool),
	}

	// Configure the session.
	if err := c.configureSession(ctx); err != nil {
		conn.CloseNow()
		return nil, fmt.Errorf("grok: session config failed: %w", err)
	}

	// Start read loop.
	go c.readLoop(ctx)

	return c, nil
}

// SendAudio sends PCM audio data to Grok. The data should be raw PCM
// bytes (not base64 encoded) — this method handles the encoding.
func (c *Client) SendAudio(ctx context.Context, pcm []byte) error {
	encoded := base64.StdEncoding.EncodeToString(pcm)
	return c.send(ctx, map[string]any{
		"type":  "input_audio_buffer.append",
		"audio": encoded,
	})
}

// SendText sends a text message into the conversation (e.g. injecting
// an agent response for Grok to speak).
func (c *Client) SendText(ctx context.Context, text string) error {
	// Add as a conversation item, then request a response.
	if err := c.send(ctx, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		},
	}); err != nil {
		return err
	}
	return c.send(ctx, map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"modalities": []string{"text", "audio"},
		},
	})
}

// InjectAssistantText injects text as if the assistant said it, then
// requests Grok to speak it. Used for relaying Claude's responses.
func (c *Client) InjectAssistantText(ctx context.Context, text string) error {
	// Add as assistant message so it's in context, then request
	// audio generation for it.
	if err := c.send(ctx, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{
				{"type": "text", "text": text},
			},
		},
	}); err != nil {
		return err
	}

	// Request the model to generate audio for what it should say next.
	// We give it a nudge to speak the injected content.
	return c.send(ctx, map[string]any{
		"type": "response.create",
		"response": map[string]any{
			"modalities": []string{"audio"},
			"instructions": "Read the last assistant message aloud naturally. " +
				"Do not add commentary or change the content — just speak it.",
		},
	})
}

// Close terminates the session.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.conn.Close(websocket.StatusNormalClosure, "bye")
}

func (c *Client) configureSession(ctx context.Context) error {
	session := map[string]any{
		"voice": c.cfg.Voice,
		"turn_detection": map[string]any{
			"type":                "server_vad",
			"threshold":           0.7,
			"silence_duration_ms": 800,
			"prefix_padding_ms":   300,
		},
		"audio": map[string]any{
			"input": map[string]any{
				"format": map[string]any{
					"type": "audio/pcm",
					"rate": 24000,
				},
			},
			"output": map[string]any{
				"format": map[string]any{
					"type": "audio/pcm",
					"rate": 24000,
				},
			},
		},
	}

	if c.cfg.SystemPrompt != "" {
		session["instructions"] = c.cfg.SystemPrompt
	}

	if len(c.cfg.Tools) > 0 {
		session["tools"] = c.cfg.Tools
	}

	return c.send(ctx, map[string]any{
		"type":    "session.update",
		"session": session,
	})
}

func (c *Client) readLoop(ctx context.Context) {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				slog.Error("grok: read error", "err", err)
				if c.cfg.OnError != nil {
					c.cfg.OnError(err)
				}
			}
			return
		}

		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("grok: invalid JSON", "err", err)
			continue
		}

		c.handleEvent(ctx, msg)
	}
}

func (c *Client) handleEvent(ctx context.Context, msg map[string]any) {
	eventType, _ := msg["type"].(string)

	switch eventType {
	case "session.updated":
		slog.Info("grok: session configured")
		if c.cfg.OnSessionReady != nil {
			c.cfg.OnSessionReady()
		}

	case "response.audio.delta":
		// Audio output from Grok — decode and forward.
		if delta, ok := msg["delta"].(string); ok && c.cfg.OnAudio != nil {
			pcm, err := base64.StdEncoding.DecodeString(delta)
			if err != nil {
				slog.Warn("grok: audio decode failed", "err", err)
				return
			}
			c.cfg.OnAudio(pcm)
		}

	case "response.audio_transcript.delta":
		// What the model is saying (text).
		if delta, ok := msg["delta"].(string); ok && c.cfg.OnTranscript != nil {
			c.cfg.OnTranscript(delta)
		}

	case "conversation.item.input_audio_transcription.completed":
		// What the user said.
		if transcript, ok := msg["transcript"].(string); ok && c.cfg.OnUserTranscript != nil {
			c.cfg.OnUserTranscript(transcript)
		}

	case "response.function_call_arguments.done":
		// Grok wants to call a tool.
		go c.handleFunctionCall(ctx, msg)

	case "input_audio_buffer.speech_started":
		slog.Debug("grok: speech started")

	case "input_audio_buffer.speech_stopped":
		slog.Debug("grok: speech stopped")

	case "error":
		errMsg, _ := msg["error"].(map[string]any)
		errText, _ := errMsg["message"].(string)
		slog.Error("grok: server error", "error", errText)
		if c.cfg.OnError != nil {
			c.cfg.OnError(fmt.Errorf("grok server: %s", errText))
		}

	case "response.done":
		slog.Debug("grok: response complete")

	default:
		slog.Debug("grok: event", "type", eventType)
	}
}

func (c *Client) handleFunctionCall(ctx context.Context, msg map[string]any) {
	callID, _ := msg["call_id"].(string)
	name, _ := msg["name"].(string)
	argsStr, _ := msg["arguments"].(string)

	slog.Info("grok: function call", "name", name, "call_id", callID)

	if c.cfg.OnFunctionCall == nil {
		c.sendFunctionResult(ctx, callID, `{"error":"no handler"}`)
		return
	}

	result, err := c.cfg.OnFunctionCall(name, json.RawMessage(argsStr))
	if err != nil {
		slog.Error("grok: function call failed", "name", name, "err", err)
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		c.sendFunctionResult(ctx, callID, string(errJSON))
		return
	}

	c.sendFunctionResult(ctx, callID, result)
}

func (c *Client) sendFunctionResult(ctx context.Context, callID, output string) {
	// Send the function result.
	if err := c.send(ctx, map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  output,
		},
	}); err != nil {
		slog.Error("grok: failed to send function result", "err", err)
		return
	}

	// Request continuation.
	if err := c.send(ctx, map[string]any{
		"type": "response.create",
	}); err != nil {
		slog.Error("grok: failed to request continuation", "err", err)
	}
}

func (c *Client) send(ctx context.Context, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("grok: marshal failed: %w", err)
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return c.conn.Write(writeCtx, websocket.MessageText, data)
}
