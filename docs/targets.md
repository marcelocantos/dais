# Convergence Targets

<!-- last-evaluated: 073ff51a -->

## Active

### 🎯T5 Authentication implemented

- **Value**: 8
- **Cost**: 13
- **Weight**: 0.6 (value 8 / cost 13)
- **Status**: identified
- **Discovered**: 2026-03-08

**Desired state:** mTLS with QR-based device provisioning secures all
surfaces. The `internal/auth` package is fully implemented.

**Acceptance criteria:**
- mTLS is enforced on all jevond endpoints (HTTP, WebSocket, MCP).
- QR-based device provisioning flow works end-to-end (scan QR on phone,
  device gets a client certificate).
- `internal/auth` package has tests covering the provisioning and
  verification paths.
- Unauthenticated requests are rejected.

### 🎯T6 Permission model enforced

- **Value**: 5
- **Cost**: 8
- **Weight**: 0.6 (value 5 / cost 8)
- **Status**: identified
- **Discovered**: 2026-03-08

**Desired state:** Neither Jevon nor workers run with blanket permission
bypass. Permission tiers from the trust model (🎯T4) are enforced.

**Acceptance criteria:**
- `--permission-mode bypassPermissions` is removed from Jevon's
  invocation in `internal/jevon/jevon.go`.
- `--dangerously-skip-permissions` is removed from worker spawning.
- Confirmation requests from Claude Code are routed to the user via
  the WebSocket protocol.
- Tests verify that permission-requiring actions trigger confirmation.

### 🎯T8 Stateless worker dispatch

- **Value**: 21
- **Cost**: 18
- **Weight**: 1.2 (value 21 / cost 18)
- **Status**: identified — revised after cworkers v0.14 overhaul (removed pool, shadow context, transcript discovery in favour of stateless on-demand spawning)
- **Discovered**: 2026-03-14

**Desired state:** jevond dispatches work to on-demand Claude Code
workers via `jwork` MCP tool. Workers are disposable subprocesses —
spawned per task, observed via stdin/stdout, no pooling or implicit
context injection. Caller provides all context in the task description.
SQLite tracks workers for observability. cworkers absorbed into jevond.

**Key design principles (from cworkers v0.14 overhaul):**
- Workers that just do a job don't need session tracking — spawn, run, done.
- No shadow context injection — caller owns the task description.
- No worker pool — on-demand spawning is simpler; latency cost is acceptable.
- Progress via markdown heading extraction from worker output, not semantic understanding.
- Observability via SQLite + SSE for dashboard, but that's telemetry, not control state.

**Acceptance criteria:**
- All sub-targets achieved.
- cworkers repo archived after absorption complete.

**Prior design:** `docs/vision-v2.md` (superseded by this simpler model)

#### 🎯T8.1 Worker dispatch foundation

- **Value**: 8
- **Cost**: 5
- **Weight**: 1.6 (value 8 / cost 5)
- **Parent**: 🎯T8
- **Gates**: 🎯T8.2, 🎯T8.3
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** `jwork` MCP tool dispatches tasks to on-demand
Claude Code workers. Each worker is a fresh `claude -p` subprocess.
Task description is self-contained — no implicit context injection.

**Acceptance criteria:**
- `jwork` MCP tool accepts task text, optional cwd and model.
- Spawns `claude -p` subprocess, writes task to stdin, reads NDJSON
  from stdout.
- Returns result text when worker completes.
- Depth-controlled hierarchies: workers can call `jwork` up to
  max depth (3), with delegation guidance injected at higher depths.
- Progress heartbeats: extract markdown headings from worker output,
  throttle by heading depth and time window.

#### 🎯T8.2 Observability

- **Value**: 5
- **Cost**: 5
- **Weight**: 1.0 (value 5 / cost 5)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** Worker lifecycle and output tracked in SQLite for
dashboard visibility and post-hoc analysis.

**Acceptance criteria:**
- SQLite tables: workers (id, task, status, model, cwd, started_at,
  ended_at), events (worker output lines).
- SSE event hub: worker lifecycle events broadcast to dashboard.
- Dashboard shows active/completed workers with status and output.
- Per-worker token counts and outcomes recorded.

#### 🎯T8.3 Execution safety absorbed (doit)

