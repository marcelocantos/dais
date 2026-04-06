# Convergence Report

Standing invariants: all green.
- Tests: all passing (`go test ./...` — 11 packages with tests, all OK)
- CI: on master, no open PRs.

## Movement

- 🎯T13: significant → close (token proxy and interruption handling added since last eval)
- 🎯T12: (unchanged — server-side versioning still missing)
- 🎯T11: (unchanged — T11.1 achieved, T11.2 not started)
- 🎯T8: (unchanged)
- 🎯T17: (new target — first evaluation)
- 🎯T16: (new target — first evaluation, blocked by 🎯T8)
- 🎯T9: (unchanged)
- 🎯T10: (unchanged)
- 🎯T7: (unchanged)
- 🎯T14: (unchanged)
- 🎯T5: (unchanged)
- 🎯T6: (unchanged)

## Gap Report

### 🎯T13 Full-duplex voice input  [weight 1.6]
Gap: close
iOS VoiceManager fully implemented (local VAD, OpenAI Realtime API WebSocket, 24kHz PCM16 streaming, silence timeout, ephemeral token request flow). Server-side token proxy endpoint (`POST /api/realtime/token`) implemented in jevonsd. Interruption handling in place (`Process.Interrupt()` sends Esc to cancel current turn). Remaining: test end-to-end on real device (Pippa).

### 🎯T11 Lua-controllable SwiftUI modifier surface  [weight 1.6]  (visual)
Gap: converging (1/2 sub-targets achieved)

  [x] 🎯T11.1 Essential modifiers (Phase 1) — achieved
  [ ] 🎯T11.2 Useful modifiers (Phase 2) — not started: none of the 25 Phase 2 props found in schema.go or ServerView.swift

Visual verification outstanding — target is tagged `visual` but no verification recorded. Run on simulator/device and confirm UI before marking achieved.

### 🎯T12 Script versioning and safe mode  [weight 1.6]
Gap: significant
iOS scaffolding exists: `ChevronGestureRecognizer.swift` (two-finger chevron gesture) and `SafeModeView.swift` (pure-Swift safe mode screen). Server-side control channel stub exists (`handleControl` in server.go) but rollback returns "sync not available" and `list_snapshots` is similarly stubbed. No `script_versions` table in the DB. No snapshot/rollback implementation. Gates 🎯T9.

### 🎯T8 Stateless worker dispatch  [weight 1.2]
Gap: not started (0/3 sub-targets achieved)

  [ ] 🎯T8.1 Worker dispatch foundation — not started: no `jwork` MCP tool, no on-demand `claude -p` spawning.
  [ ] 🎯T8.2 Observability — not started (blocked by 🎯T8.1)
  [ ] 🎯T8.3 Execution safety absorbed (doit) — not started (blocked by 🎯T8.1)

### 🎯T9 Server-driven UI for mobile app  [weight 1.0]  (visual)  (status only)
Status: converging
Changed files overlap: `internal/ui/schema.go`, `ios/Jevon/Views/ServerView.swift`, `ios/Jevon/Models/LuaRuntime.swift` — may be affected.

### 🎯T10 sqlpipe-based state sync  [weight 1.0]  (status only)
Status: converging
Changed files overlap: `internal/sync/sync.go` — may be affected.

### 🎯T7 Mobile app for Jevons  [weight 1.0]  (visual)  (status only)
Status: converging
Changed files overlap: `ios/Jevon/Views/ChatView.swift` — may be affected.

### 🎯T14 Onboarding and device pairing  [weight 1.0]  (status only)
Status: identified
Protocol state machine framework (🎯T15) achieved — provides foundation for the pairing ceremony.

### 🎯T16 Session-to-agent migration  [weight 1.0]  (BLOCKED by 🎯T8)
Gap: not started (0/2 sub-targets achieved)

  [ ] 🎯T16.1 Active work dashboard — not started (plan in `docs/plans/active-work-dashboard.md`)
  [ ] 🎯T16.2 Session grandfathering — not started (blocked by 🎯T16.1)

### 🎯T17 Jevons UI renders via ge engine  [weight 0.6]
Gap: not started (0/3 sub-targets achieved)

  [ ] 🎯T17.1 jevons-ui C++ ge application scaffold — not started
  [ ] 🎯T17.2 jevons-ui feature parity with web UI — not started (blocked by 🎯T17.1)
  [ ] 🎯T17.3 jevons-ui runs headless with scene protocol — not started (blocked by 🎯T17.2)

