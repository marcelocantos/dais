# Jevon: Target-Driven Session Infrastructure

## Thesis

Every agent is the same thing: a session that receives targets, decides
how to achieve them, and optionally delegates sub-targets to other
sessions. There is no hierarchy — no "root", no "workers", no
predetermined tree. The shape of the work determines the shape of the
system. The session the user talks to is just a session whose target
happens to be "interact with the user."

The daemon (`jevonsd`) is not an agent. It is infrastructure — a session
runtime that manages lifecycles, routes work, and enforces capabilities.
It is to sessions what an OS kernel is to processes.

Execution safety (absorbed from doit) is also infrastructure — a layer
between sessions and the outside world. It enforces what sessions can
*do* (run commands, touch files), while the daemon enforces what
sessions can *be* (their scope, authority, and resource budget).

## Lineage

Jevons absorbs two donor projects:

- **cworkers** — session pooling, shadowing, dispatch, SQLite tracking,
  Svelte dashboard, MCP server infrastructure. These become the daemon's
  core primitives.
- **doit** — tiered execution safety, rule engine, capability registry,
  hash-chained audit log. This becomes the execution safety layer.

Both repos are archived after absorption. The Homebrew formula for
cworkers is replaced by Jevon's.

## Hard Constraints

The session model is deliberately minimal and emergent, but some
elements are load-bearing constraints imposed by the Claude Code
runtime and the physical world:

### Directory context

Every `claude` process runs in a working directory. That directory
determines which `CLAUDE.md` it loads and which `.claude/` settings it
discovers. This is not optional — it shapes the session's behaviour,
available tools, and project awareness. The daemon must track each
session's directory context.

### User I/O

One session interacts with the user. This requires a physical I/O
channel — a terminal, a mobile app WebSocket, a voice pipeline. This
channel must be explicitly plumbed; it doesn't emerge from the model.
The session that has it isn't structurally special, but it is
practically unique: it's the one the user talks to.

### The daemon itself

`jevonsd` is the one entity that genuinely isn't a session. It's a Go
process that must be running before any session exists. You can't
bootstrap infrastructure from the thing that depends on it. It runs
as a brew service or launchd agent.

### Transcript format and location

Claude Code stores transcripts in `~/.claude/projects/<encoded-path>/`
as JSONL files. The daemon must know this convention to shadow sessions
and reactivate them. This is a coupling to Claude Code internals — if
Anthropic changes the format or location, the daemon breaks.

### CLI contract

Sessions are spawned via `claude -p` with stdin/stdout NDJSON. The
daemon is coupled to this interface. If Anthropic ships a different
programmatic API (SDK, library), the spawn layer changes but the
session model doesn't.

### Credentials

Claude processes need Anthropic API credentials from the environment.
This is environment-level, not session-level.

### MCP discovery

Sessions discover `jevonsd` via MCP configuration. Global config in
`~/.claude/.mcp.json` solves this — every `claude` process,
regardless of directory context, finds the daemon automatically.

## Core Model

### Sessions

A session is the universal primitive. It has:

- **Target(s)** — what it's trying to achieve. This is the only reason
  a session exists.
- **Transcript** — accumulated context from all activations. This is
  the session's memory. With 1M token context windows, transcripts can
  grow large and persist indefinitely.
- **Directory context** — the working directory that determines CLAUDE.md
  and .claude/ discovery. A hard constraint, not a soft property.
- **Capabilities** — what this session is authorised to do. Inherited
  from the originator, never exceeding it. Enforced by the daemon
  (scope/authority) and the execution safety layer (command gating).
- **Provenance** — who created this session and what authority they
  delegated. Not a tree position — just a record of origin.

Sessions do not have types. The distinctions that feel natural are
emergent patterns, not categories:

| Pattern | What makes it emerge |
|---------|---------------------|
| Interactive session | Target: "interact with the user." Has I/O channel to a human. |
| Repo session | Target is scoped to a single repository. Accumulates deep repo context over time. |
| Foreman | Target: "coordinate work in area X." Routes sub-targets to appropriate sessions. Uses sonnet. |
| One-shot worker | Target is small enough to complete in a single activation. No delegation needed. |
| Cross-repo coordinator | Target spans multiple repos. Spawns repo-scoped sub-sessions. |

