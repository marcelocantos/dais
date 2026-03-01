// Package manager provides multi-session lifecycle management,
// creating and tracking concurrent Claude Code sessions.
package manager

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/session"
)

// SessionEvent wraps a session event with its source session ID.
type SessionEvent struct {
	SessionID string
	Event     session.Event
}

// SessionSummary is a lightweight view of a session for listing.
type SessionSummary struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Status session.Status `json:"status"`
}

// CreateConfig holds parameters for creating a new session.
type CreateConfig struct {
	Name    string // human-readable name
	WorkDir string // working directory (default: ".")
	Model   string // model override (empty = manager default)
}

// Manager manages multiple concurrent Claude Code sessions.
type Manager struct {
	defaultModel string
	defaultDir   string
	db           *db.DB

	mu       sync.RWMutex
	sessions map[string]*session.Session
	nextID   int
}

// New creates a Manager with default configuration.
func New(defaultModel, defaultDir string, database *db.DB) *Manager {
	m := &Manager{
		defaultModel: defaultModel,
		defaultDir:   defaultDir,
		db:           database,
		sessions:     make(map[string]*session.Session),
	}

	// Restore workers from database.
	if workers, err := database.LoadWorkers(); err != nil {
		slog.Error("failed to load workers", "err", err)
	} else {
		for _, w := range workers {
			s := session.New(session.Config{
				ID:       w.ID,
				Name:     w.Name,
				WorkDir:  w.WorkDir,
				Model:    w.Model,
				ClaudeID: w.ClaudeID,
			})
			s.SetLastResult(w.LastResult)
			s.SetRawLog(m.rawLogFunc(w.ID))
			m.sessions[w.ID] = s

			// Track highest ID for nextID.
			var n int
			if _, err := fmt.Sscanf(w.ID, "s%d", &n); err == nil && n >= m.nextID {
				m.nextID = n
			}
		}
		if len(m.sessions) > 0 {
			slog.Info("restored workers from database", "count", len(m.sessions))
		}
	}

	return m
}

// Create creates a new session and returns it.
func (m *Manager) Create(cfg CreateConfig) *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	id := fmt.Sprintf("s%d", m.nextID)

	model := cfg.Model
	if model == "" {
		model = m.defaultModel
	}
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = m.defaultDir
	}
	name := cfg.Name
	if name == "" {
		name = fmt.Sprintf("Session %d", m.nextID)
	}

	s := session.New(session.Config{
		ID:      id,
		Name:    name,
		WorkDir: workDir,
		Model:   model,
	})
	s.SetRawLog(m.rawLogFunc(id))
	m.sessions[id] = s
	slog.Info("session created", "id", id, "name", name, "model", model)

	if err := m.db.SaveWorker(db.WorkerRow{
		ID: id, Name: name, WorkDir: workDir, Model: model,
	}); err != nil {
		slog.Error("failed to persist worker", "id", id, "err", err)
	}

	return s
}

// Get returns a session by ID, or nil if not found.
func (m *Manager) Get(id string) *session.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// List returns a summary of all sessions.
func (m *Manager) List() []SessionSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]SessionSummary, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, SessionSummary{
			ID:     s.ID(),
			Name:   s.Name(),
			Status: s.Status(),
		})
	}
	return result
}

func (m *Manager) rawLogFunc(sessionID string) session.RawLogFunc {
	return func(line []byte) {
		if err := m.db.AppendRawLog(sessionID, string(line)); err != nil {
			slog.Error("failed to persist raw log", "session", sessionID, "err", err)
		}
	}
}

// Kill stops a session and removes it from the manager.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %q not found", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	s.Stop()
	if err := m.db.DeleteWorker(id); err != nil {
		slog.Error("failed to delete worker from db", "id", id, "err", err)
	}
	slog.Info("session killed", "id", id)
	return nil
}