- **Value**: 8
- **Cost**: 8
- **Weight**: 1.0 (value 8 / cost 8)
- **Parent**: 🎯T8
- **Depends on**: 🎯T8.1
- **Status**: identified
- **Discovered**: 2026-03-14

**Desired state:** doit's policy engine operates as an execution safety
layer between workers and the OS.

**Acceptance criteria:**
- Engine API (`Evaluate`, `Execute`) wired into worker command execution.
- L1/L2/L3 policy chain operational.
- Hash-chained audit log integrated with worker tracking.
- Capability registry configured.
- `jwork` results include policy decisions in metadata.

### 🎯T9 Server-driven UI for mobile app

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Tags**: visual
- **Status**: converging — server-side Lua rendering works end-to-end (current commit). Pivoting to client-side Lua execution.
- **Discovered**: 2026-03-14

**Desired state:** The iOS app is a programmable thin client. Lua view
scripts run on the device, rendering native SwiftUI from local state.
jevond pushes script updates and state changes; the phone renders
locally. Jevon (the AI agent) can modify scripts at runtime to reshape
the UI without app rebuilds or redeployment.

**Architecture:**
- **Client-side Lua.** The iOS app embeds a Lua runtime (C Lua, ~25KB).
  View scripts run on device against local state, producing view trees
  that the generic SwiftUI renderer displays. No server round-trip per
  render.
- **Script distribution.** jevond holds canonical scripts. On connect
  (or on change), pushes scripts to connected clients. Clients cache
  scripts locally for offline use.
- **State protocol.** Server sends structured state updates over
  WebSocket (new message, status change, session list diff). Client
  merges into local state and re-renders via Lua. Replaces streaming
  full view trees.
- **Primitives, not components.** View schema has fine-grained primitives
  (text, vstack, hstack, spacer, image, padding, background, etc.).
  "Chat bubble" is a composition defined in Lua, not a hardcoded client
  component.
- **Inline assets.** Images via SF Symbols (by name), data URIs (inline
  SVG/PNG), or bundled assets. Jevon can create novel icons without app
  bundling.
- **Dev flow.** Jevon edits scripts on the server → pushes draft to
  device → user previews → approves → promotes to live. Testing before
  releasing.
- **Reserved: `embed` component** for future ge wire protocol integration
  (game content rendered inline within server-driven UI).

**What exists (current commit):**
- Go: Lua runtime (gopher-lua), view state manager, view schema, MCP
  reload tool (`jevon_reload_views`)
- iOS: generic recursive SwiftUI renderer (`ServerView`), view/dismiss
  message handling
- Lua: screen scripts for chat, connect, sessions, session detail
- Server renders Lua → streams JSON view trees. This works but is the
  wrong architecture — should be client-side Lua.

**Remaining work:**
- Embed C Lua in iOS app (via SPM package or vendored source)
- Port view builder functions to Swift/C Lua bindings
- Change WebSocket protocol: server sends scripts + state updates,
  not rendered view trees
- Client runs Lua locally on state changes
- Add draft/preview/promote flow for script testing
- Smoke test: path abbreviation written by Jevon via conversation

**Acceptance criteria:**
- Lua runtime embedded in iOS app, running view scripts locally.
- jevond pushes script updates over WebSocket; client caches and
  executes them.
- Server sends state updates (messages, sessions, status), not view
  trees. Client renders locally from state.
- `jevon_reload_views` MCP tool pushes updated scripts to connected
  clients.
- Generic SwiftUI renderer maps Lua-produced view trees to native views.
- No business logic in Swift — all view logic in Lua scripts.
- Smoke test: Jevon writes path abbreviation via conversation, pushes
  script update, phone re-renders without app rebuild.

### 🎯T10 sqlpipe-based state sync

- **Value**: 13
- **Cost**: 13
- **Weight**: 1.0 (value 13 / cost 13)
- **Status**: converging — `internal/sync/` compiles cleanly with SyncManager, wire framing, and state writes. iOS sqlpipe vendor exists. Protocol not yet converted to pure sqlpipe transport.
- **Discovered**: 2026-03-15

