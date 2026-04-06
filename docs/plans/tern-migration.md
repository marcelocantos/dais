# Tern Migration Plan

**Goal:** Tern becomes the sole communication path between the iOS app
and jevonsd. No more WebSocket fallback. Tern handles relay connections
and automatically upgrades to direct LAN when both peers are on the
same network.

**Prerequisite:** Bump tern to v0.10.0 (Go and SPM). v0.10.0 adds
`LANServer`, `Config` struct (replaces variadic options), and automatic
LAN upgrade.

## Current Status

**Go side (completed):** 
- Tern bumped to v0.10.0 in go.mod.
- `internal/server/relay.go` migrated to `tern.Config` + `NewLANServer` (stores `lanSrv` on Server). Uses `ternWriter` and updated send/recv. (Steps 1-2 complete.)
- `internal/server/pairing.go` added with `LoadOrGenerateKeyPair()` and stub `handlePairing()` that calls `SetChannel(nil)`. Keypair persisted in `~/.jevons/keypair.json`.
- QR updated to JSON format (`{"relay": "...", "id": "...", "pub": "..." }`) in `cmd/jevonsd/main.go`. Calls `srv.LoadOrGenerateKeyPair()` and `ConnectRelay()`.
- Server struct has `lanSrv`, `serverKP`, `pubKeyBase64`.

**iOS side (partial):**
- `JevonBridge.swift` has `BridgeMode.relay` using `TernConn.connect()`, receive loop with basic E2E support (but channel not set).
- `Transport.swift` abstracts WS and QUICTransport.
- Tern SPM at 0.1.0 (needs update; Go uses 0.10.0).
- QRScannerView.swift and Connection.swift still use old WebSocket/jevons:// parsing and WS transport.
- Voice not routed through tern (TODO in JevonBridge).
- No full key exchange or SetChannel usage on iOS.

**Remaining:** Key exchange integration, full iOS tern migration, Swift tern enhancements for LAN/SetChannel, WS cleanup, web UI bundling.

## Current State (historical)

### Go side (jevonsd)
- `internal/server/relay.go`: `ConnectRelay()` calls `tern.Register()`
  with the **old variadic options API** (`tern.WithTLS(...)`,
  `tern.WithToken(...)`, etc.). Must migrate to `tern.Config{}`.
- Relay connection bridges to the `remoteConn` system (same as
  `/ws/remote` WebSocket clients). Sends init, history, Lua scripts.
- No encryption (`SetChannel` never called).
- No `LANServer` — direct LAN access is via HTTP/WebSocket on port
  13705, completely separate from tern.
- Voice bridge (`/ws/voice`) is a WebSocket endpoint — not accessible
  through the relay.

### iOS side
- `Connection.swift`: connects to jevonsd via WebSocket
  (`ws://host:port/ws/remote`). Handles reconnect, state machine,
  sync protocol. **Does not use tern at all.**
- `JevonBridge.swift`: has a `BridgeMode.relay` path that imports
  `Tern` and uses `TernConn.connect()`, but this is skeletal — never
  exercised in practice. The `.direct` WebSocket path is what runs.
