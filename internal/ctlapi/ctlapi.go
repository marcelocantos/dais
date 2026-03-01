// Package ctlapi provides REST endpoints for worker session management,
// used by the dais-ctl helper binary that the shepherd calls via Bash.
package ctlapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/marcelocantos/dais/internal/db"
	"github.com/marcelocantos/dais/internal/manager"
	"github.com/marcelocantos/dais/internal/session"
)

// EventCallback is called when a worker finishes a command.
type EventCallback func(workerID, workerName, result string, failed bool)

// Handler provides REST endpoints for dais-ctl.
type Handler struct {
	mgr      *manager.Manager
	db       *db.DB
	onDone   EventCallback
	workerWD string // default working directory for workers
}

// New creates a Handler with the given manager, database, and callback.
func New(mgr *manager.Manager, database *db.DB, workerWD string, onDone EventCallback) *Handler {
	return &Handler{
		mgr:      mgr,
		db:       database,
		onDone:   onDone,
		workerWD: workerWD,
	}
}

// RegisterRoutes adds ctl API routes to the mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /ctl/workers", h.createWorker)
	mux.HandleFunc("GET /ctl/workers", h.listWorkers)
	mux.HandleFunc("GET /ctl/workers/{id}", h.getWorker)
	mux.HandleFunc("POST /ctl/workers/{id}/command", h.sendCommand)
	mux.HandleFunc("DELETE /ctl/workers/{id}", h.killWorker)
}

func (h *Handler) createWorker(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		WorkDir string `json:"workdir"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	workDir := body.WorkDir
	if workDir == "" {
		workDir = h.workerWD
	}

	sess := h.mgr.Create(manager.CreateConfig{
		Name:    body.Name,
		WorkDir: workDir,
		Model:   body.Model,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":   sess.ID(),
		"name": sess.Name(),
	})
}

func (h *Handler) listWorkers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.mgr.List())
}

type workerDetail struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Status     session.Status `json:"status"`
	LastResult string         `json:"last_result,omitempty"`
}

func (h *Handler) getWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := h.mgr.Get(id)
	if sess == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(workerDetail{
		ID:         sess.ID(),
		Name:       sess.Name(),
		Status:     sess.Status(),
		LastResult: sess.LastResult(),
	})
}

func (h *Handler) sendCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := h.mgr.Get(id)
	if sess == nil {
		http.Error(w, "worker not found", http.StatusNotFound)
		return
	}

	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Run asynchronously; notify shepherd when done.
	go h.runAndNotify(id, sess, body.Text)

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"status":    "accepted",
		"worker_id": id,
	})
}

func (h *Handler) runAndNotify(id string, sess *session.Session, text string) {
	events, err := sess.Run(context.Background(), text)
	if err != nil {
		slog.Error("worker run failed", "worker", id, "err", err)
		if h.onDone != nil {
			h.onDone(id, sess.Name(), err.Error(), true)
		}
		return
	}

	// Collect text output for the result summary.
	var textParts []string
	for ev := range events {
		if ev.Type == session.EventText {
			textParts = append(textParts, ev.Content)
		}
	}

	result := sess.LastResult()
	if result == "" {
		result = strings.Join(textParts, "")
	}

	// Truncate long results.
	const maxResult = 2000
	if len(result) > maxResult {
		result = result[:maxResult] + "\n... (truncated)"
	}

	// Persist updated worker state (claudeID, lastResult).
	if err := h.db.SaveWorker(db.WorkerRow{
		ID:         id,
		Name:       sess.Name(),
		ClaudeID:   sess.ClaudeID(),
		LastResult: result,
	}); err != nil {
		slog.Error("failed to persist worker state", "worker", id, "err", err)
	}

	failed := strings.HasPrefix(result, "error: ")
	if h.onDone != nil {
		h.onDone(id, sess.Name(), result, failed)
	}
}

func (h *Handler) killWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.mgr.Kill(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "killed"})
}
