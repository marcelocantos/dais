// Package server implements the HTTP/WebSocket server for daisd,
// handling remote client connections and routing messages through
// the Jevon coordinator. Multiple clients can connect simultaneously
// and all observe the same session.
package server

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/pigeon"

	"github.com/coder/websocket"
	"github.com/marcelocantos/claudia"
	"github.com/marcelocantos/jevons/internal/auth"
	
	"github.com/marcelocantos/jevons/internal/manager"
	
)

// remoteWriter abstracts over WebSocket and tern relay connections.
type remoteWriter interface {
	WriteText(ctx context.Context, data []byte) error
	WriteBinary(ctx context.Context, data []byte) error
	Close() error
}

// wsWriter wraps a coder/websocket.Conn.
type wsWriter struct{ conn *websocket.Conn }

func (w wsWriter) WriteText(ctx context.Context, data []byte) error {
	return w.conn.Write(ctx, websocket.MessageText, data)
}
func (w wsWriter) WriteBinary(ctx context.Context, data []byte) error {
	return w.conn.Write(ctx, websocket.MessageBinary, data)
}
func (w wsWriter) Close() error { return w.conn.CloseNow() }

type remoteConn struct {
	writer remoteWriter
	ctx    context.Context
}

// Server is the daisd HTTP/WebSocket server.
type Server struct {
	mgr     *manager.Manager
	version string
	ca      *auth.CA

	mu        sync.RWMutex
	remoteSeq int
	remotes   map[int]remoteConn
	turnBuf   string // accumulates Jevon text for current turn
	waiting   bool   // true while awaiting a response from Jevon

	lanSrv         *pigeon.LANServer // LAN server for direct connections
	creds          *CredentialStore
	openAIKey      string
	voiceBridge    *VoiceBridge
	lastScreenshot string
	screenshotCh   chan string
	proc           *claudia.Agent
	registry       *claudia.Registry
	chatListeners  []chan string
}

// Credentials returns the server-side pairing credential store.
func (s *Server) Credentials() *CredentialStore { return s.creds }

// New creates a Server with the given manager and version string.
func New(mgr *manager.Manager, version string) *Server {
	s := &Server{
		mgr:           mgr,
		version:       version,
		remotes:       make(map[int]remoteConn),
		creds:         NewCredentialStore(filepath.Join(os.Getenv("HOME"), ".jevons", "credential.json")),
		chatListeners: make([]chan string, 0),
	}

	if rec, err := s.creds.Load(); err != nil {
		slog.Warn("failed to load credential", "path", s.creds.Path(), "err", err)
	} else if rec != nil {
		slog.Info("loaded pairing credential", "peer", rec.PeerInstanceID)
	}

	return s
}

// HandleAgentEvent processes a JSONL event from the Jevon agent —
// accumulates assistant text into the current turn buffer, emits
// thinking/idle status to clients, and injects completed turns into
// the Grok voice bridge for TTS. Wired via Agent.SubscribeEvents in
// the daemon entry point.
func (s *Server) HandleAgentEvent(ev claudia.Event) {
	switch ev.Type {
	case "assistant":
		if ev.Text == "" {
			return
		}
		s.mu.Lock()
		s.turnBuf += ev.Text
		s.mu.Unlock()
		s.Broadcast(map[string]any{"type": "text", "content": ev.Text})

	case "system":
		s.mu.Lock()
		wasWaiting := s.waiting
		turnText := s.turnBuf
		s.turnBuf = ""
		s.waiting = false
		s.mu.Unlock()
		if !wasWaiting {
			return
		}
		s.Broadcast(map[string]any{"type": "status", "state": "idle"})
		if turnText != "" && s.voiceBridge != nil {
			s.voiceBridge.InjectResponse(turnText)
		}
	}
}

// BroadcastBinary sends a binary WebSocket message to all connected clients.
func (s *Server) BroadcastBinary(data []byte) {
	if len(data) == 0 {
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
		if err := rc.writer.WriteBinary(writeCtx, data); err != nil {
			slog.Debug("binary broadcast write failed", "err", err)
		}
		cancel()
	}
}

