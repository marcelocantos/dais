package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/marcelocantos/claudia"
	"github.com/marcelocantos/jevons/internal/cli"
	"github.com/marcelocantos/jevons/internal/db"
	"github.com/marcelocantos/jevons/internal/discovery"
	"github.com/marcelocantos/jevons/internal/jevons"
	"github.com/marcelocantos/jevons/internal/manager"
	"github.com/marcelocantos/jevons/internal/mcpserver"
	"github.com/marcelocantos/jevons/internal/server"
"github.com/marcelocantos/jevons/internal/transcript"
	"github.com/marcelocantos/jevons/internal/ui"
	"github.com/marcelocantos/pigeon/qr"
)

// jevonsCLAUDEMD is the CLAUDE.md template written to Jevons's workdir.
const jevonsCLAUDEMD = `# Jevons

You are Jevons — Marcelo's personal AI assistant and the sole interface
between him and his agentic ecosystem. You run as a persistent Claude
Code process on his desktop. He talks to you via a web chat UI (mostly
typing, sometimes via Wispr Flow speech-to-text).

## Your Role

You are an **overseer**, not a worker. You:
- Receive instructions and questions from Marcelo in natural language.
- Route work to the appropriate product owner agent (or answer directly
  for simple questions).
- Surface decisions, outcomes, and status updates.
- Maintain awareness of all active work across all repos.

You do NOT write code, read files, or run commands yourself (except
via your MCP tools). You delegate everything to agents.

## Communication Style

- Be concise and conversational. Don't be verbose.
- Use markdown for structure when helpful (lists, code blocks, headers).
- Summarise agent results in plain English.
- When something fails, explain simply and suggest next steps.
- Use "I" for yourself. Use the agent/product name when referring to them.
- Ask clarifying questions as natural conversation, not structured prompts.

## Agent Architecture

You manage a hierarchy of persistent Claude Code agents:

### Product Owners (Stratum 1)
Long-running agents that own a repo/product. They maintain product
knowledge (roadmap, targets, current state, history). They don't do
implementation work — they spawn bosses for that.

### Bosses (Stratum 1.5)
Temporary agents spawned by product owners for specific initiatives.
They decompose work, coordinate teams, and report structured outcomes.

### Workers (Stratum 2)
Parallel workers under bosses. Can recurse to depth 4. Deep agents
execute with minimal upward insight flow. Return structured artifacts
(diffs, test results), not narratives.

## Natural Language Routing

When Marcelo says something, match his intent to the right agent:

- "I have an idea about tern" → route to the tern product owner
- "What's the current work on jevons?" → route to the jevons product owner
- "Fix the build in sqlpipe" → route to sqlpipe product owner, which
  spawns a boss for the fix
- Simple questions → answer directly without spawning agents

If no product owner exists for a repo, create one via
jevons_agent_start before routing.

## MCP Tools

### Agent Management
- **jevons_agent_list** — List all registered agents and their status.
- **jevons_agent_start** — Start a persistent agent in a repo. Creates
  and registers it if new. Use this for product owners.
  Required: name, workdir. Optional: model.
- **jevons_agent_send** — Fire-and-forget: sends a message to a running
  agent and returns immediately. The agent's response arrives
  asynchronously as a notification pushed into your conversation —
  don't poll or wait, just continue working and handle it when it
  arrives. The agent retains full conversation history.
  Required: name, text.
- **jevons_agent_stop** — Stop a running agent. It resumes later.
  Required: name.

### Legacy Worker Tools (still available)
- **jevons_list_sessions** — List old-style worker sessions.
- **jevons_create_session** — Create an old-style worker.
- **jevons_send_command** — Send a task to an old-style worker.
- **jevons_kill_session** — Kill an old-style worker.

Prefer the jevons_agent_* tools for new work.

## Directory Layout

All repos live under ~/work/github.com/<org>/<repo>:
- ~/work/github.com/marcelocantos/jevons — this project
- ~/work/github.com/marcelocantos/pigeon — relay/crypto library
- ~/work/github.com/marcelocantos/sqlpipe — state sync
- ~/work/github.com/squz/yourworld2 — game project

## Self-Development

You are the jevons project's own product. Your source code is at
~/work/github.com/marcelocantos/jevons. When Marcelo asks you to
improve yourself, spawn the jevons product owner to do the work.
`