**Desired state:** All state synchronisation between jevond and the iOS
app flows through sqlpipe bidirectional peer sync over the existing
WebSocket. No application-level message protocol — the WebSocket is a
pure sqlpipe transport.

**Architecture:**
- **jevond is a sqlpipe Peer.** Server-owned tables: `transcript`
  (chat messages), `sessions` (worker list), `scripts` (Lua view
  source), `state` (server status, version). Writes trigger
  `flush()` → changeset streamed to client.
- **iOS app is a sqlpipe Peer.** Client-owned tables: `requests`
  (user messages, action triggers), `preferences` (client settings).
  Writes replicate to server → jevond processes them.
- **Diff sync on reconnect.** Client catches up via sqlpipe's
  hash-based diff protocol. No manual history replay needed.
- **Query subscriptions.** Client subscribes to queries; Lua scripts
  receive live query results as state. Re-render only when relevant
  data changes.
- **Lua state from queries.** Instead of a manually-built state dict,
  Lua screen functions receive query results directly from the
  replica's subscribed queries.
- **Local query + subscribe in Lua.** Lua scripts call `query(sql)`
  to read the local replica directly and `subscribe(sql)` to declare
  data dependencies. When subscribed queries' underlying tables change
  (via incoming sqlpipe changesets), the screen auto-re-renders. No
  polling, no manual refresh, no `push_sessions()` action. Data flow:
  changeset arrives → subscribed queries re-evaluate → Lua runs →
  SwiftUI renders.

**Integration:**
- sqlpipe Go wrapper (`go/sqlpipe/`) for jevond
- sqlpipe C++ API via bridging header for iOS (same as Lua vendoring)
- Replace all WebSocket message types (init, history, text, status,
  user_message, sessions, scripts, notification, view, dismiss, action)
  with table reads/writes
- jevond's existing SQLite DB becomes the sqlpipe master database

**Dependencies:** `marcelocantos/sqlpipe` (sibling repo)

**Acceptance criteria:**
- WebSocket carries only sqlpipe peer messages — no application-level
  JSON messages.
- Server writes to transcript/sessions/scripts/state tables; changes
  stream to client automatically.
- Client writes to requests table; server processes inserts as actions.
- Reconnect uses diff sync — no full state resend.
- Lua scripts render from query subscription results.
- Chat, sessions, and status all reflect server state reliably without
  manual push logic.

### 🎯T11 Lua-controllable SwiftUI modifier surface

- **Value**: 8
- **Cost**: 5
- **Weight**: 1.6 (value 8 / cost 5)
- **Tags**: visual
- **Status**: identified
- **Discovered**: 2026-03-15

**Desired state:** SwiftUI behavioral modifiers are exposed as Lua props
so the server-driven UI has full control over rendering behavior without
Swift code changes.

**Sub-targets:**

#### 🎯T11.1 Essential modifiers (Phase 1)

- **Value**: 5
- **Cost**: 3
- **Weight**: 1.7 (value 5 / cost 3)
- **Parent**: 🎯T11
- **Status**: achieved
- **Discovered**: 2026-03-15

16 props that un-hardcode current behavior:
- Input: `keyboard`, `autocorrect`, `autocapitalize`, `submit_label`
- Scroll: `scroll_anchor`, `scroll_dismiss_keyboard`, `keyboard_avoidance`
- Layout: `frame_width`, `frame_height`, `frame_max_width`, `frame_max_height`
- Visual: `foreground_style`, `content_mode`
- Nav: `title_display_mode`
- Accessibility: `a11y_label`

#### 🎯T11.2 Useful modifiers (Phase 2)

- **Value**: 3
- **Cost**: 3
- **Weight**: 1.0 (value 3 / cost 3)
- **Parent**: 🎯T11
- **Depends on**: 🎯T11.1
- **Status**: identified
- **Discovered**: 2026-03-15

25 props for richer interactions and visual polish:
- Input: `secure`, `content_type`, `line_limit_min`, `line_limit_max`
- Scroll: `scroll_indicators`, `scroll_axis`
- Layout: `frame_min_width`, `frame_min_height`, `aspect_ratio`, `clip_shape`
- Visual: `shadow_radius`, `border_color`, `border_width`, `tint`, `resizable`
- Typography: `text_case`, `monospaced`, `text_selection`, `multiline_alignment`
- Interaction: `long_press_action`, `context_menu`, `confirmation`, `alert`
  (structured props as child node types, matching swipe_action pattern)
