// Package server implements the HTTP/WebSocket server for daisd,
// handling remote client connections and routing messages through
// the shepherd coordinator. Multiple clients can connect simultaneously
// and all observe the same session.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/shepherd"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role      string    `json:"role"`      // "user" or "shepherd"
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type remoteConn struct {
	conn *websocket.Conn
	ctx  context.Context
}

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	shepherd *shepherd.Shepherd
	db       *db.DB
	version  string

	mu         sync.RWMutex
	remotes    map[*websocket.Conn]remoteConn
	transcript []TranscriptEntry
	turnBuf    string // accumulates shepherd text for current turn
}

// New creates a Server with the given shepherd, database, and version string.
// It loads any existing transcript from the database and wires shepherd
// callbacks for broadcasting to all connected clients.
func New(shep *shepherd.Shepherd, database *db.DB, version string) *Server {
	s := &Server{
		shepherd: shep,
		db:       database,
		version:  version,
		remotes:  make(map[*websocket.Conn]remoteConn),
	}

	// Load persisted transcript.
	if entries, err := database.LoadTranscript(); err != nil {
		slog.Error("failed to load transcript", "err", err)
	} else {
		for _, e := range entries {
			s.transcript = append(s.transcript, TranscriptEntry{
				Role:      e.Role,
				Text:      e.Text,
				Timestamp: e.CreatedAt,
			})
		}
		if len(s.transcript) > 0 {
			slog.Info("loaded transcript from database", "entries", len(s.transcript))
		}
	}

	// Wire shepherd callbacks once — they broadcast to all connected clients.
	shep.SetRawLog(func(line []byte) {
		if err := s.db.AppendRawLog("shepherd", string(line)); err != nil {
			slog.Error("failed to persist raw log", "err", err)
		}
	})
	shep.SetOutput(func(text string) {
		s.mu.Lock()
		s.turnBuf += text
		s.mu.Unlock()

		s.broadcast(map[string]any{
			"type":    "text",
			"content": text,
		})
	})
	shep.SetStatus(func(state string) {
		if state == "idle" {
			s.mu.Lock()
			turnText := s.turnBuf
			if turnText != "" {
				s.transcript = append(s.transcript, TranscriptEntry{
					Role:      "shepherd",
					Text:      turnText,
					Timestamp: time.Now(),
				})
				s.turnBuf = ""
			}
			s.mu.Unlock()

			if turnText != "" {
				if err := s.db.AppendTranscript("shepherd", turnText); err != nil {
					slog.Error("failed to persist shepherd turn", "err", err)
				}
			}
		}

		s.broadcast(map[string]any{
			"type":  "status",
			"state": state,
		})
	})

	return s
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

	ctx := r.Context()

	// Register this connection.
	s.mu.Lock()
	s.remotes[conn] = remoteConn{conn: conn, ctx: ctx}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.remotes, conn)
		s.mu.Unlock()
	}()

	slog.Info("remote connected", "clients", len(s.remotes))

	// Send init message.
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Send transcript history.
	s.mu.RLock()
	hist := make([]TranscriptEntry, len(s.transcript))
	copy(hist, s.transcript)
	s.mu.RUnlock()

	if len(hist) > 0 {
		s.writeJSON(conn, ctx, map[string]any{
			"type":    "history",
			"entries": hist,
		})
	}

	// Read loop: process messages from remote.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected", "clients", len(s.remotes)-1)
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
				s.mu.Lock()
				now := time.Now()
				s.transcript = append(s.transcript, TranscriptEntry{
					Role:      "user",
					Text:      msg.Text,
					Timestamp: now,
				})
				s.mu.Unlock()

				if err := s.db.AppendTranscript("user", msg.Text); err != nil {
					slog.Error("failed to persist user message", "err", err)
				}

				s.broadcast(map[string]any{
					"type":      "user_message",
					"text":      msg.Text,
					"timestamp": now,
				})

				s.shepherd.Enqueue(shepherd.Event{
					Kind: shepherd.EventUserMessage,
					Text: msg.Text,
				})
			}
		}
	}
}

// broadcast sends a JSON message to all connected remote clients.
func (s *Server) broadcast(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("marshal failed", "err", err)
		return
	}

	s.mu.RLock()
	remotes := make([]remoteConn, 0, len(s.remotes))
	for _, rc := range s.remotes {
		remotes = append(remotes, rc)
	}
	s.mu.RUnlock()

	for _, rc := range remotes {
		writeCtx, cancel := context.WithTimeout(rc.ctx, 5*time.Second)
		if err := rc.conn.Write(writeCtx, websocket.MessageText, data); err != nil {
			slog.Debug("broadcast write failed", "err", err)
		}
		cancel()
	}
}

// writeJSON sends a JSON message to a single connection.
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