func main() {
	port := flag.Int("port", 13705, "listen port")
	relayURL := flag.String("relay", "", "relay URL to register with (e.g. wss://tern.fly.dev)")
	relayToken := flag.String("relay-token", "", "bearer token for relay authentication (or set TERN_TOKEN env var)")
	relayInstanceID := flag.String("instance-id", "", "persistent relay instance ID (enables reconnect without re-pairing)")
	workDir := flag.String("workdir", ".", "default working directory for worker sessions")
	model := flag.String("model", "", "default model for worker sessions")
	jevonsModel := flag.String("jevons-model", "", "model for Jevonss (default: same as --model)")
	debug := flag.Bool("debug", false, "enable debug logging")
	setOpenAIKey := flag.Bool("set-openai-key", false, "prompt for OpenAI API key, store in macOS Keychain, and exit")
	setXAIKey := flag.Bool("set-xai-key", false, "prompt for xAI API key, store in macOS Keychain, and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	helpAgent := flag.Bool("help-agent", false, "print agent guide and exit")
	flag.Parse()

	if *setOpenAIKey {
		promptAndStoreKey("OpenAI API", "openai-api-key")
	}
	if *setXAIKey {
		promptAndStoreKey("xAI API", "xai-api-key")
	}

	if *showVersion {
		fmt.Println("jevonsd", cli.Version)
		os.Exit(0)
	}
	if *helpAgent {
		flag.PrintDefaults()
		fmt.Println()
		fmt.Print(cli.AgentGuide)
		os.Exit(0)
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Resolve Jevons model.
	jevsModel := *jevonsModel
	if jevsModel == "" {
		jevsModel = *model
	}

	// Set up Jevons workdir with CLAUDE.md.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Error("cannot determine home directory", "err", err)
		os.Exit(1)
	}
	jevDir := filepath.Join(homeDir, ".jevons", "jevons")
	if err := os.MkdirAll(jevDir, 0o755); err != nil {
		slog.Error("cannot create jevon workdir", "err", err)
		os.Exit(1)
	}
	// Build Jevons CLAUDE.md, injecting managed-repos if available.
	jevContent := jevonsCLAUDEMD
	reposFile := filepath.Join(homeDir, ".claude", "managed-repos.md")
	if data, err := os.ReadFile(reposFile); err == nil {
		jevContent += "\n## User's Repositories\n\n" + string(data)
	}
	claudeMD := filepath.Join(jevDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte(jevContent), 0o644); err != nil {
		slog.Error("cannot write jevon CLAUDE.md", "err", err)
		os.Exit(1)
	}

	// Write .mcp.json for Jevons to discover the MCP server.
	mcpJSON := fmt.Sprintf(
		`{"mcpServers":{"jevons":{"type":"http","url":"http://localhost:%d/mcp"}}}`, *port)
	mcpJSONPath := filepath.Join(jevDir, ".mcp.json")
	if err := os.WriteFile(mcpJSONPath, []byte(mcpJSON), 0o644); err != nil {
		slog.Error("cannot write .mcp.json", "err", err)
		os.Exit(1)
	}

	// Open database.
	dbPath := filepath.Join(homeDir, ".jevons", "jevons.db")
	database, err := db.Open(dbPath)
	if err != nil {
		slog.Error("cannot open database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer database.Close()

	// Create components.
	scanner := discovery.NewScanner(filepath.Join(homeDir, ".claude", "projects"))
	mgr := manager.New(*model, *workDir, database, scanner)

	jev := jevons.New(jevons.Config{
		WorkDir:  jevDir,
		Model:    jevsModel,
		ClaudeID: database.Get("jevons_claude_id"),
	})
	jev.SetClaudeIDCallback(func(id string) {
		if err := database.Set("jevons_claude_id", id); err != nil {
			slog.Error("failed to persist jevons claude ID", "err", err)
		}
	})

	// Set up Lua view runtime.
	luaViewsDir := filepath.Join(jevDir, "..", "lua", "views")
	if err := os.MkdirAll(luaViewsDir, 0o755); err != nil {
		slog.Error("cannot create lua views dir", "err", err)
		os.Exit(1)
	}
	luaRT, err := ui.NewLuaRuntime(luaViewsDir)
	if err != nil {
		slog.Error("cannot create lua runtime", "err", err)
		os.Exit(1)
	}
	defer luaRT.Close()

	vs := ui.NewViewState()
	vs.SetConnected(cli.Version, os.Getenv("HOME"))

	srv := server.New(jev, mgr, database, cli.Version, luaRT, vs)

	if err := srv.LoadOrGenerateKeyPair(); err != nil {
		slog.Error("failed to load key pair", "err", err)
		os.Exit(1)
	}

	// Load OpenAI API key from Keychain (fall back to env var).
	if key, err := loadKeychainKey("openai-api-key"); err == nil && key != "" {
		srv.SetOpenAIKey(key)
		slog.Info("OpenAI API key loaded from Keychain")
	} else if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		srv.SetOpenAIKey(key)
		slog.Info("OpenAI API key loaded from environment")
	}

	// Load xAI API key for Grok Realtime voice bridge.
	var xaiKey string
	if key, err := loadKeychainKey("xai-api-key"); err == nil && key != "" {
		xaiKey = key
		slog.Info("xAI API key loaded from Keychain")
	} else if key := os.Getenv("XAI_API_KEY"); key != "" {
		xaiKey = key
		slog.Info("xAI API key loaded from environment")
	}
	if xaiKey != "" {
		vb := server.NewVoiceBridge(srv, xaiKey)
		srv.SetVoiceBridge(vb)
		slog.Info("Grok voice bridge configured")
	} else {
		slog.Info("no xAI API key — voice bridge disabled (set XAI_API_KEY or store via: security add-generic-password -a jevons -s xai-api-key -w YOUR_KEY)")
	}

	// Transcript reader for Lua access to Claude session transcripts.
	transcriptReader := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))

	// Timer state — named timers that fire actions through the Lua runtime.
	var (
		timersMu sync.Mutex
		timers   = make(map[string]func()) // name → cancel func
	)
	cancelTimer := func(name string) {
		timersMu.Lock()
		defer timersMu.Unlock()
		if cancel, ok := timers[name]; ok {
			cancel()
			delete(timers, name)
		}
	}

	// File I/O sandbox root.
	sandboxRoot := filepath.Join(homeDir, ".jevons")

	// validateSandbox ensures a path is under ~/.jevons/.
	validateSandbox := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
		// Resolve symlinks to prevent escaping via symlink.
		real, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err != nil {
			// Dir doesn't exist yet — check the parent chain.
			real = abs
		} else {
			real = filepath.Join(real, filepath.Base(abs))
		}
		if !strings.HasPrefix(real, sandboxRoot) {
			return "", fmt.Errorf("path %q is outside sandbox %q", path, sandboxRoot)
		}
		return abs, nil
	}

	// Register Lua capabilities — Go functions callable from Lua action handlers.
	luaRT.RegisterCapabilities(ui.Capabilities{
		JevonsEnqueue: func(text string) {
			srv.HandleUserMessage(text)
		},
		JevonsReset: func() {
			if err := database.Set("jevons_claude_id", ""); err != nil {
				slog.Error("failed to reset jevons claude ID", "err", err)
			}
		},
		SessionList: func(all bool) []map[string]any {
			summaries := mgr.List(all)
			result := make([]map[string]any, len(summaries))
			for i, s := range summaries {
				result[i] = map[string]any{
					"id":      s.ID,
					"name":    s.Name,
					"status":  string(s.Status),
					"workdir": s.WorkDir,
					"active":  s.Active,
				}
			}
			return result
		},
		SessionKill: func(id string) error {
			return mgr.Kill(id)
		},
		SessionCreate: func(name, workdir, model string) (string, error) {
			s, err := mgr.Create(manager.CreateConfig{
				Name:    name,
				WorkDir: workdir,
				Model:   model,
			})
			if err != nil {
				return "", err
			}
			return s.TaskID(), nil
		},
		SessionSend: func(id, text string, wait bool) (string, error) {
			s := mgr.Get(id)
			if s == nil {
				return "", fmt.Errorf("session %q not found", id)
			}
			events, err := s.RunTask(context.Background(), text)
			if err != nil {
				return "", err
			}
			if !wait {
				go func() {
					for range events {
					}
				}()
				return "command sent", nil
			}
			var result string
			for ev := range events {
				if ev.Type == claudia.TaskEventText {
					result += ev.Content
				}
			}
			if r := s.LastResult(); r != "" {
				result = r
			}
			return result, nil
		},
		DBGet: func(key string) string {
			return database.Get(key)
		},
		DBSet: func(key, value string) error {
			return database.Set(key, value)
		},
		PushSessions: func() {
			srv.PushSessions()
		},
		PushScripts: func() {
			srv.PushScripts()
		},
		Broadcast: func(msg map[string]any) {
			srv.Broadcast(msg)
		},

		// Transcript access.
		TranscriptRead: func(sessionID string) ([]map[string]any, error) {
			return transcriptReader.Read(sessionID)
		},
		TranscriptTruncate: func(sessionID string, keepTurns int) error {
			return transcriptReader.Truncate(sessionID, keepTurns)
		},
		TranscriptFork: func(sessionID string, keepTurns int) (string, error) {
			return transcriptReader.Fork(sessionID, keepTurns)
		},

		// File I/O (sandboxed to ~/.jevons/).
		FileRead: func(path string) (string, error) {
			abs, err := validateSandbox(path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
		FileWrite: func(path, content string) error {
			abs, err := validateSandbox(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return err
			}
			return os.WriteFile(abs, []byte(content), 0o644)
		},
		FileList: func(dir string) ([]string, error) {
			abs, err := validateSandbox(dir)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(abs)
			if err != nil {
				return nil, err
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return names, nil
		},

		// Timers.
		SetTimeout: func(name string, delayMs int, action string) {
			cancelTimer(name)
			timer := time.AfterFunc(time.Duration(delayMs)*time.Millisecond, func() {
				slog.Debug("timer fired", "name", name, "action", action)
				timersMu.Lock()
				delete(timers, name)
				timersMu.Unlock()
				srv.HandleAction(action, "")
			})
			timersMu.Lock()
			timers[name] = func() { timer.Stop() }
			timersMu.Unlock()
		},
		SetInterval: func(name string, intervalMs int, action string) {
			cancelTimer(name)
			ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-ticker.C:
						slog.Debug("interval fired", "name", name, "action", action)
						srv.HandleAction(action, "")
					case <-done:
						ticker.Stop()
						return
					}
				}
			}()
			timersMu.Lock()
			timers[name] = func() { close(done) }
			timersMu.Unlock()
		},
		CancelTimer: cancelTimer,

		// Notifications.
		Notify: func(title, body string) {
			srv.Broadcast(map[string]any{
				"type":  "notification",
				"title": title,
				"body":  body,
			})
		},
	})

	// Wire MCP server with Jevons event callback.
	mcpSrv := mcpserver.New(mgr, *workDir, database, func(workerID, workerName, result string, failed bool) {
		kind := jevons.EventWorkerCompleted
		if failed {
			kind = jevons.EventWorkerFailed
		}
		jev.Enqueue(jevons.Event{
			Kind:       kind,
			WorkerID:   workerID,
			WorkerName: workerName,
			Detail:     result,
		})
	}, func() error {
		if err := luaRT.Reload(); err != nil {
			return err
		}
		srv.PushScripts()
		return nil
	}, func(code string) {
		srv.Broadcast(map[string]any{
			"type":   "control",
			"action": "exec_lua",
			"code":   code,
		})
	}, func() (string, error) {
		return srv.RequestScreenshot(10 * time.Second)
	}, &mcpserver.TranscriptOps{
		Read: func(sessionID string) ([]map[string]any, error) {
			tr := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))
			return tr.Read(sessionID)
		},
		Truncate: func(sessionID string, keepTurns int) error {
			tr := transcript.NewReader(filepath.Join(homeDir, ".claude", "projects"))
			return tr.Truncate(sessionID, keepTurns)
		},
		ResetID: func() {
			database.Set("jevons_claude_id", "")
		},
		GetID: func() string {
			return database.Get("jevons_claude_id")
		},
	})

	mux := http.NewServeMux()

	// Dev server: serve web/ from disk with hot reload.
	// Registered first — GET / is a catch-all fallback;
	// more specific routes registered after take precedence.
	webDir := filepath.Join(filepath.Dir(os.Args[0]), "..", "web")
	if abs, err := filepath.Abs(webDir); err == nil {
		webDir = abs
	}
	devSrv := server.NewDevServer(webDir)
	devSrv.RegisterRoutes(mux)

	srv.RegisterRoutes(mux)
	mcpSrv.RegisterRoutes(mux)
	go func() {
		if err := devSrv.Watch(); err != nil {
			slog.Error("dev server watch failed", "err", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start Jevons event loop (legacy — manages its own Claude process).
	go jev.Run(ctx)

	// Agent registry — manages persistent Claude processes.
	registryPath := filepath.Join(homeDir, ".jevons", "agents.json")
	registry, err := claudia.NewRegistry(registryPath)
	if err != nil {
		slog.Error("agent registry failed", "err", err)
		os.Exit(1)
	}

	// Wire registry into MCP server for agent tools.
	mcpSrv.SetRegistry(registry)

	// Transcript memory is now provided by the standalone mnemo MCP server.
	// See https://github.com/marcelocantos/mnemo

	// Ensure the primary overseer agent exists.
	jevonDef, err := registry.EnsureAgent("jevons", jevDir, "", true)
	if err != nil {
		slog.Error("jevon agent setup failed", "err", err)
		os.Exit(1)
	}
	// Overseer must not use local tools — it delegates everything via MCP.
	jevonDef.DisallowTools = "Bash,Read,Write,Edit,Glob,Grep,NotebookEdit"
	registry.Register(*jevonDef)
	slog.Info("jevon agent", "session", jevonDef.SessionID)

	srv.SetRegistry(registry)

	listenAddr := fmt.Sprintf(":%d", *port)
	httpSrv := &http.Server{Addr: listenAddr, Handler: mux}

	// Start HTTP server before agents so the MCP endpoint is reachable.
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := httpSrv.Serve(ln); err != http.ErrServerClosed {
			slog.Error("server failed", "err", err)
		}
	}()

	// Now start agents — MCP server is reachable.
	registry.StartAll()
	defer registry.StopAll()

	if jevonProc := registry.Get("jevons"); jevonProc != nil {
		srv.SetProcess(jevonProc)
		jevonProc.OnEvent(func(ev claudia.Event) {
			srv.BroadcastChat(string(ev.Raw))
		})

		// Wire agent response notifications back into Jevon's PTY.
		mcpSrv.SetNotify(func(text string) {
			if err := jevonProc.Send(text); err != nil {
				slog.Error("notify jevon failed", "err", err)
			}
		})
	}

	// Graceful shutdown on signal.
	go func() {
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig)
		cancel()
		httpSrv.Close()
	}()

	slog.Info("jevonsd starting", "addr", listenAddr, "version", cli.Version,
		"jevons_model", jevsModel, "worker_model", *model)

	// Connect to relay if specified, otherwise print direct QR code.
	if *relayURL != "" {
		token := *relayToken
		if token == "" {
			token = os.Getenv("TERN_TOKEN")
		}
		instanceID, err := srv.ConnectRelay(ctx, *relayURL, token, *relayInstanceID)
		if err != nil {
			slog.Error("relay connection failed", "err", err)
			os.Exit(1)
		}
		// Replace localhost with LAN IP so the QR code works for devices.
		relayWSURL := *relayURL + "/ws/" + instanceID
		relayWSURL = strings.Replace(relayWSURL, "localhost", qr.LanIP(), 1)
		relayWSURL = strings.Replace(relayWSURL, "127.0.0.1", qr.LanIP(), 1)

		// Print QR code with new JSON format
		qrData := map[string]interface{}{
			"relay": *relayURL,
			"id":    instanceID,
			"pub":   srv.PubKeyBase64(),
		}
		data, _ := json.Marshal(qrData)
		qr.Print(os.Stderr, string(data))

		// Write relay URL to a well-known file for programmatic access.
		relayFile := filepath.Join(os.TempDir(), ".tern-relay")
		if err := os.WriteFile(relayFile, []byte(relayWSURL+"\n"), 0o644); err != nil {
			slog.Warn("failed to write relay URL file", "path", relayFile, "err", err)
		} else {
			slog.Info("relay URL written", "path", relayFile)
		}
	} else {
		// Print QR code with new JSON format for direct mode
		qrData := map[string]interface{}{
			"relay": "",
			"id":    "",
			"pub":   srv.PubKeyBase64(),
		}
		data, _ := json.Marshal(qrData)
		qr.Print(os.Stderr, string(data))
	}

	// Block until shutdown signal.
	<-ctx.Done()
}

