# Tern Migration Plan

**Goal:** Tern becomes the sole communication path between the iOS app
and jevond. No more WebSocket fallback. Tern handles relay connections
and automatically upgrades to direct LAN when both peers are on the
same network.

**Prerequisite:** Bump tern to v0.10.0 (Go and SPM). v0.10.0 adds
`LANServer`, `Config` struct (replaces variadic options), and automatic
LAN upgrade.

## Current State

### Go side (jevond)
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
- `Connection.swift`: connects to jevond via WebSocket
  (`ws://host:port/ws/remote`). Handles reconnect, state machine,
  sync protocol. **Does not use tern at all.**
- `JevonBridge.swift`: has a `BridgeMode.relay` path that imports
  `Tern` and uses `TernConn.connect()`, but this is skeletal — never
  exercised in practice. The `.direct` WebSocket path is what runs.
- `TernConn` (in tern's Swift package) does **not** have LAN upgrade
  support, `SetChannel`, or control message handling. It only does
  basic relay send/recv.

### QR code flow
- jevond prints a QR containing `jevon://<LAN_IP>:13705` (direct
  mode) or a relay WebSocket URL.
- iOS `QRScannerView` decodes it → `Connection.connect(to:port:)` →
  WebSocket.

## Target State

1. **jevond always starts a tern LANServer** (listening on a random
   QUIC port). Also always registers with the relay (or supports
   relay-less mode where only LAN is available).
2. **QR code encodes relay URL + instance ID + jevond's public key.**
   No more `jevon://` scheme or direct HTTP URLs.
3. **iOS connects via tern only.** `TernConn.connect()` to the relay.
   After key exchange and `SetChannel`, tern automatically discovers
   the LAN path and upgrades transparently.
4. **All communication flows through the tern `Conn`** — chat messages
   on the primary stream, voice on a named StreamChannel or
   DatagramChannel.
5. **E2E encryption** on all traffic. Relay never sees plaintext.

## Migration Steps

### Step 1: Bump tern to v0.10.0

**Go:**
```bash
cd /Users/marcelo/work/github.com/marcelocantos/jevon
go get github.com/marcelocantos/tern@v0.10.0
go mod tidy
```

**SPM** (ios/Jevon.xcodeproj/project.pbxproj):
Change `minimumVersion = 0.9.0` → `minimumVersion = 0.10.0`.

**Build both** to verify API compatibility. The Go side will break
because `tern.Register()` no longer takes variadic options — it takes
`tern.Config{}`. Fix `relay.go` first (Step 2).

### Step 2: Migrate relay.go to Config struct + add LANServer

**File:** `internal/server/relay.go`

Replace:
```go
var opts []tern.Option
opts = append(opts, tern.WithTLS(&tls.Config{InsecureSkipVerify: true}))
if token != "" {
    opts = append(opts, tern.WithToken(token))
}
if instanceID != "" {
    opts = append(opts, tern.WithInstanceID(instanceID))
}
conn, err := tern.Register(ctx, relayURL, opts...)
```

With:
```go
lanSrv, err := tern.NewLANServer("", nil) // random port, self-signed TLS
if err != nil {
    return "", fmt.Errorf("LAN server: %w", err)
}

cfg := tern.Config{
    TLS:        &tls.Config{InsecureSkipVerify: true},
    Token:      token,
    InstanceID: instanceID,
    LANServer:  lanSrv,
}
conn, err := tern.Register(ctx, relayURL, cfg)
```

Store `lanSrv` on the Server struct so it can be closed on shutdown.

**Also update `sendJSON`** — `conn.Send()` signature may have changed
in v0.10.0 (check: does it still take `context.Context`?).

### Step 3: Add key exchange and encryption to jevond

**New file:** `internal/server/pairing.go` (or extend `relay.go`)

After registration:
1. Generate an `ecdh.PrivateKey` (X25519) at startup. Store the key
   pair persistently (in `~/.jevon/keypair.json` or SQLite).
2. Encode the **public key** in the QR code alongside relay URL and
   instance ID.
3. When the iOS client connects, it sends its public key as the first
   message on the primary stream.
4. jevond receives it, derives the session key via ECDH + HKDF:
   ```go
   rec := crypto.NewPairingRecord(clientInstanceID, relayURL, serverKP, clientPubKey)
   ch, _ := rec.DeriveChannel([]byte("server-to-client"), []byte("client-to-server"))
   conn.SetChannel(ch)
   ```
5. `SetChannel()` triggers automatic LAN advertisement to the client.
6. Derive and display a 6-digit confirmation code:
   ```go
   code, _ := crypto.DeriveConfirmationCode(serverKP.Public, clientPubKey)
   ```
   Display in jevond terminal. iOS displays it too. User visually
   confirms match (MitM protection).
7. Store `PairingRecord` in SQLite for reconnection without re-pairing.

### Step 4: Update QR code format

**Current:** `jevon://192.168.1.5:13705` or relay WebSocket URL.

**New:** JSON encoded in the QR:
```json
{
  "relay": "https://tern.fly.dev",
  "id": "abc123",
  "pub": "<base64 X25519 public key>"
}
```

Update `cmd/jevond/main.go` where `qr.Print()` is called to encode
this new format.

Update iOS `QRScannerView` to parse the new JSON format.

### Step 5: Replace Connection.swift with tern

**This is the biggest iOS change.** `Connection.swift` currently
manages:
- WebSocket connection + reconnect
- State machine (disconnected/connecting/connected/error)
- Binary sync protocol (sqlpipe)
- Control channel (exec_lua, screenshot, etc.)
- Subscription-based auto-render

Replace the WebSocket transport with `TernConn`:

1. **Connect:** `TernConn.connect(host:port:instanceID:)` using relay
   host/port and instance ID from QR.
2. **Key exchange:** Generate key pair, send public key, receive
   server's public key (already in QR), derive channel, set it.
   `TernConn` needs `SetChannel` added to the Swift side (see Step 7).
3. **Message framing:** Currently WebSocket distinguishes text vs
   binary frames. Over tern primary stream, use a 1-byte type prefix:
   - `0x00` + JSON = text message
   - `0x01` + bytes = binary (sync protocol)
   Or use separate tern channels: primary stream for JSON,
   DatagramChannel for sync, StreamChannel for voice.
4. **Reconnect:** On disconnect, reload `PairingRecord` from Keychain,
   reconnect to relay, derive channel from record (no re-pairing).
5. **State machine:** Keep the same states but driven by TernConn
   events instead of URLSessionWebSocketTask.

### Step 6: Route voice through tern

Voice audio currently goes through a separate `/ws/voice` WebSocket.
Through the relay, this endpoint is unreachable.

**Option A — StreamChannel("voice"):**
- Reliable, ordered. Higher latency.
- jevond accepts a StreamChannel named "voice", bridges to the Grok
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

**jevond side changes:**
- In `voice.go`, replace the `/ws/voice` WebSocket handler with a
  tern channel listener.
- When a client sends `{"type":"voice_start"}` on the primary stream,
  jevond opens the Grok Realtime session and starts bridging audio
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

This is work in the **tern repo**, not jevon. File a target or issue
on tern if not already tracked (🎯T1.8 may cover this — check
`tern/docs/targets.md`).

### Step 8: Remove WebSocket fallback

Once all paths work through tern:
1. Remove `/ws/remote` handler from `server.go`.
2. Remove `Connection.swift`'s WebSocket code (or keep as dead code
   for reference during migration).
