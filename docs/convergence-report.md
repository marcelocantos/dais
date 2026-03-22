# Convergence Report

Standing invariants: all green.
- Tests: all passing (`go test ./...` — 11 packages with tests, all OK)
- CI: on master, no open PRs.

## Movement

- 🎯T13: (new target — first evaluation)
- 🎯T12: (new target — first evaluation)
- 🎯T14: (new target — first evaluation)
- 🎯T11: (unchanged)
- 🎯T8: (unchanged)
- 🎯T10: (unchanged)
- 🎯T9: (unchanged)
- 🎯T7: (unchanged)
- 🎯T5: (unchanged)
- 🎯T6: (unchanged)

## Gap Report

### 🎯T13 Full-duplex voice input  [weight 1.6]
Gap: significant
iOS `VoiceManager.swift` is well-implemented: local VAD via `AVAudioEngine`, OpenAI Realtime API WebSocket with semantic VAD, 24kHz PCM16 streaming, silence timeout, ephemeral token request flow. However, the server-side ephemeral token proxy endpoint (`api/realtime/token`) does not exist in jevond. Interruption handling (cancel current Claude process and restart on new utterance) is not implemented. No tests for the voice path.

### 🎯T11 Lua-controllable SwiftUI modifier surface  [weight 1.6]  (visual)
Gap: converging (1/2 sub-targets achieved)

  [x] 🎯T11.1 Essential modifiers (Phase 1) — achieved
  [ ] 🎯T11.2 Useful modifiers (Phase 2) — not started: none of the 25 Phase 2 props found in schema.go or ServerView.swift

Visual verification outstanding — target is tagged `visual` but no verification recorded. Run on simulator/device and confirm UI before marking achieved.

### 🎯T12 Script versioning and safe mode  [weight 1.6]
Gap: significant
iOS scaffolding exists: `ChevronGestureRecognizer.swift` (two-finger chevron gesture) and `SafeModeView.swift` (pure-Swift safe mode screen with snapshot listing and rollback UI). However, the server-side is missing: no `script_versions` table in the DB, no snapshot/rollback handlers in jevond, no control channel implementation. `SafeModeView` calls `connection.sendControl()` which does not appear to be implemented. Gates 🎯T9.

### 🎯T8 Stateless worker dispatch  [weight 1.2]
Gap: not started (0/3 sub-targets achieved)

  [ ] 🎯T8.1 Worker dispatch foundation — not started: no `jwork` MCP tool, no on-demand `claude -p` spawning.
  [ ] 🎯T8.2 Observability — not started (blocked by 🎯T8.1)
  [ ] 🎯T8.3 Execution safety absorbed (doit) — not started (blocked by 🎯T8.1)

### 🎯T10 sqlpipe-based state sync  [weight 1.0]  (status only)
Status: converging
Changed files overlap: `internal/sync/sync.go` — may be affected.

### 🎯T9 Server-driven UI for mobile app  [weight 1.0]  (visual)  (status only)
Status: converging
Changed files overlap: `internal/ui/schema.go`, `ios/Jevon/Views/ServerView.swift` — may be affected.

### 🎯T7 Mobile app for Jevon  [weight 1.0]  (visual)  (status only)
Status: converging
Changed files overlap: `ios/Jevon/Views/ChatView.swift` — may be affected.

### 🎯T14 Onboarding and device pairing  [weight 1.0]  (status only)
Status: identified
Protocol state machine framework (🎯T15) achieved — provides foundation for the pairing ceremony.

### 🎯T5 Authentication implemented  [weight 0.6]  (status only)
Status: identified
No changed files overlap. `internal/auth` remains a stub.

### 🎯T6 Permission model enforced  [weight 0.6]  (status only)
Status: identified
No changed files overlap. `--dangerously-skip-permissions` still present.

## Recommendation

Work on: **🎯T13 Full-duplex voice input**
Reason: Tied for highest effective weight (1.6) with 🎯T11, 🎯T12, and 🎯T8.1. 🎯T13 has the most progress already — the iOS client is substantially implemented. The remaining work (server-side token proxy, interruption handling) is well-scoped and builds on existing jevond infrastructure. Closing this target adds a transformative UX capability (hands-free voice interaction), whereas 🎯T11.2 is incremental and 🎯T8.1 is greenfield.

## Suggested action

Add the ephemeral token proxy endpoint to jevond: a `POST /api/realtime/token` handler that requests a short-lived session token from the OpenAI API using the server's API key (stored in Keychain or env var) and returns it to the iOS client. This unblocks the full voice pipeline without exposing the API key to the device.

<!-- convergence-deps
evaluated: 2026-03-22T00:26:12Z
sha: 1590194

🎯T13:
  gap: significant
  assessment: "iOS VoiceManager fully implemented (VAD, OpenAI Realtime, PCM16 streaming). Server-side token proxy and interruption handling missing."
  read:
    - ios/Jevon/Models/VoiceManager.swift

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
  assessment: "iOS scaffolding exists (ChevronGestureRecognizer, SafeModeView). Server-side versioning, control channel, and sendControl not implemented."
  read:
    - ios/Jevon/Views/ChevronGestureRecognizer.swift
    - ios/Jevon/Views/SafeModeView.swift
    - ios/Jevon/Models/Connection.swift
    - internal/db/schema.go

🎯T8:
  gap: not started
  assessment: "Revised to stateless worker dispatch. No sub-targets achieved."
  read:
    - internal/session/session.go

🎯T8.1:
  gap: not started
  assessment: "No jwork MCP tool. No on-demand claude -p spawning. Session struct basic."
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
    - internal/ui/lua.go
    - internal/ui/schema.go
    - ios/Jevon/Models/LuaRuntime.swift
    - ios/Jevon/Views/ServerView.swift

🎯T7:
  gap: close
  assessment: "Phases 1-3 implemented. Secure channel (needs T5) and visual verification remain."
  read:
    - ios/Jevon/Views/ChatView.swift

🎯T14:
  gap: not started
  assessment: "Protocol framework achieved (T15). Pairing ceremony not yet wired."
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