- `TernConn` (in tern's Swift package) does **not** have LAN upgrade
  support, `SetChannel`, or control message handling. It only does
  basic relay send/recv.

### QR code flow
- jevonsd prints a QR containing `jevons://<LAN_IP>:13705` (direct
  mode) or a relay WebSocket URL.
- iOS `QRScannerView` decodes it → `Connection.connect(to:port:)` →
  WebSocket.

## Target State

1. **jevonsd always starts a tern LANServer** (listening on a random
   QUIC port). Also always registers with the relay (or supports
   relay-less mode where only LAN is available).
2. **QR code encodes relay URL + instance ID + jevonsd's public key.**
   No more `jevons://` scheme or direct HTTP URLs.
3. **iOS connects via tern only.** `TernConn.connect()` to the relay.
   After key exchange and `SetChannel`, tern automatically discovers
   the LAN path and upgrades transparently.
4. **All communication flows through the tern `Conn`** — chat messages
   on the primary stream, voice on a named StreamChannel or
   DatagramChannel.
5. **E2E encryption** on all traffic. Relay never sees plaintext.

## Migration Steps

### Step 1: Bump tern to v0.10.0 [COMPLETED on Go side]

**Go:** [x] Bumped to v0.10.0 in go.mod, tidy done.

**SPM:** [ ] Update ios/project.yml from 0.1.0 to match (note: Swift package versions differ).

**Build:** Verified on Go; iOS tern version lags.

### Step 2: Migrate relay.go to Config struct + add LANServer [COMPLETED]

### Step 2: Migrate relay.go to Config struct + add LANServer [COMPLETED]

**File:** `internal/server/relay.go:34`

- Now uses `NewLANServer`, `tern.Config` with LANServer, stores `s.lanSrv`.
- `ternWriter` adapts `Send`/`Recv` (no ctx on Recv in current impl).
- sendJSON updated to use `conn.Send` with timeout ctx.
- `handlePairing` not yet wired into receive loop (see Step 3).

See current implementation in `internal/server/relay.go:29`.

### Step 3: Add key exchange and encryption to jevonsd [PARTIAL]

**File:** `internal/server/pairing.go` [x] Keypair load/generate implemented.

- `LoadOrGenerateKeyPair()` done, called from main.go.
- `handlePairing` stub: loads keypair, `conn.SetChannel(nil)` to enable LAN, but no full ECDH/HKDF, no confirmation code, no clientPubKey handling.
- Not yet called from `relay.go` receive loop (TODO: listen for pairing message from client).
- QR includes "pub" key [x].
- Full crypto (using tern/crypto?) and PairingRecord to DB pending. See targets.md for 🎯T14 pairing ceremony.

### Step 4: Update QR code format [PARTIAL]

**Current (legacy):** `jevons://...` or ws:// still supported in scanner but QR now JSON.

**New:** JSON with "relay", "id", "pub" [x] implemented in `cmd/jevonsd/main.go:685`.

Update iOS `QRScannerView.swift` and `ConnectView.swift` / `Connection.swift` to parse JSON QR and use relay mode with pubkey for key exchange [pending].

### Step 5: Replace Connection.swift with tern [PARTIAL]

**Status:** JevonBridge.swift and Transport.swift have basic relay/TernConn support (connect, recv loop, E2E stub). Connection.swift still WS-only.

Partial implementation in `JevonBridge.swift:199` for relay mode. Full replacement of state machine, sync protocol, control msgs pending. Key exchange not implemented on iOS.

### Step 6: Route voice through tern

Voice audio currently goes through a separate `/ws/voice` WebSocket.
Through the relay, this endpoint is unreachable.

**Option A — StreamChannel("voice"):**
- Reliable, ordered. Higher latency.
- jevonsd accepts a StreamChannel named "voice", bridges to the Grok
  Realtime session.
- Binary PCM16 frames + JSON control messages multiplexed on the
  channel (use type prefix byte).

**Option B — DatagramChannel("voice"):**
- Unreliable, low-latency. Better for real-time audio.
- Lost packets = brief audio glitch (acceptable for voice).
- But JSON control messages (transcripts, status) need reliable
  delivery → send those on the primary stream.

**Recommended: Option B** with a split:
- `DatagramChannel("voice-audio")` for PCM16 frames (both directions).
- Voice control messages (start/stop/status/transcript) on the
  primary stream with a `"type":"voice_*"` envelope.

**jevonsd side changes:**
- In `voice.go`, replace the `/ws/voice` WebSocket handler with a
  tern channel listener.
- When a client sends `{"type":"voice_start"}` on the primary stream,
  jevonsd opens the Grok Realtime session and starts bridging audio
  from `DatagramChannel("voice-audio")`.
- Grok's audio output goes back through the same datagram channel.
- Transcripts and status sent on the primary stream.

**iOS side changes (`JevonBridge.swift`):**
- `startVoice()` sends `{"type":"voice_start"}` on the primary
  stream, then starts mic capture and sends PCM16 via the datagram
  channel.
- Incoming datagrams on `voice-audio` are buffered and played natively.
- Transcript/status events arrive on the primary stream.

### Step 7: Add LAN upgrade + SetChannel to Swift TernConn

The Swift `TernConn` in tern's SPM package needs:

1. **`setChannel(_ channel: E2EChannel)`** — enable encrypt/decrypt on
   send/recv. Should mirror Go's `SetChannel`.
2. **Control message handling** — parse `lanOffer` arriving on the
   encrypted stream, dial the LAN address, complete the
   challenge/response, swap transport.
3. **`LAN: Bool` config** — opt into LAN upgrade.

This is work in the **tern repo**, not jevons. File a target or issue
on tern if not already tracked (🎯T1.8 may cover this — check
`tern/docs/targets.md`).

### Step 8: Remove WebSocket fallback

Once all paths work through tern:
1. Remove `/ws/remote` handler from `server.go`.
2. Remove `Connection.swift`'s WebSocket code (or keep as dead code
   for reference during migration).
