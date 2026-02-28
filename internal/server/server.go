// Package server implements the HTTP/WebSocket server for daisd,
// handling the remote client connection and routing messages through
// the shepherd coordinator.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/dais/internal/shepherd"
)

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	shepherd *shepherd.Shepherd
	version  string

	mu     sync.Mutex
	remote *websocket.Conn
}

// New creates a Server with the given shepherd and version string.
func New(shep *shepherd.Shepherd, version string) *Server {
	return &Server{
		shepherd: shep,
		version:  version,
	}
}

// RegisterRoutes adds HTTP and WebSocket routes to the mux.
// Additional routes (e.g. ctlapi) should be registered separately.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("/ws/remote", s.handleRemote)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleRemote(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		slog.Error("remote accept failed", "err", err)
		return
	}
	defer conn.CloseNow()

	conn.SetReadLimit(1 << 20) // 1 MB

	s.mu.Lock()
	if s.remote != nil {
		s.remote.Close(websocket.StatusGoingAway, "replaced by new connection")
	}
	s.remote = conn
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.remote == conn {
			s.remote = nil
		}
		s.mu.Unlock()
	}()

	slog.Info("remote connected")

	// Wire shepherd output to this connection.
	ctx := r.Context()
	s.shepherd.SetOutput(func(text string) {
		s.writeJSON(conn, ctx, map[string]any{
			"type":    "text",
			"content": text,
		})
	})
	s.shepherd.SetStatus(func(state string) {
		s.writeJSON(conn, ctx, map[string]any{
			"type":  "status",
			"state": state,
		})
	})

	// Send init message.
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Read loop: process messages from remote.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected")
			}
			return
		}
		if mt != websocket.MessageText {
			continue
		}

		var msg struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "message":
			if msg.Text != "" {
				s.shepherd.Enqueue(shepherd.Event{
					Kind: shepherd.EventUserMessage,
					Text: msg.Text,
				})
			}
		}
	}
}

func (s *Server) writeJSON(conn *websocket.Conn, ctx context.Context, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal failed", "err", err)
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, data); err != nil {
		slog.Debug("write failed", "err", err)
	}
}