### 🎯T5 Authentication implemented  [weight 0.6]  (status only)
Status: identified
No changed files overlap. `internal/auth` remains a stub.

### 🎯T6 Permission model enforced  [weight 0.6]  (status only)
Status: identified
No changed files overlap. `--dangerously-skip-permissions` still present.

## Recommendation

Work on: **🎯T13 Full-duplex voice input**
Reason: Tied for highest effective weight (1.6) with 🎯T11, 🎯T12, and 🎯T8.1. 🎯T13 has the smallest gap — "close" vs "significant" or "not started" for the others. All server and client code is implemented; the only remaining work is real-device testing on Pippa. Closing this target is the cheapest win among the top-weighted targets.

## Suggested action

Connect Pippa and test the full voice pipeline end-to-end: build and install the iOS app via `xcodebuild`, verify the mic activates local VAD, confirm audio streams to OpenAI via the ephemeral token from jevonsd's `/api/realtime/token` endpoint, and check that completed utterances arrive as chat messages. Test interruption by speaking while the agent is responding.

<!-- convergence-deps
evaluated: 2026-03-29T06:52:52Z
sha: afe8751

🎯T13:
  gap: close
  assessment: "iOS VoiceManager complete. Token proxy and interruption handling implemented. Remaining: real-device testing on Pippa."
  read:
    - internal/server/server.go
    - internal/server/chat.go
    - internal/claude/process.go

🎯T11:
  gap: converging
  assessment: "T11.1 achieved (16 props). T11.2 not started. Visual verification outstanding."
  read:
    - internal/ui/schema.go
    - ios/Jevon/Views/ServerView.swift

🎯T11.1:
  gap: achieved
  assessment: "All 16 props implemented in schema.go and ServerView.swift."
  read:
    - internal/ui/schema.go
    - ios/Jevon/Views/ServerView.swift

🎯T11.2:
  gap: not started
  assessment: "None of the 25 Phase 2 props implemented."
  read:
    - internal/ui/schema.go

🎯T12:
  gap: significant
  assessment: "iOS scaffolding exists (ChevronGestureRecognizer, SafeModeView). Control channel stub in server.go but rollback/snapshots return errors. No script_versions table."
  read:
    - internal/server/server.go
    - internal/db/db.go

🎯T8:
  gap: not started
  assessment: "Revised to stateless worker dispatch. No sub-targets achieved."
  read:
    - internal/session/session.go

🎯T8.1:
  gap: not started
  assessment: "No jwork MCP tool. No on-demand claude -p spawning."
  read:
    - internal/session/session.go

🎯T10:
  gap: significant
  assessment: "SyncManager compiles with full API. Protocol not yet converted to pure sqlpipe. No tests."
  read:
    - internal/sync/sync.go

🎯T9:
  gap: significant
  assessment: "Server-side Lua works. Client LuaRuntime.swift exists. Mid-pivot to client-side."
  read:
    - internal/ui/schema.go
    - ios/Jevon/Views/ServerView.swift
    - ios/Jevon/Models/LuaRuntime.swift

🎯T7:
  gap: close
  assessment: "Phases 1-3 implemented. Secure channel (needs T5) and visual verification remain."
  read:
    - ios/Jevon/Views/ChatView.swift

🎯T14:
  gap: not started
  assessment: "Protocol framework achieved (T15). Pairing ceremony not yet wired."
  read: []

🎯T16:
  gap: not started
  assessment: "Blocked by T8. Plan exists in docs/plans/active-work-dashboard.md."
  read: []

🎯T16.1:
  gap: not started
  assessment: "Plan exists. Parent blocked by T8."
  read: []

🎯T17:
  gap: not started
  assessment: "New target. No implementation yet."
  read: []

🎯T17.1:
  gap: not started
  assessment: "No scaffold. ge submodule exists but no jevons-ui binary."
  read: []

🎯T5:
  gap: not started
  assessment: "internal/auth is a stub. No mTLS, no QR provisioning."
  read: []

🎯T6:
  gap: not started
  assessment: "bypassPermissions still in session.go. No confirmation routing."
  read:
    - internal/session/session.go
-->
