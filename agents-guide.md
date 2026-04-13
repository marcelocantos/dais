# Jevons Agent Guide

Jevons is a remote control system for Claude Code instances.
It consists of a coordinator daemon (`jevonsd`) and a TUI client (`remote`).

## Architecture

```
  remote (TUI)  ──WebSocket──►  jevonsd  ──spawns──►  Jevons (Claude Code)
  phone app     ──WebSocket──►          ──manages──►  workers  (Claude Code)
                                   MCP ◄─────────────┘ (tool calls)
```

- **jevonsd**: HTTP/WebSocket server. Manages a Jevons session and worker
  pool. Exposes an in-process MCP server for Jevons → worker management.
  Streams transcript to connected clients. Stores conversation history
  and raw NDJSON logs in SQLite (`~/.jevons/jevons.db`).
- **remote**: Terminal UI client. Connects to jevonsd via WebSocket, renders
  markdown responses with glamour, supports input history and scroll.

## Running

```bash
# Start the coordinator (default port 13705)
jevonsd --port 13705 --workdir ~/projects --model sonnet

# Connect a terminal client
remote --addr localhost:13705
```

## Key concepts

- **Jevon**: A Claude Code session managed by jevonsd that coordinates
  workers. It receives user messages and decides whether to answer
  directly or delegate to workers. It manages workers via MCP tools
  provided by the Jevons server.
- **Workers**: Claude Code sessions that do actual coding work. Jevon
  creates and manages them via MCP tools.
- **Remote clients**: Multiple TUI or mobile clients can connect
  simultaneously. User messages and responses are broadcast to all.

## MCP tools

jevonsd exposes an in-process MCP server at `/mcp`. Key tools:

- **`jevons_active_work`** — Unified dashboard of active work across all repos.
  Cross-references recent Claude Code sessions, dirty working trees, and open
  GitHub PRs. Parameters: `hours` (default 72), `include_clean` (default false).
- **`jwork`** — On-demand worker dispatch. Spawns a Claude Code worker to execute
  a task. Parameters: `task` (required), `repo` (optional), `model` (optional).
- **`jevons_agent_list`** — List all registered agents and their status.
- **`jevons_agent_start`** — Start a persistent agent in a repo/directory.
- **`jevons_agent_send`** — Send a message to a running agent (async fire-and-forget).
- **`jevons_agent_stop`** — Stop a running agent.

## WebSocket protocol

Clients connect to `ws://<host>:<port>/ws/remote`. Messages are JSON:

```json
// Client → server
{"type": "message", "text": "build the login page"}

// Server → client
{"type": "text", "content": "partial markdown..."}
{"type": "status", "state": "thinking|idle"}
{"type": "error", "message": "something went wrong"}
{"type": "history", "entries": [{"role": "user|jevons", "text": "...", "timestamp": "..."}]}
{"type": "user_message", "text": "...", "timestamp": "..."}
```

## Configuration

- **`~/.jevons/`**: Data directory (SQLite DB, Jevons workdir, input history).
- **`~/.claude/managed-repos.md`**: Optional file listing repos Jevon
  should know about. Injected into Jevon's CLAUDE.md.

## Gotchas

- The C++ app (`bin/jevons`) requires Git LFS objects and is not included
  in release binaries. Only the Go binaries are distributed.
- Jevon's CLAUDE.md and .mcp.json are generated at startup and
  written to `~/.jevons/jevons/`. Do not edit them manually.
