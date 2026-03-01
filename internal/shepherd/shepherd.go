package shepherd

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/marcelocantos/dais/internal/session"
)

// Config holds shepherd configuration.
type Config struct {
	WorkDir  string // working directory (contains CLAUDE.md)
	Model    string // model for shepherd (e.g. "opus", "sonnet")
	CtlAddr  string // address of daisd's ctl API (e.g. "http://localhost:8080")
	CtlBin   string // path to dais-ctl binary
	ClaudeID string // restored claude session ID for --resume
}

// OutputFunc receives text from the shepherd to stream to the user.
type OutputFunc func(text string)

// StatusFunc receives shepherd status changes.
type StatusFunc func(state string)

// RawLogFunc receives raw NDJSON lines from the Claude process.
type RawLogFunc func(line []byte)

// Shepherd coordinates between the user and Claude Code workers.
type Shepherd struct {
	cfg        Config
	onOutput   OutputFunc
	onStatus   StatusFunc
	onRawLog   RawLogFunc
	onClaudeID ClaudeIDFunc

	mu       sync.Mutex
	queue    []Event
	notify   chan struct{}
	claudeID string // claude session ID for --resume
	running  bool
}

// ClaudeIDFunc is called when the shepherd's claude session ID changes.
type ClaudeIDFunc func(id string)

// New creates a Shepherd with the given config.
func New(cfg Config) *Shepherd {
	return &Shepherd{
		cfg:      cfg,
		claudeID: cfg.ClaudeID,
		notify:   make(chan struct{}, 1),
	}
}

// SetOutput sets the callback for shepherd text output.
func (s *Shepherd) SetOutput(fn OutputFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onOutput = fn
}

// SetStatus sets the callback for shepherd status changes.
func (s *Shepherd) SetStatus(fn StatusFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onStatus = fn
}

// SetRawLog sets the callback for raw NDJSON lines from the Claude process.
func (s *Shepherd) SetRawLog(fn RawLogFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRawLog = fn
}

// SetClaudeIDCallback sets the callback for when the shepherd's claude
// session ID is first captured.
func (s *Shepherd) SetClaudeIDCallback(fn ClaudeIDFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onClaudeID = fn
}

// Enqueue adds an event to the shepherd's queue. If the shepherd is idle,
// it will be woken up to process the event.
func (s *Shepherd) Enqueue(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	s.mu.Lock()
	s.queue = append(s.queue, ev)
	s.mu.Unlock()

	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Run starts the shepherd event loop. It blocks until ctx is cancelled.
func (s *Shepherd) Run(ctx context.Context) {
	slog.Info("shepherd started", "workdir", s.cfg.WorkDir)

	for {
		select {
		case <-ctx.Done():
			slog.Info("shepherd stopped")
			return
		case <-s.notify:
		}

		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			continue
		}
		batch := s.queue
		s.queue = nil
		s.running = true
		s.mu.Unlock()

		s.emitStatus("thinking")

		prompt := FormatPrompt(batch)
		slog.Debug("shepherd invoking", "prompt", prompt)

		if err := s.invoke(ctx, prompt); err != nil {
			slog.Error("shepherd invoke failed", "err", err)
		}

		s.mu.Lock()
		s.running = false
		hasMore := len(s.queue) > 0
		s.mu.Unlock()

		s.emitStatus("idle")

		if hasMore {
			select {
			case s.notify <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Shepherd) invoke(ctx context.Context, prompt string) error {
	invokeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--permission-mode", "bypassPermissions",
		"--allowed-tools", "Bash(dais-ctl:*)",
	}

	s.mu.Lock()
	claudeID := s.claudeID
	s.mu.Unlock()

	if claudeID != "" {
		args = append(args, "--resume", claudeID)
	}

	if s.cfg.Model != "" {
		args = append(args, "--model", s.cfg.Model)
	}

	// Pass prompt via stdin (not positional arg) because --allowed-tools
	// is variadic and would consume the prompt.
	slog.Debug("spawning shepherd claude", "args", args)

	cmd := exec.CommandContext(invokeCtx, "claude", args...)
	cmd.Dir = s.cfg.WorkDir
	cmd.Stdin = strings.NewReader(prompt)

	// Set up environment: remove CLAUDECODE, add DAIS_CTL_ADDR, ensure
	// dais-ctl is on PATH.
	env := filterEnv(os.Environ(), "CLAUDECODE")
	env = append(env, "DAIS_CTL_ADDR="+s.cfg.CtlAddr)
	if s.cfg.CtlBin != "" {
		// Prepend the directory containing dais-ctl to PATH.
		dir := s.cfg.CtlBin[:strings.LastIndex(s.cfg.CtlBin, "/")]
		for i, e := range env {
			if strings.HasPrefix(e, "PATH=") {
				env[i] = "PATH=" + dir + ":" + e[5:]
				break
			}
		}
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Log stderr.
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			slog.Debug("shepherd stderr", "line", scanner.Text())
		}
	}()

	// Parse stdout NDJSON.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		s.mu.Lock()
		rawFn := s.onRawLog
		s.mu.Unlock()
		if rawFn != nil {
			rawFn(line)
		}

		events := session.ParseLine(line)
		for _, ev := range events {
			switch ev.Type {
			case session.EventInit:
				s.mu.Lock()
				isNew := s.claudeID == ""
				if isNew {
					s.claudeID = ev.SessionID
					slog.Info("shepherd session established", "claude_id", ev.SessionID)
				}
				fn := s.onClaudeID
				s.mu.Unlock()
				if isNew && fn != nil {
					fn(ev.SessionID)
				}

			case session.EventText:
				s.mu.Lock()
				fn := s.onOutput
				s.mu.Unlock()
				if fn != nil {
					fn(ev.Content)
				}

			case session.EventToolUse:
				slog.Debug("shepherd tool call",
					"tool", ev.ToolName, "input", ev.ToolInput)

			case session.EventResult:
				slog.Debug("shepherd turn complete",
					"duration_ms", ev.DurationMs,
					"cost_usd", ev.CostUSD,
					"input_tokens", ev.Usage.InputTokens,
					"output_tokens", ev.Usage.OutputTokens,
					"cache_creation", ev.Usage.CacheCreationInputTokens,
					"cache_read", ev.Usage.CacheReadInputTokens)

			case session.EventError:
				slog.Warn("shepherd error", "msg", ev.ErrorMsg)
				s.mu.Lock()
				fn := s.onOutput
				s.mu.Unlock()
				if fn != nil {
					fn("I encountered an error: " + ev.ErrorMsg)
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("claude exited: %w", err)
	}
	return nil
}

func (s *Shepherd) emitStatus(state string) {
	s.mu.Lock()
	fn := s.onStatus
	s.mu.Unlock()
	if fn != nil {
		fn(state)
	}
}

func filterEnv(env []string, exclude string) []string {
	prefix := exclude + "="
	var result []string
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}