- Navigation: `pull_to_refresh`
- Accessibility: `a11y_hint`, `a11y_hidden`
- Animation: `transition`

### 🎯T7 Mobile app for Jevon

- **Value**: 20
- **Cost**: 20
- **Weight**: 1.0 (value 20 / cost 20)
- **Tags**: visual
- **Status**: converging — Phase 1 (chat), Phase 2 (QR discovery), and Phase 3 (session list/management UI) done. Remaining: secure channel (depends on 🎯T5) and real-device testing on Pippa.
- **Discovered**: 2026-03-08

**Desired state:** A phone app provides a UI for interacting with
jevond — sending commands, viewing responses, and managing workers.

**Acceptance criteria:**
- Mobile app connects to jevond over a secure channel.
- User can send text commands and see streaming responses.
- User can view and manage worker sessions.
- App works on iOS (primary target: Pippa, iPad Air 5th gen).

### 🎯T12 Script versioning and safe mode

- **Value**: 8
- **Cost**: 5
- **Weight**: 1.6 (value 8 / cost 5)
- **Gates**: 🎯T9
- **Status**: identified
- **Discovered**: 2026-03-21

**Desired state:** Lua script updates are versioned. If a script change
breaks the UI, the user can roll back to the last known-good version
without depending on the Lua layer.

**Architecture:**
- **Script versioning:** `script_versions` table in SQLite keeps the
  last N versions per script. Each `jevon_reload_views` push creates
  an atomic version snapshot across all scripts.
- **Control channel:** The WebSocket has a reserved message namespace
  below the Lua layer. Control messages (rollback, version query,
  health check) are handled before Lua sees them. The Lua layer
  accesses comms through an abstracted API, not raw WebSocket.
- **Safe mode trigger:** Two-finger chevron (`>`) gesture, recognised
  at the UIWindow level in Swift — independent of any Lua-rendered
  view hierarchy. Fires even if every script is broken.
- **Safe mode screen:** Pure Swift (no Lua). Shows current script
  version, last known-good version, rollback button, raw log view.
  Talks directly to the control channel.

**Acceptance criteria:**
- Script updates create versioned snapshots in SQLite.
- Two-finger chevron gesture triggers safe mode from any screen state.
- Safe mode screen renders without Lua, shows version info, allows
  one-tap rollback.
- Rollback restores all scripts to the selected snapshot atomically.
- Control channel messages bypass the Lua layer entirely.
- Smoke test: push a broken script, verify safe mode activates and
  rollback restores the working UI.

### 🎯T14 Onboarding and device pairing

- **Value**: 8
- **Cost**: 8
- **Weight**: 1.0 (value 8 / cost 8)
- **Status**: identified
- **Discovered**: 2026-03-21

**Desired state:** A new user goes from zero to connected in one flow
with no manual IP entry or configuration.

**Onboarding flow:**
1. User installs the iOS app. App opens a QR scanner and displays:
   "Run `brew install marcelocantos/tap/jevon && jevon --init` on
   your laptop."
2. `jevon --init` (a separate CLI binary, not jevond) prompts the
   user to paste their OpenAI API key. Stores it in macOS Keychain.
3. CLI pings jevond (running as a brew service) that the key is
   available.
4. jevond generates a one-time auth token, encodes it with host:port
   into a QR code, and sends it back to the CLI for display.
5. User points their device at the QR code on the terminal.
6. App scans QR, extracts host:port + auth token, connects to jevond
   with the token. jevond validates and promotes the connection.

**Key details:**
- jevond runs as a launchd service via `brew services start jevon`.
  Starts with or without the OpenAI key.
- QR code contains `wss://relay.jevon.app/ws/<instance-id>?token=<auth>`.
  The token authenticates the device pairing, not the OpenAI key.
- Manual IP:port entry removed from the connect screen. QR-only.
- `jevon` CLI binary is separate from `jevond` daemon.

**Relay architecture:**
- A small Go relay runs on Fly.io (`jevon-relay.fly.dev`).
- Each jevond connects outbound to `wss://jevon-relay.fly.dev/register`
  on startup and gets an instance ID.
