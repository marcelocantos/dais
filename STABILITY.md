# Stability

## Stability commitment

Version 1.0 will represent a backwards-compatibility contract. After 1.0,
breaking changes to the public CLI interface, WebSocket protocol, REST API,
configuration format, or database schema require forking the project into a
new product (e.g. `jevon2`). The pre-1.0 period exists to get these surfaces
right before locking them in.

## Interaction surface catalogue

Snapshot as of v0.2.0.

### CLI: `jevond`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--port` | int | `13705` | Stable |
| `--relay` | string | `""` | Fluid — new in v0.2.0; URL format and registration protocol may change |
| `--set-openai-key` | string | `""` | Stable |
| `--workdir` | string | `"."` | Needs review — semantics may evolve with project management features |
| `--model` | string | `""` | Needs review — may consolidate with config file |
| `--jevon-model` | string | `""` | Needs review — same concern |
| `--debug` | bool | `false` | Stable |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### CLI: `remote`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--addr` | string | `"localhost:13705"` | Needs review — name/semantics will change with mTLS |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### MCP Server (`/mcp`)

| Tool | Parameters | Stability |
|---|---|---|
| `jevon_list_sessions` | `all?: bool` | Stable |
| `jevon_session_status` | `id: string` | Stable |
| `jevon_create_session` | `name?, workdir?, model?` | Stable |
| `jevon_send_command` | `id, text, wait?=true` | Stable |
| `jevon_kill_session` | `id: string` | Stable |
| `jevon_reload_views` | (none) | Fluid — new in v0.2.0; depends on Lua view architecture |

### WebSocket protocol (`ws://<host>:<port>/ws/remote`)

#### Server → Client

| Type | Fields | Stability |
|---|---|---|
| `init` | `version` string | Stable |
| `history` | `entries[]` — `{role, text, timestamp}` | Needs review — `role` values may align to `"assistant"` |
| `text` | `content` string (incremental markdown) | Stable |
| `status` | `state` string (`"thinking"`, `"idle"`) | Needs review — additional states may be needed |
| `error` | `message` string | Fluid — defined but not emitted by server |
| `user_message` | `text` string, `timestamp` string | Stable |
| `scripts` | `source` string (Lua view scripts) | Fluid — new in v0.2.0; Lua view architecture evolving |
| `view` | view tree JSON (server-rendered) | Fluid — being replaced by client-side Lua |
| `dismiss` | `screen` string | Fluid — tied to view architecture |
| `sessions` | `sessions[]` — worker session list | Fluid — format evolving |
| `notification` | `title`, `body` strings | Fluid — new, may change |
| `control` | `action`, `value` strings | Fluid — control channel for safe mode |

#### Client → Server

| Type | Fields | Stability |
|---|---|---|
| `action` | `action` string, `value` string | Fluid — tied to Lua view architecture |
| `user_message` | `text` string | Stable |
| `control` | `action` string, `value` string | Fluid — control channel for safe mode |

### REST API

| Method | Path | Stability |
|---|---|---|
| `GET` | `/health` | Stable |
| `GET` | `/api/sessions` | Stable |
| `GET` | `/api/sessions/{id}` | Stable |
| `POST` | `/api/sessions/{id}/kill` | Stable |
| `POST` | `/api/realtime/token` | Fluid — new in v0.2.0; proxies OpenAI ephemeral token |

### SQLite schema (`~/.jevon/jevon.db`)

#### `transcript`

| Column | Type | Stability |
|---|---|---|
| `id` | INTEGER PK AUTOINCREMENT | Stable |
| `role` | TEXT | Needs review — unconstrained; values `"user"`, `"jevon"` |
| `text` | TEXT | Stable |
| `created_at` | TIMESTAMP DEFAULT CURRENT_TIMESTAMP | Stable |

#### `kv`

| Column | Type | Stability |
|---|---|---|
| `key` | TEXT PK | Stable |
| `value` | TEXT | Stable |

Known keys: `jevon_claude_id`.

#### `workers`

| Column | Type | Stability |
|---|---|---|
| `id` | TEXT PK | Needs review — format (`"s1"`) |
| `name` | TEXT | Stable |
| `workdir` | TEXT | Stable |
| `model` | TEXT | Stable |
| `claude_id` | TEXT | Stable |
| `last_result` | TEXT | Stable |

Note: No `status` column — status is in-memory only and lost on restart.

#### `raw_log`

| Column | Type | Stability |
|---|---|---|
| `id` | INTEGER PK AUTOINCREMENT | Fluid |
| `source` | TEXT | Fluid |
| `line` | TEXT | Fluid |
| `created_at` | TIMESTAMP DEFAULT CURRENT_TIMESTAMP | Fluid |

Recently added. No read path exists yet. Schema and purpose likely to evolve.

### Configuration

| Path | Purpose | Stability |
|---|---|---|
| `~/.jevon/` | Data directory | Stable |
| `~/.jevon/jevon.db` | SQLite database | Stable |
| `~/.jevon/jevon/CLAUDE.md` | Generated Jevon instructions | Fluid — regenerated on every startup |
| `~/.jevon/jevon/.mcp.json` | MCP server config for Jevon | Fluid — regenerated on every startup |
| `~/.jevon/lua/views/` | Lua view scripts | Fluid — new in v0.2.0 |
| `~/.jevon/remote_history` | TUI input history (plain text) | Stable |
| `$TMPDIR/.tern-relay` | Relay URL file (written by jevond) | Fluid — new in v0.2.0 |

### Dependencies

| Dependency | Version | Purpose | Stability |
|---|---|---|---|
| `github.com/marcelocantos/tern` | v0.2.0 | Relay client, E2E crypto, protocol framework, QR | Fluid — new in v0.2.0 |
| `github.com/marcelocantos/sqlpipe` | v0.12.0 | Bidirectional state sync | Needs review |

## Gaps and prerequisites

The following must be addressed before 1.0:

### Security
- **Authentication**: No auth on any surface. The `internal/auth` package is a
  stub. Pairing ceremony is formally verified (🎯T15 achieved) but not wired
  into runtime. Tracked by 🎯T5 (authentication) and 🎯T6 (permission
  enforcement).
- **Permission model**: Both Jevon and workers run with permissions bypassed.
  Trust model defined in `docs/trust-model.md`.

### Protocol and API
- **Error message type**: Defined in the protocol but never emitted by the
  server. Must be wired up.
- **Server-driven UI protocol**: Many message types (`view`, `dismiss`,
  `scripts`, `action`, `control`, `sessions`, `notification`) are fluid.
  The Lua view architecture is still evolving (🎯T9).

### Data integrity
- **Worker status persistence**: Status is in-memory only.
- **Transcript pruning**: No mechanism to bound transcript size.

### Jevon template
- **Hardcoded paths**: The embedded template references machine-specific
  directory layout and personal repos. Must be parameterised or removed.
- **Overwritten on startup**: Users cannot customise Jevon's CLAUDE.md
  without it being overwritten.

### Testing
- **No integration tests** for WebSocket protocol, relay bridge, or voice
  pipeline.
- Test coverage improved since v0.1.0 (61 tests across 7 packages) but
  gaps remain in `voice`, `auth`, `cli` packages.

### Documentation
- **NOTICES file**: iOS vendored dependencies (Lua, sqlpipe, sqldeep) lack
  attribution file.

## Out of scope for 1.0

- Mobile UI via ge engine (C++ app) — separate development track.
- Worker-to-worker communication.
- Multi-user / multi-tenant support.
- Plugin or extension system.
- Config file (`~/.jevon/config.yaml`) — flags are sufficient for now.