// RegisterRoutes adds HTTP and WebSocket routes to the mux.
// Additional routes (e.g. MCP server) should be registered separately.
// Static file serving is handled by DevServer.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/provision", s.handleProvision)
	mux.HandleFunc("/ws/chat", s.handleChat)
	mux.HandleFunc("/ws/remote", s.handleRemote)
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("/ws/agent-terminal", s.handleAgentTerminal)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	mux.HandleFunc("POST /api/sessions/{id}/kill", s.handleKillSession)
	mux.HandleFunc("POST /api/realtime/token", s.handleRealtimeToken)
	mux.HandleFunc("/ws/voice", s.handleVoice)
}

func (s *Server) handleVoice(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	vb := s.voiceBridge
	s.mu.RUnlock()
	if vb == nil {
		// Accept the WebSocket so the client gets a clear JSON error
		// (failed upgrades don't surface error text to JavaScript).
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		data, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": "Voice not configured. Run: bin/jevonsd --set-xai-key",
		})
		conn.Write(r.Context(), websocket.MessageText, data)
		conn.Close(websocket.StatusNormalClosure, "no API key")
		return
	}
	vb.HandleVoiceWS(w, r)
}

// handleRealtimeToken proxies an ephemeral token request to the OpenAI
// Realtime API. The API key stays on the server; the client gets a
// short-lived token for direct WebSocket connection to OpenAI.
func (s *Server) handleRealtimeToken(w http.ResponseWriter, r *http.Request) {
	apiKey := s.openAIKey
	if apiKey == "" {
		http.Error(w, `{"error":"OPENAI_API_KEY not configured (set env var or store in macOS Keychain: security add-generic-password -a jevon -s openai-api-key -w YOUR_KEY)"}`, http.StatusServiceUnavailable)
		return
	}

	body := `{"model":"gpt-4o-transcribe","voice":"alloy"}`
	req, err := http.NewRequestWithContext(r.Context(), "POST",
		"https://api.openai.com/v1/realtime/sessions", strings.NewReader(body))
	if err != nil {
		http.Error(w, `{"error":"request creation failed"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("openai realtime session failed", "err", err)
		http.Error(w, `{"error":"openai request failed"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// SetOpenAIKey sets the OpenAI API key for Realtime API token proxying.
func (s *Server) SetOpenAIKey(key string) { s.openAIKey = key }

// SetVoiceBridge attaches a Grok voice bridge for the /ws/voice endpoint.
func (s *Server) SetVoiceBridge(vb *VoiceBridge) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.voiceBridge = vb
}

// VoiceBridgeRef returns the voice bridge, if configured.
func (s *Server) VoiceBridgeRef() *VoiceBridge { return s.voiceBridge }

// SetCA attaches a CA for mTLS device provisioning.
func (s *Server) SetCA(ca *auth.CA) { s.ca = ca }

// provisionRequest is the JSON body for POST /api/provision.
type provisionRequest struct {
	DeviceID  string `json:"device_id"`
	PublicKey string `json:"public_key"` // base64-encoded Ed25519 public key
}

// provisionResponse is the JSON body returned by POST /api/provision.
type provisionResponse struct {
	Certificate   string `json:"certificate"`    // PEM-encoded client cert
	CACertificate string `json:"ca_certificate"` // PEM-encoded CA cert
}

// handleProvision issues a client certificate for a new device.
// This endpoint is intentionally accessible without a client certificate —
// it is the bootstrap step for provisioning new devices.
func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	if s.ca == nil {
		http.Error(w, `{"error":"CA not configured"}`, http.StatusServiceUnavailable)
		return
	}

	var req provisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" {
		http.Error(w, `{"error":"device_id is required"}`, http.StatusBadRequest)
		return
	}
	if req.PublicKey == "" {
		http.Error(w, `{"error":"public_key is required"}`, http.StatusBadRequest)
		return
	}

	keyBytes, err := base64.StdEncoding.DecodeString(req.PublicKey)
	if err != nil {
		http.Error(w, `{"error":"public_key must be base64-encoded"}`, http.StatusBadRequest)
		return
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		http.Error(w, `{"error":"public_key must be a 32-byte Ed25519 public key"}`, http.StatusBadRequest)
		return
	}
	pubKey := ed25519.PublicKey(keyBytes)

	certPEM, err := s.ca.IssueCert(pubKey, req.DeviceID)
	if err != nil {
		slog.Error("provision: IssueCert failed", "device_id", req.DeviceID, "err", err)
		http.Error(w, `{"error":"certificate issuance failed"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("device provisioned", "device_id", req.DeviceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(provisionResponse{
		Certificate:   string(certPEM),
		CACertificate: string(s.ca.CertPEM()),
	})
}

// ClientCertMiddleware wraps an http.Handler and enforces that the client
// presented a valid certificate on all routes except those in exemptPaths.
// Use this when the TLS config has ClientAuth: VerifyClientCertIfGiven so
// that provisioning endpoints remain reachable before a cert is issued.
func ClientCertMiddleware(next http.Handler, exemptPaths ...string) http.Handler {
	exempt := make(map[string]bool, len(exemptPaths))
	for _, p := range exemptPaths {
		exempt[p] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !exempt[r.URL.Path] {
			if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
				http.Error(w, `{"error":"client certificate required"}`, http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
	s.remoteSeq++
	remoteID := s.remoteSeq
	s.remotes[remoteID] = remoteConn{writer: wsWriter{conn: conn}, ctx: ctx}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.remotes, remoteID)
		s.mu.Unlock()
	}()

	slog.Info("remote connected", "clients", len(s.remotes))

	// Send init. History is no longer carried in-memory — Claude's
	// JSONL session file at proc.JSONLPath() is the canonical record.
	s.writeJSON(conn, ctx, map[string]any{
		"type":    "init",
		"version": s.version,
	})

	// Read loop: process messages from remote.
	for {
		mt, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Info("remote disconnected", "clients", len(s.remotes)-1)
			}
			return
		}

		// Skip binary messages.
		if mt == websocket.MessageBinary {
			continue
		}

		if mt != websocket.MessageText {
			continue
		}

		var msg struct {
			Type   string `json:"type"`
			Text   string `json:"text,omitempty"`
			Action string `json:"action,omitempty"`
			Value  string `json:"value,omitempty"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "control":
			s.handleControl(conn, ctx, msg.Action, msg.Value)

		case "message":
			s.HandleUserMessage(msg.Text)

		case "action":
			s.HandleAction(msg.Action, msg.Value)
		}
	}
}

// Broadcast sends a JSON message to all connected remote clients.
func (s *Server) Broadcast(v any) {
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
		if err := rc.writer.WriteText(writeCtx, data); err != nil {
			slog.Debug("broadcast write failed", "err", err)
		}
		cancel()
	}
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	sessions := s.mgr.List(all)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := s.mgr.Get(id)
	if sess == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id":          sess.ID(),
		"name":        sess.Name(),
		"status":      sess.Status(),
		"workdir":     sess.WorkDir(),
		"last_result": sess.LastResult(),
	})
}

