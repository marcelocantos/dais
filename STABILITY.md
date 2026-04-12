# Stability

## Stability commitment

Version 1.0 will represent a backwards-compatibility contract. After 1.0,
breaking changes to the public CLI interface, WebSocket protocol, REST API,
configuration format, or database schema require forking the project into a
new product (e.g. `jevons2`). The pre-1.0 period exists to get these surfaces
right before locking them in.

## Interaction surface catalogue

Snapshot as of v0.3.0.

### CLI: `jevonsd`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--port` | int | `13705` | Stable |
| `--relay` | string | `""` | Fluid — URL format and registration protocol may change |
| `--relay-token` | string | `""` | Fluid |
| `--instance-id` | string | `""` | Fluid |
| `--set-openai-key` | bool | `false` | Stable — interactive key prompt |
| `--set-xai-key` | bool | `false` | Fluid — new in v0.3.0, interactive xAI key prompt for Grok voice bridge |
| `--workdir` | string | `"."` | Needs review — semantics may evolve |
| `--model` | string | `""` | Needs review — may consolidate with config |
| `--jevons-model` | string | `""` | Needs review — same concern |
| `--debug` | bool | `false` | Stable |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### CLI: `remote`

Terminal UI client for jevonsd.

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--addr` | string | `"localhost:13705"` | Stable |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### MCP Server (`/mcp`)

| Tool | Parameters | Stability |
|---|---|---|
| `jevons_list_sessions` | `all?: bool` | Stable |
| `jevons_session_status` | `id: string` | Stable |
| `jevons_create_session` | `name?, workdir?, model?` | Stable |
| `jevons_send_command` | `id, text, wait?=true` | Stable |
| `jevons_kill_session` | `id: string` | Stable |
| `jevons_agent_list` | (none) | Fluid |
| `jevons_agent_start` | `name, workdir, model?` | Fluid |
| `jevons_agent_send` | `name, text` | Fluid — async fire-and-forget since v0.3.0 |
| `jevons_agent_stop` | `name` | Fluid |
| `jevons_transcript_read` | `session?, limit?` | Fluid — new in v0.3.0, reads Jevon conversation history |
| `jevons_transcript_rewind` | `session, n?` | Fluid — new in v0.3.0, trims Jevon history |
| `jevons_reload_views` | (none) | Fluid |

Transcript memory search has moved out-of-process. Global search across all
Claude Code sessions is now provided by the standalone
[`mnemo`](https://github.com/marcelocantos/mnemo) MCP server (previously
`jevons_search_memory`, `jevons_memory_query`, `jevons_memory_stats`,
`jevons_list_memory_sessions` — all removed in v0.3.0).

### WebSocket protocol

#### `/ws/chat` (new in v0.2.0)

Raw JSONL passthrough — server sends Claude Code JSONL events directly.
Client interprets user, assistant, tool_use, tool_result, and system events.
Client sends plain text messages (or "stop" to interrupt).

| Direction | Format | Stability |
|---|---|---|
| Server → Client | Raw JSONL lines (history + live) | Fluid |
| Client → Server | Plain text | Fluid |

#### `/ws/remote` (legacy)

Structured JSON messages for the iOS remote client.

| Direction | Stability |
|---|---|
| Server → Client | Fluid — many message types tied to Lua view architecture |
| Client → Server | Fluid |

#### `/ws/reload` (new in v0.2.0)

Dev-only hot reload signal. Server sends "reload" on file changes.

#### `/ws/agent-terminal` (new in v0.3.0)

Live PTY viewer for a running agent. Click an agent in the web UI to
stream its Claude Code session output.

| Direction | Format | Stability |
|---|---|---|
| Server → Client | Raw PTY bytes | Fluid |

#### `/ws/voice` (new in v0.3.0)

Grok Realtime voice bridge. Full-duplex audio between the browser/iOS
client and the xAI Realtime API (`wss://api.x.ai/v1/realtime`). Server
transcodes, applies adaptive local VAD, and relays audio and events.

| Direction | Format | Stability |
|---|---|---|
| Server ↔ Client | Binary audio frames + JSON events | Fluid |

### REST API

| Method | Path | Stability |
|---|---|---|
| `GET` | `/health` | Stable |
| `GET` | `/` | Fluid — serves web UI from `web/` directory |
| `GET` | `/api/agents` | Fluid |
| `GET` | `/scripts/*` | Fluid — new in v0.3.0, serves JS modules (transport.js, etc.) from `web/scripts/` |
| `GET` | `/api/sessions` | Stable |
| `GET` | `/api/sessions/{id}` | Stable |
| `POST` | `/api/sessions/{id}/kill` | Stable |
| `POST` | `/api/realtime/token` | Fluid |

### Agent registry (`~/.jevons/agents.json`)

New in v0.2.0. JSON array of agent definitions.

| Field | Type | Stability |
|---|---|---|
| `name` | string | Fluid |
| `workdir` | string | Fluid |
| `session_id` | string (UUID) | Fluid |
| `model` | string (optional) | Fluid |
| `auto_start` | bool | Fluid |
| `parent` | string (optional) | Fluid |

### Configuration

| Path | Purpose | Stability |
|---|---|---|
| `~/.jevons/` | Data directory | Stable |
| `~/.jevons/jevons.db` | SQLite database | Stable |
| `~/.jevons/agents.json` | Agent registry | Fluid |
| `~/.jevons/jevons/CLAUDE.md` | Generated Jevons instructions | Fluid |
| `~/.jevons/jevons/.mcp.json` | MCP server config for Jevons | Fluid |
| `~/.jevons/lua/views/` | Lua view scripts | Fluid |
| `~/.jevons/remote_history` | `remote` TUI input history | Stable |
| `web/` | Web UI (served from disk, hot-reloaded) | Fluid |

Transcript memory (`~/.jevons/memory.db`) was removed in v0.3.0. The
mnemo MCP server now provides global session indexing; jevonsd no
longer maintains its own transcript database.

## Gaps and prerequisites

### Security
- No auth on any surface. Pairing ceremony verified but not wired.
- Workers and Jevons run with permissions bypassed.

### Architecture
- Claude Code session/agent management was extracted to the `claudia`
  library in v0.3.0; remaining Grok realtime bridge still lives in-tree.
- Lua view script runtime (🎯T9) is partially implemented — server-side
  rendering works; client-side Lua on iOS is not yet wired.
- sqlpipe state sync (🎯T10) is incomplete — `internal/sync/` compiles
  but WebSocket protocol has not been cut over.

### Testing
- No integration tests for WebSocket, agent lifecycle, or voice bridge.
- No automated test for the end-to-end voice path (browser mic → Grok
  Realtime → TTS response).

### Documentation
- NOTICES file missing for vendored dependencies.
- README install section documents only the GitHub releases download
  path; no packaged distribution (brew, apt) yet.

## Out of scope for 1.0

- Mobile UI via ge engine.
- Worker-to-worker communication.
- Multi-user / multi-tenant support.
- Plugin or extension system.