3. Remove the `jevon://` URL scheme.
4. Remove the `httpBaseURL` / direct HTTP loading path from
   `ContentView.swift`.
5. The WKWebView loads the web UI HTML **bundled in the app** (not
   fetched from jevond). All data flows through the JS bridge →
   TernConn. This means the web UI HTML/JS/CSS must be embedded in
   the iOS app bundle.

### Step 9: Bundle web UI in iOS app

Since the WKWebView can no longer fetch from jevond (tern doesn't
serve HTTP), the web UI must be bundled:
1. Copy `web/index.html` and `web/scripts/` into the Xcode project
   as bundle resources.
2. Load via `webView.loadFileURL(bundleURL, allowingReadAccessTo:
   bundleDir)`.
3. The web UI detects native mode (`window.webkit.messageHandlers`)
   and uses the JS bridge — no network requests to jevond.

## Execution Order

Steps 1-4 can be done in one session (Go-side tern migration).
Steps 5-6 are the iOS migration (bigger, can be parallel with 7).
Step 7 is tern repo work (prerequisite for full iOS LAN upgrade).
Steps 8-9 are cleanup after everything works.

**Recommended sequence:**
1. Steps 1-2 (bump + Config migration) — small, unblocks everything
2. Step 4 (QR format) — needed for iOS to connect via tern
3. Step 3 (key exchange) — needed for encryption + LAN trigger
4. Steps 5-6 (iOS tern + voice) — main iOS work
5. Step 7 (Swift TernConn LAN) — tern repo, may need to happen
   alongside step 5
6. Steps 8-9 (cleanup + bundle) — final polish

## Files Affected

### Go (jevond)
- `go.mod` — bump tern
- `internal/server/relay.go` — Config struct, LANServer, key exchange
- `internal/server/server.go` — remove /ws/remote (Step 8)
- `internal/server/voice.go` — voice over tern channels
- `internal/server/pairing.go` — new: key exchange, PairingRecord
- `cmd/jevond/main.go` — QR format, LANServer lifecycle, keypair flag

### iOS
- `Jevon.xcodeproj/project.pbxproj` — bump tern SPM, add bundled web resources
- `Models/Connection.swift` — replace WebSocket with TernConn
- `Models/JevonBridge.swift` — update relay mode for voice channels
- `Views/ConnectView.swift` — parse new QR JSON format
- `Views/QRScannerView.swift` — decode new QR format
- `Views/ContentView.swift` — load bundled web UI, not HTTP URL
- `Views/WebUIView.swift` — loadFileURL instead of network URL

### Tern repo (separate)
- `Sources/Tern/TernRelay.swift` — SetChannel, LAN upgrade, control msgs