None of these are declared. They arise from the nature of the targets.

### Lifecycle

Sessions don't have explicit lifecycle states. They have **activity**:

- **Active** — a `claude` process is running against this session's
  transcript right now.
- **Idle** — no running process. The transcript and metadata persist.
  Can be reactivated at any time.

There is no "done" state. A session's originator might achieve its
target and then assign new work. Or it might idle indefinitely. With
1M token context, a session could hang around forever — if it's no
longer needed, it just doesn't get activated again. There's no reason
for an originator to think "let's kill it now." It may as well keep
it around, just in case.

**Reaping** is garbage collection, not a semantic event. Sessions that
haven't been activated within a configurable window get archived. The
daemon runs a quiet background sweep. Reaping policy could be
adaptive — sessions with large, context-rich transcripts get a longer
grace period since they're more expensive to recreate. If an originator
is reaped, its orphaned sub-sessions become reaping candidates too.

Reaping doesn't mean deletion. Transcripts can be archived, and in
principle a reaped session could be reconstituted — though at that age,
starting fresh is usually better.

### Authority

Authority flows from the user, not from tree position.

- The user's interactive session is the user's proxy. It holds **root
  authority** — it can inspect, pause, kill, or reprioritise any session
  in the system.
- When a session spawns a sub-session, it grants a **subset** of its
  own authority: specific targets, resource budgets (token spend, spawn
  limits), and scope constraints.
- A session cannot escalate its own authority. It can only subdivide
  what it was given.
- Any session can kill or modify sessions it spawned (it holds the
  capability token). The daemon enforces this.

This is capability-based security applied to session management. The
community doesn't get out of control because no session can exceed its
granted authority, and the user (through their interactive session)
always holds the master switch.

## Work Routing

`jwork` submits a target into the system. The daemon decides where
it runs.

When work is submitted, the daemon routes it:

1. **Existing idle session with relevant context?** Reactivate it. The
   transcript already contains useful reasoning — don't throw it away.
2. **Active session working on something related?** Queue it there.
   Let the session decide how to integrate the new target.
3. **Neither?** Spin up a new session.

The daemon makes routing decisions based on session metadata: target
descriptions, transcript summaries, recency, scope overlap. This is a
judgment call, not a computation — context awareness, not deep
reasoning. Sonnet is the right model for routing decisions. A poor
routing decision isn't catastrophic; worst case, a session lacks
context and does the work from scratch, slightly slower than optimal.

### Foremen

When a scope gets busy — multiple targets arriving for the same area —
the daemon can spin up a coordinating session (a "foreman") to manage
routing within that scope. Or a session that keeps getting routed
coordination work naturally becomes one. Foremen are emergent, not
prescribed.

A foreman is just a session whose target is "coordinate work in this
area." It knows what other sessions exist in its scope, what they're
working on, and routes new sub-targets accordingly. It doesn't need
opus — sonnet handles coordination well, and a suboptimal routing
decision is cheap.

### Batching and Continuations

Work decoupled from workers enables:

- **Batching** — two targets arrive for the same scope within seconds.
  Instead of two sessions, the daemon routes both to one.
- **Continuations** — "fix the build" fails. The caller submits "fix
  the build, here's the error." The daemon routes it back to the same
  session that already tried and has the failure context.
- **Specialisation** — over time, certain sessions accumulate deep
  expertise in an area. The daemon preferentially routes related work
  to them.

## MCP Interface

Jevons exposes its capabilities as MCP tools. Every session has access
to `jwork`. Control-plane tools require sufficient authority.

| Tool | Purpose |
|------|---------|
| `jwork(target, scope?, model?)` | Submit a target. Daemon routes it. Returns the result. |
| `jsessions(scope?)` | List sessions — active, idle, their targets, last activation. |
| `jkill(session_id)` | Kill a session and optionally its descendants. Requires authority over the target session. |
| `jprioritise(session_id, priority?)` | Reprioritise a session's work. Requires authority over the target session. |
| `jstatus()` | System overview — pool state, active count, metrics summary. |

