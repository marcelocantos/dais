# Stability

## Stability commitment

Version 1.0 will represent a backwards-compatibility contract. After 1.0,
breaking changes to the public CLI interface, WebSocket protocol, REST API,
configuration format, or database schema require forking the project into a
new product (e.g. `jevons2`). The pre-1.0 period exists to get these surfaces
right before locking them in.

## Interaction surface catalogue

Snapshot as of v0.2.0.

### CLI: `jevonsd`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--port` | int | `13705` | Stable |
| `--relay` | string | `""` | Fluid — URL format and registration protocol may change |
| `--relay-token` | string | `""` | Fluid |
| `--instance-id` | string | `""` | Fluid |
| `--set-openai-key` | string | `""` | Stable |
| `--workdir` | string | `"."` | Needs review — semantics may evolve |
| `--model` | string | `""` | Needs review — may consolidate with config |
| `--jevons-model` | string | `""` | Needs review — same concern |
| `--debug` | bool | `false` | Stable |
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
| `jevons_agent_list` | (none) | Fluid — new in v0.2.0 |
| `jevons_agent_start` | `name, workdir, model?` | Fluid — new in v0.2.0 |
| `jevons_agent_send` | `name, text` | Fluid — new in v0.2.0 |
| `jevons_agent_stop` | `name` | Fluid — new in v0.2.0 |
| `jevons_search_memory` | `query, limit?, session_type?` | Fluid — new in v0.2.0 |
| `jevons_memory_query` | `query` (SQL/sqldeep) | Fluid — new in v0.2.0 |
| `jevons_memory_stats` | (none) | Fluid — new in v0.2.0 |
| `jevons_list_memory_sessions` | `session_type?, min_messages?, limit?, project?` | Fluid — new in v0.2.0 |
| `jevons_reload_views` | (none) | Fluid |

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

### REST API

| Method | Path | Stability |
|---|---|---|
| `GET` | `/health` | Stable |
| `GET` | `/` | Fluid — serves web UI from `web/` directory |
| `GET` | `/api/agents` | Fluid — new in v0.2.0 |
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

### Transcript memory (`~/.jevons/memory.db`)

New in v0.2.0. SQLite FTS5 index of all Claude Code session transcripts.

| Table | Columns | Stability |
|---|---|---|
| `messages` | `id, session_id, project, role, text, timestamp, type, is_noise` | Fluid |
| `messages_fts` | FTS5 virtual table on text/role/project/session_id | Fluid |
| `sessions` | View: session_id, project, session_type, total_msgs, substantive_msgs, first_msg, last_msg | Fluid |
| `ingest_state` | `path, offset` | Fluid |

### Configuration

| Path | Purpose | Stability |
|---|---|---|
| `~/.jevons/` | Data directory | Stable |
| `~/.jevons/jevons.db` | SQLite database | Stable |
| `~/.jevons/agents.json` | Agent registry | Fluid — new in v0.2.0 |
| `~/.jevons/memory.db` | Transcript memory index | Fluid — new in v0.2.0 |
| `~/.jevons/jevons/CLAUDE.md` | Generated Jevons instructions | Fluid |
| `~/.jevons/jevons/.mcp.json` | MCP server config for Jevons | Fluid |
| `web/` | Web UI (served from disk, hot-reloaded) | Fluid — new in v0.2.0 |

## Gaps and prerequisites

### Security
- No auth on any surface. Pairing ceremony verified but not wired.
- Workers and Jevons run with permissions bypassed.

### Architecture
- Legacy Jevons process (voice pipeline) still runs alongside new agent registry.
- sqlpipe removed but some legacy code paths (Lua views, sync) remain as stubs.
- Agent MCP tools exist but not fully tested end-to-end.

### Testing
- No integration tests for WebSocket, agent lifecycle, or transcript memory.

### Documentation
- NOTICES file missing for vendored dependencies.
- README needs updating to reflect new architecture.

## Out of scope for 1.0

- Mobile UI via ge engine.
- Worker-to-worker communication.
- Multi-user / multi-tenant support.
- Plugin or extension system.