func (s *Server) handleKillSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.mgr.Kill(id); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// HandleUserMessage processes a text message from a remote client.
func (s *Server) HandleUserMessage(text string) {
	if text == "" {
		return
	}

	s.Broadcast(map[string]any{
		"type":      "user_message",
		"text":      text,
		"timestamp": time.Now(),
	})

	s.mu.RLock()
	proc := s.proc
	s.mu.RUnlock()
	if proc == nil {
		slog.Warn("jevons: message received before claude started")
		return
	}
	s.mu.Lock()
	s.waiting = true
	s.mu.Unlock()
	s.Broadcast(map[string]any{"type": "status", "state": "thinking"})
	if err := proc.Send(text); err != nil {
		slog.Error("jevons: send failed", "err", err)
	}
}

// HandleAction processes a UI action from a remote client or a timer callback.
func (s *Server) HandleAction(action, value string) {
	if action == "" {
		return
	}
	slog.Debug("action received", "action", action, "value", value)

	// send_message always goes through HandleUserMessage for persistence
	// and broadcast, regardless of Lua handling.
	switch {
	case action == "send_message":
		s.HandleUserMessage(value)

	case action == "show_sessions":
		s.PushSessions()

	case action == "dismiss_sheet":
		// Client handles dismiss locally.

	case action == "disconnect":
		slog.Info("disconnect requested via action")

	case len(action) > 13 && action[:13] == "kill_session:":
		sessionID := action[13:]
		if err := s.mgr.Kill(sessionID); err != nil {
			slog.Warn("kill session failed", "id", sessionID, "err", err)
		} else {
			s.PushSessions()
		}

	default:
		slog.Warn("unknown action", "action", action)
	}
}