Authority checks are enforced at execution time, not at tool
visibility. Any session can call `jkill`; the daemon checks whether
the caller holds a capability token that covers the target session.

## Infrastructure Layers

### Daemon — jevonsd (session runtime)

The daemon is dumb infrastructure. It provides:

- **Registry** — knows every session: its targets, directory context,
  transcript location, capabilities, provenance, activity state, last
  activation timestamp.
- **Activation** — spins up `claude` processes pointed at existing
  transcripts. Manages stdin/stdout/stderr.
- **Routing** — matches incoming work to sessions based on metadata,
  scope overlap, and recency.
- **Pooling** — pre-warms idle capacity for fast dispatch. After each
  activation, spawns a replacement so the next dispatch is instant.
  (Absorbed from cworkers.)
- **Shadowing** — tails active transcripts to provide context to newly
  spawned sessions in the same scope. (Absorbed from cworkers.)
- **Authority enforcement** — checks capability tokens on spawn, kill,
  and modify operations. Ensures sessions can't exceed their granted
  scope.
- **Reaping** — background sweep for idle sessions past their retention
  window. Archive and reclaim.
- **Tracking** — SQLite database recording sessions, activations,
  events, metrics. Feeds the dashboard.

The daemon does not make decisions about *what work to do*. That's the
sessions' job. The daemon only decides *where work runs* (routing) and
*whether work is allowed* (authority).

### Execution Safety (absorbed from doit)

Sits between sessions and the outside world. Enforces what commands a
session can execute:

- **Tiered safety** — read / build / write / dangerous. Each command
  evaluated against its tier.
- **Rules** — hardcoded blocklists (e.g., `rm -rf /`, `git push
  --force`), per-project policy, learned patterns.
- **Audit** — hash-chained append-only log of every command executed,
  the policy decision, and the outcome.

Execution safety does **not** sit between sessions. Inter-session
authority (who can talk to whom, who can spawn/kill whom) is the
daemon's concern. These are different trust boundaries with different
rules:

- Inter-session authority is lightweight — capability tokens, scope
  checks. Metadata operations.
- Execution safety is heavyweight — argument parsing, rule evaluation,
  tier escalation, audit chains. Real work.

### Relationship

```
   session ──→ jevonsd       "submit work / spawn session / kill session"
   session ──→ exec-safety  "execute this command safely"
   jevonsd  ──→ session      "here's new work / you're being deactivated"
```

The daemon manages the session graph. Execution safety manages the
session-OS boundary. Sessions make all the decisions about what to do.

## Model Selection

Two tiers for now:

- **Sonnet** — routing decisions, well-scoped implementation, foreman
  coordination, mechanical changes. Default for most work.
- **Opus** — complex reasoning, architectural decisions, novel
  problem-solving, deep analysis. Used when getting it right matters
  more than speed.

Haiku is a future optimisation. Adding it requires workload data to
know where it helps — which means the metrics infrastructure needs to
exist first. The metrics system should capture enough data to answer
"would haiku have been sufficient here?" in post-hoc analysis.

## Metrics and Observability

To make informed decisions about model selection, routing quality, and
system performance, the daemon must capture rich data:

### Per-session metrics
- Model used
- Target description
- Token counts (input/output) per activation
- Activation count and timestamps
- Time from activation to idle
- Outcome: target achieved / failed / redirected / still active

### Routing metrics
- What context signals led to routing to an existing session vs new
- Whether the routed session actually had useful context (did it
  reference prior transcript entries, or effectively start fresh?)
- Routing latency

### Cost metrics
- Token spend per target, broken down by model tier
- Cost per target achieved (aggregated across all sub-sessions)
- Where sonnet-routed work was trivial enough for haiku (post-hoc
  analysis for future tier decisions)

