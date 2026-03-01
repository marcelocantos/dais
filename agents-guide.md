# dais Agent Guide

dais ("Do As I Say!") is a remote control system for Claude Code instances.
It consists of a coordinator daemon (`daisd`), a TUI client (`remote`), and
a worker management CLI (`dais-ctl`).

## Architecture

```
  remote (TUI)  ──WebSocket──►  daisd  ──spawns──►  shepherd (Claude Code)
  phone app     ──WebSocket──►         ──manages──►  workers  (Claude Code)
                                       ◄──REST────  dais-ctl (used by shepherd)
```

- **daisd**: HTTP/WebSocket server. Manages a shepherd session and worker
  pool. Streams transcript to connected clients. Stores conversation
  history and raw NDJSON logs in SQLite (`~/.dais/dais.db`).
- **remote**: Terminal UI client. Connects to daisd via WebSocket, renders
  markdown responses with glamour, supports input history and scroll.
- **dais-ctl**: CLI used by the shepherd to create/list/command/wait/kill
  worker sessions via the daisd REST API.

## Running

```bash
# Start the coordinator (default port 8080)
daisd --port 8080 --workdir ~/projects --model sonnet

# Connect a terminal client
remote --addr localhost:8080

# Worker management (normally called by shepherd, not directly)
dais-ctl create --name "feature work" --workdir ~/projects/myapp
dais-ctl list
dais-ctl command <id> "implement the login page"
dais-ctl wait <id>
dais-ctl kill <id>
```

## Key concepts

- **Shepherd**: A Claude Code session managed by daisd that coordinates
  workers. It receives user messages and decides whether to answer
  directly or delegate to workers.
- **Workers**: Claude Code sessions that do actual coding work. The
  shepherd creates and manages them via dais-ctl.
- **Remote clients**: Multiple TUI or mobile clients can connect
  simultaneously. User messages and responses are broadcast to all.

## WebSocket protocol

Clients connect to `ws://<host>:<port>/ws/remote`. Messages are JSON:

```json
// Client → server
{"type": "message", "text": "build the login page"}

// Server → client
{"type": "text", "content": "partial markdown..."}
{"type": "status", "state": "thinking|idle"}
{"type": "error", "message": "something went wrong"}
{"type": "history", "entries": [{"role": "user|shepherd", "text": "...", "timestamp": "..."}]}
{"type": "user_message", "text": "...", "timestamp": "..."}
```

## Configuration

- **`~/.dais/`**: Data directory (SQLite DB, shepherd workdir, input history).
- **`~/.claude/managed-repos.md`**: Optional file listing repos the
  shepherd should know about. Injected into the shepherd's CLAUDE.md.
- **`DAIS_CTL_ADDR`**: Environment variable overriding the dais-ctl
  base URL (default `http://localhost:8080`).

## Gotchas

- The C++ app (`bin/dais`) requires Git LFS objects and is not included
  in release binaries. Only the Go binaries are distributed.
- dais-ctl is a companion binary that must be in the same directory as
  daisd (it locates it relative to its own executable path).
- The shepherd's CLAUDE.md is generated at startup and written to
  `~/.dais/shepherd/CLAUDE.md`. Do not edit it manually.