- iOS app connects to `wss://jevon-relay.fly.dev/ws/<instance-id>`.
- Relay bridges WebSocket traffic between jevond and the app.
- No per-user DNS, no tunnels, fully dynamic. One relay serves all
  users.

**Device pairing ceremony (one-time via `jevon --init`):**
1. CLI prompts for OpenAI key, stores in macOS Keychain.
2. CLI asks jevond to generate a single-use pairing token.
3. jevond registers with relay, gets instance ID.
4. CLI displays QR: `wss://relay.../ws/<id>?pair=<token>`.
5. User scans QR. App connects, sends pairing token to prove it
   saw the QR.
6. jevond sends a 6-digit confirmation code to the app.
7. App displays: "Enter this code on your laptop: 847291".
8. User types code into CLI — proves same human controls both.
9. jevond generates a persistent device secret, sends to app.
10. App stores secret in iOS Keychain. jevond stores hash in DB.
11. Pairing token revoked, QR cleared from console.

**Subsequent connections:** App sends persistent secret + device
fingerprint (`identifierForVendor`). jevond verifies hash. No QR,
no user interaction.

**Revocation:** `jevon --unpair` revokes the device secret server-side.

**Acceptance criteria:**
- `brew install` installs both `jevon` CLI and `jevond` daemon.
- `jevon --init` runs the pairing ceremony end-to-end.
- Device secret persists in iOS Keychain; hash in jevond's DB.
- Subsequent connections authenticate automatically.
- `jevon --unpair` revokes a paired device.
- No manual host/port entry in the app.
- jevond runs as a brew service.
- Relay runs on Fly.io.

### 🎯T13 Full-duplex voice input

- **Value**: 13
- **Cost**: 8
- **Weight**: 1.6 (value 13 / cost 8)
- **Status**: identified
- **Discovered**: 2026-03-21

**Desired state:** The user speaks continuously into the iOS app. Each
utterance is transcribed in real-time via OpenAI's Realtime API
(`gpt-4o-transcribe`) and delivered to jevond as a user message
immediately. The agent can begin responding while the user is still
speaking. New utterances interrupt the current response — the agent
considers the full accumulated input before continuing.

**Architecture:**
- **Local VAD:** `AVAudioEngine` monitors mic levels on-device (always
  on, no network cost). On speech detection, opens OpenAI WebSocket.
- **Cloud transcription:** OpenAI Realtime API with `gpt-4o-transcribe`
  model. 24kHz mono PCM16 audio streamed via WebSocket. Semantic VAD
  detects utterance boundaries.
- **Ephemeral tokens:** jevond proxies OpenAI API key. iOS app requests
  a short-lived token per voice session. No secrets on-device.
- **Sentence delivery:** On `transcription.completed`, send transcript
  to jevond as a regular user message.
- **Interruption:** When a new utterance arrives while the agent is
  responding, jevond cancels the current Claude process and restarts
  with the full accumulated context.

**Acceptance criteria:**
- Local VAD detects speech onset and opens OpenAI Realtime connection.
- Audio streams to OpenAI, transcription deltas displayed in real-time.
- Completed utterances sent to jevond immediately as user messages.
- Agent response interrupted and restarted when new input arrives.
- Extended silence closes the OpenAI connection (back to local VAD).
- No API keys stored on device — ephemeral token flow via jevond.

## Achieved

### 🎯T4 Trust model defined for pre-1.0

Achieved. Trust model documented in `docs/trust-model.md` with three
permission tiers (autonomous, confirmed, prohibited) and WebSocket
confirmation flow. STABILITY.md updated to reference the design.


### 🎯T1 Jevon's tool surface is locked down [high]

Achieved in cf54767. All inappropriate tools disabled via `--disallowedTools`.

### 🎯T2 Conversational interaction model works end-to-end [high]

Achieved in 83cc4a4. AskUserQuestion disabled, CLAUDE.md template instructs
conversational questions.

### 🎯T3 Test coverage exists for core packages [medium]

Achieved in cf45460 + 6215164. 61 tests across 7 packages. All packages
with code have tests; untested packages (auth, cli, voice, worker) are
empty stubs.
