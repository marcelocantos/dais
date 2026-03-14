// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package sync manages bidirectional sqlpipe replication between jevond
// and connected iOS clients. jevond runs a Master for server-owned
// tables and a Replica for the client-owned "requests" table.
package sync

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	gosync "sync"
	"time"

	"github.com/marcelocantos/sqlpipe/go/sqlpipe"

	_ "github.com/mattn/go-sqlite3"
)

// serverTables are mastered by jevond.
var serverTables = map[string]bool{
	"sync_transcript": true,
	"sessions":        true,
	"scripts":         true,
	"server_state":    true,
}

// clientTables are mastered by the iOS app.
var clientTables = map[string]bool{
	"requests": true,
}

// Request is a decoded row from the client-owned requests table.
type Request struct {
	ID        int64
	Type      string
	Payload   string
	CreatedAt string
}

// SessionData holds session info for upserting into the sessions table.
type SessionData struct {
	ID      string
	Name    string
	Status  string
	WorkDir string
	Active  bool
	Score   float64
	ModTime string
}

// SyncManager coordinates sqlpipe Master (server→client) and Replica
// (client→server) replication over a shared SQLite database.
type SyncManager struct {
	db      *sql.DB
	master  *sqlpipe.Master
	replica *sqlpipe.Replica

	masterConn  *sql.Conn
	replicaConn *sql.Conn

	mu        gosync.Mutex
	onRequest func(Request)

	// Track last-seen request ID so we only fire callback for new rows.
	lastRequestID int64
}