3. Remove the `jevons://` URL scheme.
4. Remove the `httpBaseURL` / direct HTTP loading path from
   `ContentView.swift`.
5. The WKWebView loads the web UI HTML **bundled in the app** (not
   fetched from jevonsd). All data flows through the JS bridge →
   TernConn. This means the web UI HTML/JS/CSS must be embedded in
   the iOS app bundle.

### Step 9: Bundle web UI in iOS app

Since the WKWebView can no longer fetch from jevonsd (tern doesn't
serve HTTP), the web UI must be bundled:
1. Copy `web/index.html` and `web/scripts/` into the Xcode project
   as bundle resources.
2. Load via `webView.loadFileURL(bundleURL, allowingReadAccessTo:
   bundleDir)`.
3. The web UI detects native mode (`window.webkit.messageHandlers`)
   and uses the JS bridge — no network requests to jevonsd.

## Execution Order (updated)

Steps 1-2, partial 3-4 completed on Go side. iOS partial in Bridge/Transport.

**Recommended next:**
1. Integrate handlePairing and full key exchange in relay.go + pairing.go (complete Step 3).
2. Update iOS QR parsing and Connection to use new JSON + relay mode with pubkey.
3. Complete voice over tern (Step 6) and E2E in JevonBridge.
4. Bump iOS tern SPM and implement SetChannel/LAN in tern Swift (Step 7).
5. Steps 8-9 cleanup once stable.
6. Verify LAN upgrade and remove WS fallback.

See 🎯T14 in docs/targets.md for related pairing work.

## Files Affected (updated)

### Go (jevonsd)
- [x] `go.mod` — tern v0.10.0
- [x] `internal/server/relay.go` — Config, LANServer, ternWriter
- [x] `internal/server/pairing.go` — keypair (partial)
- [x] `internal/server/server.go` — fields, WS still present
- [ ] `cmd/jevonsd/main.go` — QR done, integrate pairing
- [ ] `internal/server/voice.go` — update for tern channels
- [ ] remove /ws/remote

### iOS
- [partial] `Models/JevonBridge.swift`, `Models/Transport.swift` — relay skeleton
- [ ] `Views/QRScannerView.swift` — JSON parser
- [ ] `Models/Connection.swift` — full tern integration
- [ ] bump tern in `project.yml`
- [ ] bundle web UI, update ContentView/WebUIView
- [ ] JevonBridge voice over tern

### Tern repo (separate)
- Update Swift package for SetChannel, LAN upgrade, E2EChannel support.