// handleControl processes control-channel messages that bypass the Lua layer.
// These are used for safe mode operations (rollback, version query, health).
func (s *Server) handleControl(conn *websocket.Conn, ctx context.Context, action, value string) {
	respond := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn.Write(writeCtx, websocket.MessageText, data)
	}

	switch action {
	case "health":
		respond(map[string]any{
			"type":   "control",
			"action": "health",
			"status": "ok",
		})

	case "list_snapshots":
		respond(map[string]any{
			"type":   "control",
			"action": "list_snapshots",
			"error":  "sync not available",
		})


	case "screenshot":
		// Forward screenshot request to all connected clients.
		s.Broadcast(map[string]any{
			"type":   "control",
			"action": "screenshot",
		})
		// Don't respond yet — the client will send screenshot_result.

	case "screenshot_result":
		// Client sent back a screenshot as base64 PNG in the value field.
		if value == "" {
			slog.Warn("screenshot_result: no data")
			return
		}
		imgData, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			slog.Error("screenshot_result: decode failed", "err", err)
			return
		}
		path := filepath.Join(os.TempDir(), "jevons-screenshot.png")
		if err := os.WriteFile(path, imgData, 0644); err != nil {
			slog.Error("screenshot_result: write failed", "err", err)
			return
		}
		slog.Info("screenshot saved", "path", path)
		s.mu.Lock()
		s.lastScreenshot = path
		if s.screenshotCh != nil {
			select {
			case s.screenshotCh <- path:
			default:
			}
		}
		s.mu.Unlock()

	case "rollback":
		respond(map[string]any{
			"type":   "control",
			"action": "rollback",
			"error":  "sync not available",
		})

	default:
		slog.Warn("unknown control action", "action", action)
	}
}

// RequestScreenshot sends a screenshot request to connected clients and waits
// for the result. Returns the file path of the saved PNG.
func (s *Server) RequestScreenshot(timeout time.Duration) (string, error) {
	ch := make(chan string, 1)
	s.mu.Lock()
	s.screenshotCh = ch
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.screenshotCh = nil
		s.mu.Unlock()
	}()

	s.Broadcast(map[string]any{
		"type":   "control",
		"action": "screenshot",
	})

	select {
	case path := <-ch:
		return path, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("screenshot timeout")
	}
}

// PushSessions fetches the current session list and broadcasts it to all clients.
func (s *Server) PushSessions() {
	summaries := s.mgr.List(false)
	type sessionJSON struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Status  string `json:"status"`
		WorkDir string `json:"workdir"`
		Active  bool   `json:"active"`
	}
	entries := make([]sessionJSON, len(summaries))
	for i, sum := range summaries {
		entries[i] = sessionJSON{
			ID:      sum.ID,
			Name:    sum.Name,
			Status:  string(sum.Status),
			WorkDir: sum.WorkDir,
			Active:  sum.Active,
		}
	}
	s.Broadcast(map[string]any{
		"type":     "sessions",
		"sessions": entries,
	})
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
