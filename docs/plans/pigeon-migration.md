# Pigeon migration: adopt PairingArtifact + PairingHost + CredentialStore

**Triggering change (pigeon master, commit `c0688aa`, not yet tagged):**
Pigeon has gained three new primitives that exactly cover the device-pairing
shape jevons needs:

- `pigeon.PairingArtifact` (Go/Swift/Kotlin) — typed pairing envelope
  carrying `crypto.PairingRecord` + bearer token + `IssuedAt` + `ExpiresAt`.
  Canonical JSON encoding **and** canonical base64url text encoding (same
  payload, single-line — QR-friendly).
- `pigeon.CredentialStore` interface (`Save` / `Load` / `Delete` /
  `IsExpired`). Reference implementations: `FileCredentialStore` (Go,
  desktop), `KeychainCredentialStore` + `FileCredentialStore` (Swift),
  `EncryptedSharedPreferences` (Kotlin).
- `pigeon.PairingHost` server helper. `Mint(peerInstanceID) →
  (*PairingArtifact, *crypto.PairingRecord, error)` — wraps
  `IssueCredential` + `crypto.PairingRecord` minting + 30-day TTL +
  optional bearer token via `IssueToken`.
- `pigeon.ConnectWithArtifact(ctx, artifact, cfg)` — client-side connect
  using the persisted artifact. Applies token from artifact, derives the
  E2E channel from `record`, sets pairing record on the conn.
- `pigeon.ErrPairingExpired` — sentinel returned by `ConnectWithArtifact`
  past `ExpiresAt`. Callers route to a re-pair flow.
- `cmd/pigeon-pair` — CLI that mints an artifact for out-of-band injection
  (deploy script use case: `xcrun devicectl device copy to`).

**Goal:** delete jevons' bespoke key/QR/pairing code (`internal/server/pairing.go`,
the ad-hoc QR JSON in `cmd/jevonsd/main.go`, the iOS QR JSON parser,
manual `PigeonConn.connect(host, port, instanceID)` calls) and replace it
with a thin layer over the new pigeon primitives. The artifact carries
everything needed for reconnect; expiry triggers re-pair.

## Prerequisite

Pigeon master must be tagged (expected v0.19.0). Until then, do not bump
`go.mod` — keep this plan as a staging document. When v0.19.0 is published,
the bump unlocks the work below.

## Current state to dismantle

### Go side (jevonsd)
- `internal/server/pairing.go` — `KeyPair` struct, `LoadOrGenerateKeyPair`,
  `handlePairing` stub. **Delete entirely.** Replaced by `PairingHost.Mint`
  + a `crypto.PairingRecord` registry indexed by peer instance ID.
- `internal/server/server.go` — fields `serverKP`, `pubKeyBase64`, accessor
  `PubKeyBase64()`. **Delete.** No more raw X25519 keys held server-side.
- `cmd/jevonsd/main.go:684-724` — ad-hoc QR JSON
  `{"relay":"","id":"","pub":"..."}`. **Replace** with
  `artifact.MarshalText()` (single base64url token in the QR).
- `internal/server/relay.go` — `ConnectRelay(ctx, relayURL, token,
  instanceID)`. Stays but no longer manages key material; the relay
  registration is just transport — the auth side moves to PairingHost.

### iOS side
- `Models/Transport.swift` — `QUICTransport.connect(host, port, instanceID)`.
  **Replace** with `pigeon.ConnectWithArtifact(...)` driven by
  `KeychainCredentialStore`.
- `Models/JevonsBridge.swift` — `BridgeMode.relay(host:port:instanceID:)`,
  the manual `PigeonConn.connect(host, port, instanceID)` and the
  `e2eChannel: E2EChannel?` field. **Replace** with mode driven by a
  loaded `PairingArtifact`; the channel is set automatically by
  `ConnectWithArtifact`.
- `Views/QRScannerView.swift` — current parser handles
  `jevons://host:port` and the JSON `{"id","relay","pub"}`. **Replace**
  with `PairingArtifact.unmarshalText(scanned)` (or fall back to
  `unmarshal(jsonBytes)` if QR contained JSON for any reason).
- `JevonsApp.swift` — startup currently expects fresh QR each launch on
  device. **Add**: try `KeychainCredentialStore.load()`, then
  `ConnectWithArtifact`; on `ErrPairingExpired` (or no credential), show
  the QR scanner.

## Target state

### Server
1. On startup, jevonsd loads (or initialises) its server-side
   `crypto.PairingRecord` set from `~/.jevons/credentials.json` (one
   record per paired device, keyed by peer instance ID).
2. `--pair <instance-id>` mode: invoke `pigeon.PairingHost.Mint(id)`,
   persist the server record, render the artifact's text encoding as a
   QR on stderr. The user scans it on the iPad.
