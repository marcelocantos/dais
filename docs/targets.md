# Convergence Targets

## Active

### 🎯T5 Authentication implemented

- **Value**: 8
- **Cost**: 13
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

### 🎯T7 Mobile app for Jevon

- **Value**: 20
- **Cost**: 20
- **Status**: converging — Phase 1 (chat app) and Phase 2 (QR discovery) done. jevond prints QR code at startup with `jevon://` URL; iOS app has camera-based QR scanner. Worker list (Phase 3) not started.
- **Discovered**: 2026-03-08

**Desired state:** A phone app provides a UI for interacting with
jevond — sending commands, viewing responses, and managing workers.

**Acceptance criteria:**
- Mobile app connects to jevond over a secure channel.
- User can send text commands and see streaming responses.
- User can view and manage worker sessions.
- App works on iOS (primary target: Pippa, iPad Air 5th gen).

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