// NewSyncManager opens (or reuses) the database, creates the sync schema,
// and initialises a Master for server tables and a Replica for client tables.
func NewSyncManager(dbPath string, onRequest func(Request)) (*SyncManager, error) {
	// Open with session-extension flags via the DSN.
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	ctx := context.Background()

	// Acquire two dedicated connections — Master and Replica each need
	// their own because sqlpipe installs preupdate hooks per-connection.
	masterConn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("master conn: %w", err)
	}
	replicaConn, err := db.Conn(ctx)
	if err != nil {
		masterConn.Close()
		db.Close()
		return nil, fmt.Errorf("replica conn: %w", err)
	}

	sm := &SyncManager{
		db:          db,
		masterConn:  masterConn,
		replicaConn: replicaConn,
		onRequest:   onRequest,
	}

	// Seed the last-seen request ID so we don't replay old rows on startup.
	var maxID sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(id) FROM requests`).Scan(&maxID); err == nil && maxID.Valid {
		sm.lastRequestID = maxID.Int64
	}

	logCb := func(level sqlpipe.LogLevel, msg string) {
		switch level {
		case sqlpipe.LogDebug:
			slog.Debug("sqlpipe", "msg", msg)
		case sqlpipe.LogInfo:
			slog.Info("sqlpipe", "msg", msg)
		case sqlpipe.LogWarn:
			slog.Warn("sqlpipe", "msg", msg)
		default:
			slog.Error("sqlpipe", "msg", msg)
		}
	}

	master, err := sqlpipe.NewMaster(masterConn, sqlpipe.MasterConfig{
		TableFilter: &sqlpipe.TableFilter{Tables: serverTables},
		OnLog:       logCb,
	})
	if err != nil {
		replicaConn.Close()
		masterConn.Close()
		db.Close()
		return nil, fmt.Errorf("new master: %w", err)
	}
	sm.master = master

	replica, err := sqlpipe.NewReplica(replicaConn, sqlpipe.ReplicaConfig{
		TableFilter: &sqlpipe.TableFilter{Tables: clientTables},
		OnConflict: func(ct sqlpipe.ConflictType, ce sqlpipe.ChangeEvent) sqlpipe.ConflictAction {
			slog.Warn("sqlpipe replica conflict", "type", ct, "table", ce.Table)
			return sqlpipe.ConflictReplace
		},
		OnLog: logCb,
	})
	if err != nil {
		master.Close()
		replicaConn.Close()
		masterConn.Close()
		db.Close()
		return nil, fmt.Errorf("new replica: %w", err)
	}
	sm.replica = replica

	return sm, nil
}

// DB returns the underlying *sql.DB for use by other packages that
// need direct database access (e.g., the existing db.DB wrapper).
func (sm *SyncManager) DB() *sql.DB { return sm.db }

// ── Wire framing ────────────────────────────────────────────────
//
// Over the WebSocket we send binary frames containing one or more
// sqlpipe messages. Each message is prefixed by a 1-byte role tag
// (0 = from-master, 1 = from-replica) so the remote end can route
// it to the correct handler.
//
// Frame layout: [role:1][sqlpipe message bytes...]
// Multiple frames can be concatenated in a single WebSocket message.

// encodeFrame wraps a serialised sqlpipe Message with a role prefix.
func encodeFrame(role sqlpipe.SenderRole, msg sqlpipe.Message) []byte {
	wire := sqlpipe.Serialize(msg)
	out := make([]byte, 0, 1+len(wire))
	out = append(out, byte(role))
	out = append(out, wire...)
	return out
}

// encodeFrames encodes multiple messages with the same role.
func encodeFrames(role sqlpipe.SenderRole, msgs []sqlpipe.Message) []byte {
	if len(msgs) == 0 {
		return nil
	}
	var out []byte
	for _, msg := range msgs {
		out = append(out, encodeFrame(role, msg)...)
	}
	return out
}

// DecodeFrames splits a binary WebSocket payload into role-tagged messages.
func DecodeFrames(data []byte) (masterMsgs, replicaMsgs []sqlpipe.Message, err error) {
	pos := 0
	for pos < len(data) {
		if pos+5 > len(data) { // 1 role + 4 length prefix minimum
			return nil, nil, fmt.Errorf("truncated frame at offset %d", pos)
		}
		role := sqlpipe.SenderRole(data[pos])
		pos++

		// sqlpipe message: [4B LE length][tag][payload]
		msgLen := binary.LittleEndian.Uint32(data[pos:])
		total := 4 + int(msgLen)
		if pos+total > len(data) {
			return nil, nil, fmt.Errorf("truncated message at offset %d", pos)
		}
		msg, err := sqlpipe.Deserialize(data[pos : pos+total])
		if err != nil {
			return nil, nil, fmt.Errorf("deserialize at offset %d: %w", pos, err)
		}
		pos += total

		switch role {
		case sqlpipe.RoleAsMaster:
			// Remote sent as master → we handle as replica
			masterMsgs = append(masterMsgs, msg)
		case sqlpipe.RoleAsReplica:
			// Remote sent as replica → we handle as master
			replicaMsgs = append(replicaMsgs, msg)
		}
	}
	return
}

// ── Handshake ───────────────────────────────────────────────────

// Hello returns the initial handshake bytes to send to a newly connected
// client. This includes the Master's current state (flush) and the
// Replica's Hello message.
func (sm *SyncManager) Hello() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var out []byte

	// Master flush — send current state of server tables.
	masterMsgs, err := sm.master.Flush()
	if err != nil {
		return nil, fmt.Errorf("master flush: %w", err)
	}
	out = append(out, encodeFrames(sqlpipe.RoleAsMaster, masterMsgs)...)

	// Replica hello — initiate replication of client tables.
	hello, err := sm.replica.Hello()
	if err != nil {
		return nil, fmt.Errorf("replica hello: %w", err)
	}
	out = append(out, encodeFrame(sqlpipe.RoleAsReplica, hello)...)

	return out, nil
}

// ── Message handling ────────────────────────────────────────────

// HandleMessage processes an incoming binary WebSocket frame from a client.
// Returns response bytes to send back, if any.
func (sm *SyncManager) HandleMessage(data []byte) ([]byte, error) {
	masterMsgs, replicaMsgs, err := DecodeFrames(data)
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	var out []byte

	// Messages the client sent as master → feed to our Replica.
	for _, msg := range masterMsgs {
		hr, err := sm.replica.HandleMessage(msg)
		if err != nil {
			slog.Error("replica handle error", "err", err)
			continue
		}
		// Send response messages back as replica.
		out = append(out, encodeFrames(sqlpipe.RoleAsReplica, hr.Messages)...)

		// Check for new requests.
		sm.processNewRequests()
	}

	// Messages the client sent as replica → feed to our Master.
	for _, msg := range replicaMsgs {
		resp, err := sm.master.HandleMessage(msg)
		if err != nil {
			slog.Error("master handle error", "err", err)
			continue
		}
		out = append(out, encodeFrames(sqlpipe.RoleAsMaster, resp)...)
	}

	return out, nil
}

// processNewRequests scans the requests table for rows with id > lastRequestID
// and fires the onRequest callback. Must be called under sm.mu.
func (sm *SyncManager) processNewRequests() {
	if sm.onRequest == nil {
		return
	}
	rows, err := sm.db.Query(
		`SELECT id, type, payload, created_at FROM requests WHERE id > ? ORDER BY id`,
		sm.lastRequestID,
	)
	if err != nil {
		slog.Error("query new requests", "err", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.Type, &r.Payload, &r.CreatedAt); err != nil {
			slog.Error("scan request", "err", err)
			continue
		}
		sm.lastRequestID = r.ID
		// Fire callback outside the lock would be better, but for now
		// keep it simple — the callback should be non-blocking.
		sm.onRequest(r)
	}
}

// ── State writes (server-owned tables) ──────────────────────────

// Flush extracts pending Master changes and returns wire bytes to
// broadcast to all connected clients.
func (sm *SyncManager) Flush() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	msgs, err := sm.master.Flush()
	if err != nil {
		return nil, err
	}
	return encodeFrames(sqlpipe.RoleAsMaster, msgs), nil
}

// WriteTranscript inserts a message into sync_transcript and flushes.
func (sm *SyncManager) WriteTranscript(role, content string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`INSERT INTO sync_transcript (role, content) VALUES (?, ?)`,
		role, content,
	)
	if err != nil {
		return nil, fmt.Errorf("insert transcript: %w", err)
	}
	return sm.flushLocked()
}

// WriteServerState updates the server_state singleton and flushes.
func (sm *SyncManager) WriteServerState(status, streamingText string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`UPDATE server_state SET status = ?, streaming_text = ? WHERE id = 1`,
		status, streamingText,
	)
	if err != nil {
		return nil, fmt.Errorf("update server_state: %w", err)
	}
	return sm.flushLocked()
}

// AppendStreamingText appends to streaming_text (for incremental output).
func (sm *SyncManager) AppendStreamingText(text string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`UPDATE server_state SET streaming_text = streaming_text || ? WHERE id = 1`,
		text,
	)
	if err != nil {
		return nil, fmt.Errorf("append streaming_text: %w", err)
	}
	return sm.flushLocked()
}

// ClearStreamingText resets streaming_text to empty.
func (sm *SyncManager) ClearStreamingText() ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`UPDATE server_state SET streaming_text = '' WHERE id = 1`)
	if err != nil {
		return nil, fmt.Errorf("clear streaming_text: %w", err)
	}
	return sm.flushLocked()
}

// WriteSessions upserts all session rows and flushes.
func (sm *SyncManager) WriteSessions(sessions []SessionData) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, s := range sessions {
		modTime := s.ModTime
		if modTime == "" {
			modTime = now
		}
		active := 0
		if s.Active {
			active = 1
		}
		_, err := sm.db.Exec(
			`INSERT INTO sessions (id, name, status, workdir, active, score, mod_time)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
				name     = excluded.name,
				status   = excluded.status,
				workdir  = excluded.workdir,
				active   = excluded.active,
				score    = excluded.score,
				mod_time = excluded.mod_time`,
			s.ID, s.Name, s.Status, s.WorkDir, active, s.Score, modTime,
		)
		if err != nil {
			return nil, fmt.Errorf("upsert session %s: %w", s.ID, err)
		}
	}
	return sm.flushLocked()
}

// WriteScripts upserts a Lua script and flushes.
func (sm *SyncManager) WriteScripts(name, source string) ([]byte, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`INSERT INTO scripts (name, source, updated_at)
		 VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		 ON CONFLICT(name) DO UPDATE SET
			source     = excluded.source,
			updated_at = excluded.updated_at`,
		name, source,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert script %s: %w", name, err)
	}
	return sm.flushLocked()
}

// SetVersion sets the version in server_state.
func (sm *SyncManager) SetVersion(version string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, err := sm.db.Exec(
		`UPDATE server_state SET version = ? WHERE id = 1`, version)
	return err
}

// flushLocked flushes the master and returns wire bytes. Caller must hold sm.mu.
func (sm *SyncManager) flushLocked() ([]byte, error) {
	msgs, err := sm.master.Flush()
	if err != nil {
		return nil, fmt.Errorf("flush: %w", err)
	}
	return encodeFrames(sqlpipe.RoleAsMaster, msgs), nil
}

// Close releases all resources.
func (sm *SyncManager) Close() error {
	if sm.master != nil {
		sm.master.Close()
	}
	if sm.replica != nil {
		sm.replica.Close()
	}
	if sm.masterConn != nil {
		sm.masterConn.Close()
	}
	if sm.replicaConn != nil {
		sm.replicaConn.Close()
	}
	if sm.db != nil {
		return sm.db.Close()
	}
	return nil
}