3. Default run mode: register with the relay (existing `ConnectRelay`),
   accept incoming connections, look up the matching server-side
   `PairingRecord` for each `auth_request` to validate the channel.
4. When a credential is past its `ExpiresAt`, drop the connection with a
   typed reason; the client routes to re-pair UI.

### Client (iOS)
1. App launch → `KeychainCredentialStore("com.marcelocantos.jevons.credential").load()`.
2. If found and not expired → `pigeon.ConnectWithArtifact(ctx, artifact,
   .init(lan: true))`. Receive loop runs against the resulting `Conn`.
3. If absent or `ErrPairingExpired` → show QR scanner.
4. On scan, decode via `PairingArtifact.unmarshalText`, save via the
   credential store, then `ConnectWithArtifact`.

### Out-of-band provisioning (xcrun deploy)
For developer-flow deploys, jevons's iOS deploy script can call
`pigeon-pair --relay=$RELAY --instance=$DEVICE_ID --ttl=720h
--format=text --out=/tmp/artifact.txt --server-record-out=/tmp/server.json`,
copy `artifact.txt` onto the device's app sandbox via `xcrun devicectl
device copy to`, register `server.json` with jevonsd, then launch the
app. First run loads the artifact and connects without ever scanning a
QR.

## Migration steps

1. **Wait for `pigeon` v0.19.0 tag.** This unlocks everything.
2. **Bump go.mod** `github.com/marcelocantos/pigeon` → v0.19.0; `go mod
   tidy`.
3. **Bump iOS SPM** `ios/project.yml` `Pigeon` → 0.19.0; regen Xcode
   project.
4. **Server: introduce credential store** —
   `internal/server/credentials.go` wraps `pigeon.FileCredentialStore`
   per-device (or just a single map saved to `~/.jevons/credentials.json`).
5. **Server: replace pairing.go** — delete `KeyPair` /
   `LoadOrGenerateKeyPair` / `handlePairing`. Add `PairFlow(instanceID
   string) → (artifactText, error)` using `PairingHost.Mint`.
6. **Server: replace QR generation** in `cmd/jevonsd/main.go` —
   `pigeon-pair`-style for `--pair <id>`, no QR at all in the default
   relay-only path.
7. **Server: wire auth on incoming connections** — server.go reads the
   peer instance ID from the connection, looks up its `PairingRecord`,
   and rejects unknown / expired peers. (Hooks into pigeon's auth
   sub-machine — design exact callback at implementation time; this is
   where the most uncertainty lives.)
8. **iOS: add `PigeonAccount.swift`** — small wrapper over
   `KeychainCredentialStore` that exposes `load`, `save`,
   `connectIfPossible(config)`. The bridge calls this rather than
   poking pigeon directly.
9. **iOS: rewrite QRScannerView** to decode via
   `PairingArtifact.unmarshalText`. Drop the legacy `jevons://` and
   `{"id","relay","pub"}` parsers.
10. **iOS: rewrite Transport / JevonsBridge** — collapse `BridgeMode.relay`
    + `BridgeMode.direct` into a single artifact-driven connect. The
    `e2eChannel` field becomes dead code (`ConnectWithArtifact` sets it
    on the conn).
11. **iOS: app startup** — load credential, attempt connect, fall back
    to scanner on `ErrPairingExpired` / `ErrNoCredential`.
12. **End-to-end smoke test** — `jevonsd --pair pippa-dev` on laptop,
    scan on iPad, send a chat message. Force-expire (set TTL=10s),
    confirm re-pair UI fires.
13. **Delete dead code** — old WS `/ws/remote` direct path,
    `BridgeMode.direct`, `Connection.swift` WS state machine if no
    longer reachable.

## Risk + open questions

- **Pigeon's auth sub-machine** is the part that connects the server's
  `PairingRecord` registry to incoming `Conn`s. The new `PairingHost`
  hands you a `serverRecord` to register, but the surface for "register
  this record so the relay accepts auth_request from peer X" is not
  yet documented in jevons's mental model — read pigeon's
  `pairing_host_test.go` and `connect_artifact_test.go` to confirm
  before writing server.go changes.
- **Voice over relay** is unchanged by this migration — still TODO
  (`JevonsBridge.startVoice` shows an error in relay mode). Track
  separately under 🎯T13.
- **Existing keypair file** at `~/.jevons/keypair.json` becomes
  orphaned. Add a one-time deletion in the migration (or just leave it).

## Out of scope

- Brew service install (`jevonsd` as a brew service) — separate part of 🎯T14.
- Onboarding UX polish (CLI prompts, confirmation code rendering) —
  separate part of 🎯T14.
- mTLS via `internal/auth` — landed already (commit `a67bcbe`); will
  coexist with pigeon-based pairing for the LAN HTTP listener.
