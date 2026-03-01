# Stability

## Stability commitment

Version 1.0 will represent a backwards-compatibility contract. After 1.0,
breaking changes to the public CLI interface, WebSocket protocol, REST API,
configuration format, or database schema require forking the project into a
new product (e.g. `dais2`). The pre-1.0 period exists to get these surfaces
right before locking them in.

## Interaction surface catalogue

Snapshot as of v0.1.0.

### CLI: `daisd`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--port` | int | `8080` | Stable |
| `--workdir` | string | `"."` | Needs review — semantics may evolve with project management features |
| `--model` | string | `""` | Needs review — may consolidate with config file |
| `--shepherd-model` | string | `""` | Needs review — same concern |
| `--debug` | bool | `false` | Stable |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### CLI: `remote`

| Flag | Type | Default | Stability |
|---|---|---|---|
| `--addr` | string | `"localhost:8080"` | Needs review — name/semantics will change with mTLS |
| `--version` | bool | `false` | Stable |
| `--help-agent` | bool | `false` | Stable |

### CLI: `dais-ctl`

| Subcommand | Arguments | Stability |
|---|---|---|
| `create` | `[--name NAME] [--workdir DIR] [--model MODEL]` | Stable |
| `list` | _(none)_ | Stable |
| `status <worker-id>` | positional | Stable |
| `command <worker-id> <prompt...>` | positional | Stable |
| `wait <worker-id>` | positional | Needs review — polling-based; may become long-poll or SSE |
| `kill <worker-id>` | positional | Stable |
| `--version` | _(none)_ | Stable |
| `--help-agent` | _(none)_ | Stable |

| Environment variable | Default | Stability |
|---|---|---|
| `DAIS_CTL_ADDR` | `http://localhost:8080` | Stable |

### WebSocket protocol (`ws://<host>:<port>/ws/remote`)

#### Server → Client

| Type | Fields | Stability |
|---|---|---|
| `init` | `version` string | Stable |
| `history` | `entries[]` — `{role, text, timestamp}` | Needs review — `role` values (`"user"`, `"shepherd"`) may align to `"assistant"` |
| `text` | `content` string (incremental markdown) | Stable |
| `status` | `state` string (`"thinking"`, `"idle"`) | Needs review — additional states may be needed |
| `error` | `message` string | Fluid — defined but not emitted by server |
| `user_message` | `text` string, `timestamp` string | Stable |

#### Client → Server

| Type | Fields | Stability |
|---|---|---|
| `message` | `text` string | Stable |

### REST API (`/ctl/`)

| Method | Path | Stability |
|---|---|---|
| `POST` | `/ctl/workers` | Needs review — no API versioning prefix |
| `GET` | `/ctl/workers` | Needs review |
| `GET` | `/ctl/workers/{id}` | Needs review |
| `POST` | `/ctl/workers/{id}/command` | Needs review |
| `DELETE` | `/ctl/workers/{id}` | Needs review |
| `GET` | `/health` | Stable |

Notes: Error response formats are inconsistent (JSON vs plain text). Worker
IDs are sequential `"s1"`, `"s2"`, etc. — may switch to ULIDs. No auth.

### SQLite schema (`~/.dais/dais.db`)

#### `transcript`

| Column | Type | Stability |
|---|---|---|
| `id` | INTEGER PK AUTOINCREMENT | Stable |
| `role` | TEXT | Needs review — unconstrained; values `"user"`, `"shepherd"` |
| `text` | TEXT | Stable |
| `created_at` | TIMESTAMP DEFAULT CURRENT_TIMESTAMP | Stable |

#### `kv`

| Column | Type | Stability |
|---|---|---|
| `key` | TEXT PK | Stable |
| `value` | TEXT | Stable |

Known keys: `shepherd_claude_id`.

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
| `~/.dais/` | Data directory | Stable |
| `~/.dais/dais.db` | SQLite database | Stable |
| `~/.dais/shepherd/CLAUDE.md` | Generated shepherd instructions | Fluid — regenerated on every startup |
| `~/.dais/remote_history` | TUI input history (plain text) | Stable |
| `~/.claude/managed-repos.md` | Optional repo list injected into shepherd | Needs review |

## Gaps and prerequisites

The following must be addressed before 1.0:

### Security
- **Authentication**: No auth on any surface. The `internal/auth` package is a
  stub. mTLS with QR-based device provisioning is planned but unimplemented.
- **Permission model**: Both shepherd and workers run with permissions bypassed
  (`--permission-mode bypassPermissions`, `--dangerously-skip-permissions`).
  Needs a trust model before 1.0.

### Protocol and API
- **Error message type**: Defined in the protocol but never emitted by the
  server. Must be wired up.
- **API versioning**: REST API has no `/v1/` prefix. Adding one later would
  break all clients.
- **Consistent error responses**: Mix of JSON and plain text 404s in ctlapi.
- **Worker ID format**: Sequential integers (`s1`, `s2`) — should be ULIDs
  for persistence across restarts.

### Data integrity
- **Worker status persistence**: Status is in-memory only. All workers become
  `idle` after a restart regardless of actual state.
- **Worker upsert bug**: `SaveWorker` does not update `workdir` or `model`
  after creation — schema/code mismatch.
- **Transcript pruning**: No mechanism to bound transcript size.

### Shepherd template
- **Hardcoded paths**: The embedded template references machine-specific
  directory layout and personal repos. Must be parameterised or removed.
- **Overwritten on startup**: Users cannot customise the shepherd's CLAUDE.md
  without it being overwritten.

### Documentation
- **README**: Needs usage examples, architecture diagram, and agent guide
  reference for discoverability.
- **`--help` output for `remote`**: No explicit usage function — only the
  default `flag` output.

### Testing
- **No integration tests** for WebSocket protocol, REST API, or shepherd
  lifecycle.
- **No tests** for `dais-ctl`, `remote`, `server`, `shepherd`, `voice`,
  `auth`, `cli`, `ctlapi`, or `db` packages.

## Out of scope for 1.0

- Mobile UI via ge engine (C++ app) — separate development track.
- Voice pipeline (AssemblyAI STT) — experimental, not part of the stable
  interface.
- Worker-to-worker communication.
- Multi-user / multi-tenant support.
- Plugin or extension system.
- Config file (`~/.dais/config.yaml`) — flags are sufficient for now.