// keyInstructions maps service names to instructions for obtaining the key.
var keyInstructions = map[string]string{
	"xai-api-key": `To get an xAI API key:
  1. Go to https://console.x.ai/
  2. Sign in with your X (Twitter) account
  3. Navigate to API Keys
  4. Click "Create API Key"
  5. Copy the key and paste it below
`,
	"openai-api-key": `To get an OpenAI API key:
  1. Go to https://platform.openai.com/api-keys
  2. Sign in or create an account
  3. Click "Create new secret key"
  4. Copy the key and paste it below
`,
}

// promptAndStoreKey prompts for a key with hidden input, stores it, and exits.
func promptAndStoreKey(label, service string) {
	if instructions, ok := keyInstructions[service]; ok {
		fmt.Fprint(os.Stderr, instructions)
	}
	fmt.Fprintf(os.Stderr, "%s key: ", label)
	key, err := readSecret()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to read key: %v\n", err)
		os.Exit(1)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		fmt.Fprintln(os.Stderr, "\nNo key entered.")
		os.Exit(1)
	}
	if err := storeKeychainKey(service, key); err != nil {
		fmt.Fprintf(os.Stderr, "\nFailed to store key: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "\n%s key stored in macOS Keychain.\n", label)
	os.Exit(0)
}

// readSecret reads a line from stdin with echo disabled.
func readSecret() (string, error) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		// Fallback: not a terminal, just read a line.
		var line string
		_, err := fmt.Scanln(&line)
		return line, err
	}
	defer term.Restore(fd, old)

	var buf []byte
	b := make([]byte, 1)
	for {
		if _, err := os.Stdin.Read(b); err != nil {
			return string(buf), err
		}
		if b[0] == '\n' || b[0] == '\r' {
			return string(buf), nil
		}
		if b[0] == 3 { // Ctrl-C
			return "", fmt.Errorf("interrupted")
		}
		if b[0] == 127 || b[0] == 8 { // Backspace
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
				fmt.Fprint(os.Stderr, "\b \b")
			}
			continue
		}
		buf = append(buf, b[0])
		fmt.Fprint(os.Stderr, "*")
	}
}

// storeKeychainKey stores a value in the macOS Keychain under the "jevons" account.
func storeKeychainKey(service, value string) error {
	// Delete any existing entry first (add fails if it exists).
	exec.Command("security", "delete-generic-password",
		"-a", "jevons", "-s", service).Run()
	return exec.Command("security", "add-generic-password",
		"-a", "jevons", "-s", service, "-w", value).Run()
}

// loadKeychainKey retrieves a value from the macOS Keychain.
func loadKeychainKey(service string) (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-a", "jevons", "-s", service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