### Quality signals
- Did the work need revision (originator sent follow-up corrections)?
- Did tests pass on first activation?
- Did the originator accept the result or redirect?
- Session reactivation rate (high reactivation might indicate work
  wasn't completed adequately)

### Dashboard

The Svelte dashboard (absorbed from cworkers) evolves from
worker-event viewer to system-wide session analytics:

- **Live** — active sessions, their targets, current state
- **Graph** — session provenance relationships (who spawned whom)
- **Metrics** — cost, performance, routing quality over time
- **Model analysis** — where each tier was used and whether it was
  appropriate (key input for haiku introduction)

## Mobile App

The existing iOS app (🎯T7) provides the user I/O channel. Through it,
the user:

- Submits targets (which become `jwork` calls)
- Views session state and streaming output
- Manages sessions (kill, reprioritise)
- Approves/denies permission requests
- Uses voice for hands-free interaction

The mobile app talks to `jevonsd` over a secure channel (mTLS with
QR-based device provisioning, per the existing auth architecture).

## Design Rationale

The design emerged from a sequence of realisations, each dissolving an
assumed constraint:

1. **"Workers don't need to be one-shot."** The one-shot worker was an
   artifact of `claude -p` being a single prompt-response cycle. But
   sessions persist across process exits via their transcripts. A
   session that hits a snag can pause, get unblocked, and resume.
   There's no reason to force single-shot semantics.

2. **"Why impose a tree?"** The initial design had Jevons → repo
   sessions → workers as a strict hierarchy. But what about work that
   spans ge, sqlpipe, and sqldeep? Why arbitrarily insist it must live
   in one repo? The tree shape is itself a constraint, and an
   unnecessary one. Sessions form a graph, not a tree.

3. **"Is there even a root?"** If the hierarchy is emergent, and
   every session is the same primitive, then there's no root. The
   session the user talks to isn't structurally special — it's just a
   session with a user I/O channel. "Everyone's a Jevon."

4. **"Why tie work to workers?"** `cwork` implied spawning a new
   worker. But maybe existing sessions already have relevant context.
   Decoupling work from workers means the daemon routes targets to
   wherever they'll be most efficiently handled — reactivating idle
   sessions, queuing to active ones, or spawning new ones only when
   needed.

5. **"Foremen aren't a type."** The idea of a "foreman" — a session
   that coordinates others — felt like it needed a special construct.
   It doesn't. It's just a session whose target is "coordinate work in
   this area." It emerges when a scope gets busy.

6. **"Sessions don't need to die."** With 1M token context windows,
   sessions can persist indefinitely. An originator doesn't need to
   decide "this session is done." It just stops activating it. If it's
   needed later, it's still there with full context. Cleanup is garbage
   collection, not a decision.

7. **"Routing is a judgment call, not deep reasoning."** A poor
   routing decision just means a session starts without context — it
   works slightly slower, not incorrectly. Sonnet handles this fine.
   Haiku might too, but that's a future optimisation backed by data.

Each realisation removed a structural constraint and replaced it with
an emergent property. The result is a minimal model — sessions,
targets, capabilities — from which all the "obvious" structures
(repo sessions, workers, foremen, coordinators) emerge naturally.

## Absorption Inventory

### From cworkers (github.com/marcelocantos/cworkers)

cworkers is a single-file MCP server (`main.go`, ~1700 lines) with a
Svelte 5 dashboard. Key components to absorb:

| Component | Location | What it provides |
|-----------|----------|-----------------|
| **Worker pool** | `main.go` `pool` struct | Pre-warmed `claude -p` processes keyed by cwd+model. Self-warming replenishment after dispatch. |
| **Shadow registry** | `main.go` `shadowRegistry` struct | Per-cwd transcript tailers maintaining rolling context windows. Auto-discovers transcripts from `~/.claude/projects/`. |
| **Worker dispatch** | `main.go` `workerProc` struct + `broker.handleCwork` | Spawn process, write prompt to stdin, parse NDJSON stdout, extract result. Progress heartbeats every 30s. |
| **MCP server** | `main.go` `serve()` | Streamable HTTP MCP on port 4242 using mcp-go. Session hooks for connect/disconnect, depth tracking via URL params. |
| **SQLite tracking** | `main.go` `dbSchema` + `initDB` | Sessions, workers, events tables. WAL mode. `~/.local/share/cworkers/cworkers.db`. |
| **SSE event hub** | `main.go` `eventHub` struct | Broadcast lifecycle and per-worker events to dashboard clients. |
| **Dashboard** | `dashboard/` (7 Svelte 5 components, ~600 lines) | Hierarchical session/worker tree, real-time SSE updates, markdown+syntax-highlighted event log, URL state persistence, file opening via POST /api/open. |
| **Progress throttle** | `main.go` `progressThrottle` struct | Forward markdown headings from worker output as MCP progress notifications (tiered: H1 immediate, H2 throttled 10s, H3+ suppressed). |
| **help-agent.md** | Embedded via `go:embed` | Operational guide delivered to agents via MCP `WithInstructions`. |

### From doit (github.com/marcelocantos/doit)

doit is a capability broker with a three-level policy chain. Key
components to absorb:

| Component | Location | What it provides |
|-----------|----------|-----------------|
| **Engine** | `engine/engine.go` | Public API: `Evaluate` (dry-run), `Execute` (with policy check), `ExecuteStreaming`. Wraps the full policy chain. |
| **L1 — Deterministic rules** | `internal/policy/level1.go` + `internal/rules/` | Hardcoded denials (`rm -rf /`, `chmod 000 /`), config-driven rules, auto-allow for safe commands. <1ms. |
| **L2 — Learned patterns** | `internal/policy/level2.go` + `store.go` | Per-segment matching against learned policy store. Entries promoted from L3 after confidence stabilises. <10ms. |
| **L3 — LLM gatekeeper** | `internal/policy/level3.go` + `internal/llm/` | Novel commands evaluated by `claude -p`. Decisions migrate to L2 over time. 1–5s. |
| **Capability registry** | `internal/cap/` | Maps ~20 capability names to implementations with tier assignments (read/build/write/dangerous). |
| **Audit log** | `internal/audit/` | SHA-256 hash-chained, append-only JSONL. Tamper-evident. Query and verify operations. |
| **Config** | `internal/config/` | YAML config: global + per-project overlay (tighten-only merge). Tier enablement, policy levels, audit path. |
| **MCP tools** | `mcptools/mcptools.go` | 4 tools: `doit_execute`, `doit_dry_run`, `doit_policy_status`, `doit_approve`. |
| **Pipeline parser** | `internal/pipeline/` | Two-level parser for compound commands with unicode operators. Executor with goroutine-based piping. |

**Not absorbed** (legacy, already superseded):
- `internal/daemon/`, `internal/client/`, `internal/ipc/` — Unix
  socket daemon/client architecture. Replaced by MCP.
- Unicode operator syntax (`¦`, `›`, `‹`, `＆＆`, `‖`, `；`) —
  evaluate whether this is still needed or whether direct shell
  execution with policy gating is sufficient.

### Already in jevons

| Component | Location | Status | Relationship to vision |
|-----------|----------|--------|----------------------|
| **jevonsd daemon** | `cmd/jevonsd/main.go` | Real | Becomes the session runtime. Rewrite internals to use session model. |
| **Session management** | `internal/session/` | Real | Wraps `claude -p` with NDJSON parsing. Foundation for session activation. |
| **Manager** | `internal/manager/` | Real | Multi-session lifecycle with relevance scoring. Evolves into the session registry with routing. |
| **MCP server** | `internal/mcpserver/` | Real | 5 tools (`jevons_list/create/status/send_command/kill`). Becomes the `jwork/jsessions/jkill/jprioritise/jstatus` surface. |
| **HTTP/WebSocket server** | `internal/server/` | Real | Broadcasts Jevons events to connected clients. Continues serving mobile app + remote TUI. |
| **TUI client (remote)** | `cmd/remote/main.go` | Real | Bubble Tea terminal UI with markdown rendering. One possible user I/O channel. |
| **iOS app** | `ios/Jevon/` | Real | SwiftUI app with QR scanning, WebSocket, chat UI. Primary mobile I/O channel. |
| **SQLite** | `internal/db/` | Real | Transcript, KV, raw log tables. Extend with session registry, metrics, routing data. |
| **Session discovery** | `internal/discovery/` | Real | Scans `~/.claude/projects/` for JSONL files. Reused for shadow context. |
| **QR provisioning** | `internal/qr/` | Real | Generates QR codes for mobile app discovery. |
| **Auth** | `internal/auth/` | Stub | mTLS cert management. To be implemented. |
| **Voice** | `internal/voice/` | Stub | AssemblyAI + LLM cleanup. To be implemented. |
| **Worker** | `internal/worker/` | Stub | Superseded by the session model. |
| **C++ app** | `src/` | Placeholder | ge-based rendering. Future desktop UI, not on critical path. |
| **ge engine** | `ge/` (submodule) | Real | WebGPU/SDL3 rendering engine. Used by C++ app. |

## Migration Path

### Phase 1: Session model foundation

Replace jevons's current `internal/session/` + `internal/manager/` with
the session-as-universal-primitive model. Key changes:

- Sessions have targets, capabilities, provenance, directory context
- Session registry in SQLite (not just transcript/KV)
- `jwork` MCP tool that submits targets (initially routes to new
  sessions only — routing intelligence comes later)
- Remove the "Jevons is a special coordinator session" concept from
  `internal/jevons/` — it becomes just another session

### Phase 2: Absorb cworkers primitives

Bring in the pool, shadow, and dispatch infrastructure:

- Worker pool → session pool (pre-warmed `claude -p` processes)
- Shadow registry → unchanged (transcript tailing for context)
- Progress throttle → unchanged (MCP heartbeats)
- SSE event hub → extend for session lifecycle events
- Dashboard → adapt for session model (sessions instead of workers)

### Phase 3: Absorb doit execution safety

Bring in the policy engine as an execution safety layer:

- Engine API (`Evaluate`, `Execute`) wired into session command
  execution
- L1/L2/L3 policy chain operational
- Audit log integrated with session tracking
- Capability registry configured
- `jwork` results include policy decisions in metadata

### Phase 4: Intelligent routing

Add context-aware work routing:

- Session metadata (target descriptions, scope, recency) indexed
- Routing logic: match incoming targets to existing sessions
- Reactivation: spin up `claude -p` against existing transcripts
- Continuation support: related work routed to sessions with context
- Foreman emergence: detect busy scopes, spawn coordinators

### Phase 5: Metrics and analysis

Rich data capture for model tier optimisation:

- Per-session token counts, activation counts, outcomes
- Routing decision logging and quality assessment
- Cost aggregation per target
- Dashboard analytics views
- Foundation for haiku introduction decision

## Open Questions

1. **Transcript management** — with sessions persisting indefinitely,
   transcripts grow unbounded. Is this purely a `claude` concern
   (context compression), or does the daemon need to manage transcript
   size (archival, summarisation)?

2. **Routing intelligence** — how does the daemon know what's in a
   session's transcript without reading it? Metadata tags on sessions?
   Embeddings? Or just target descriptions and recency?

3. **Cross-daemon** — does the model extend to multiple machines? A
   daemon per machine with federated session routing? Or is single-
   machine the right scope?

4. **User multiplexing** — one user for now, but does the authority
   model extend to multiple users delegating to a shared daemon?

## Superseded Designs

This vision supersedes:

- **`docs/architecture.md`** — described a Bun+TypeScript orchestrator
  with Flutter mobile app and rigid session types. The Go daemon
  approach (from cworkers) with emergent session patterns replaces
  this.
- **cworkers `docs/proposal-unified-substrate.md`** — described a
  strict hierarchy (Jevons → repo sessions → workers) with Jevons as a
  separate user-facing product sitting above cworkers.

The key shifts from both:

- **No hierarchy** — sessions form a flat graph, not a tree. Structure
  emerges from work, not prescription.
- **No types** — no "repo session" vs "worker" vs "foreman" categories.
  Just sessions with different targets and lifetimes.
- **Work decoupled from workers** — `jwork` submits a target, the
  daemon routes it. The caller doesn't know or care whether the daemon
  reactivated an old session or spawned a new one.
- **Jevons is not special** — the interactive session is just a session
  whose target is "interact with the user."
- **Execution safety is infrastructure** — between sessions and the OS,
  not between sessions.
