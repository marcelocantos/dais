# Mobile App Architecture

## Overview

The Jevons mobile app is a **native iOS app** (SwiftUI) that connects to
jevonsd's WebSocket endpoint (`/ws/remote`). It is a command/chat
interface — not a ge rendering client. The ge player is a separate
dev-only tool.

## Tech Stack

- **Language**: Swift 6
- **UI**: SwiftUI
- **Target**: iOS 17+ (primary device: Pippa, iPad Air 5th gen)
- **Networking**: URLSessionWebSocketTask (built-in, no dependencies)
- **Build**: Xcode project (no ge, no Dawn, no SDL)

## Connection Model

### Discovery

The user connects to jevonsd by scanning a QR code displayed in the
terminal when jevonsd starts. The QR encodes:

```
jevons://<host>:<port>
```

The app uses AVFoundation for QR scanning (same pattern as the ge
iOS player's `QRScanner.mm`).

**Future (🎯T5):** mTLS client certificates provisioned via the QR
code. For now, connections are unauthenticated.

### WebSocket Protocol

Connects to `ws://<host>:<port>/ws/remote`. The protocol is JSON text
frames:

**Server → Client:**

| Type | Fields | Description |
|------|--------|-------------|
| `init` | `version` | Server greeting |
| `history` | `entries[]` (role, text, timestamp) | Transcript replay on connect |
| `text` | `content` | Streaming text fragment |
| `status` | `state` ("thinking" / "idle") | Jevons activity state |
| `user_message` | `text`, `timestamp` | Echo of a user's message |

**Client → Server:**

| Type | Fields | Description |
|------|--------|-------------|
| `message` | `text` | User command |

**Future (🎯T6):**

| Type | Fields | Description |
|------|--------|-------------|
| `confirm_response` | `id`, `approved`, `reason?` | Permission decision |

### Reconnection

Auto-reconnect with exponential backoff (1s → 2s → 4s → 8s cap).
Reset on successful connection. Show connection status in the UI.

## Core Screens

### 1. Connect (QR Scanner)

- Full-screen camera viewfinder
- Scans for `jevons://` QR codes
- Remembers last-connected server (UserDefaults)
- Manual entry fallback: host:port text field

### 2. Chat (Main)

- Scrolling message list (user messages right-aligned, Jevon
  left-aligned, like iMessage)
- Text input bar at bottom with send button
- Status indicator: "Thinking..." when Jevons is processing
- Streaming text appended to the current Jevons message bubble
- History loaded on connect

### 3. Workers (Secondary)

- List of worker sessions (from future `jevons_list_sessions` exposure)
- Each row: name, status badge (idle/running/error), last activity
- Tap to see worker detail (last result, workdir)
- Not implemented in v1 — placeholder tab

## Project Structure

```
ios/
├── Jevon.xcodeproj/
├── Jevon/
│   ├── JevonApp.swift           # App entry point
│   ├── Models/
│   │   ├── ServerMessage.swift  # Codable types for WS protocol
│   │   └── Connection.swift     # WebSocket connection manager
│   ├── Views/
│   │   ├── ConnectView.swift    # QR scanner + manual entry
│   │   ├── ChatView.swift       # Main chat interface
│   │   └── WorkersView.swift    # Worker list (placeholder)
│   └── Info.plist
└── docs/                        # This file lives here (project root)
```

No `ge/` dependency. No C++. No Dawn. Pure Swift/SwiftUI.

## How It Differs from the ge Player

| Concern | ge Player | Jevons Mobile App |
|---------|-----------|------------------|
| Purpose | Render WebGPU commands | Send commands, view responses |
| Protocol | Dawn wire (TCP, binary) | jevonsd WebSocket (JSON) |
| Rendering | Full GPU pipeline | Standard SwiftUI views |
| Dependencies | Dawn, SDL3 (cross-compiled) | None (system frameworks) |
| Build | CMake → Xcode | Native Xcode project |
| App-specific | No (renders any ge app) | Yes (Jevons only) |

## Server-Side Changes

jevonsd already has everything the v1 mobile app needs:

- `/ws/remote` endpoint with init, history, text, status messages
- Transcript persistence in SQLite
- Broadcast to all connected clients

### Needed additions

1. **QR code on startup**: jevonsd should print a `jevons://host:port`
   QR code to stderr (like ge's wire session does for `squz-remote://`).
   Use the same `qrcodegen` library (already vendored in ge).

2. **Worker list endpoint** (v2): Expose session list over WebSocket
   so the Workers tab can display it. New message type:
   ```json
   {"type": "sessions", "sessions": [...]}
   ```

## Implementation Plan

### Phase 1: Minimal chat app

1. Create Xcode project with SwiftUI
2. Implement WebSocket connection manager
3. Build ChatView with message list and input
4. Manual connection (hardcoded or text field — no QR yet)
5. Test against running jevonsd

### Phase 2: QR discovery

1. Add AVFoundation QR scanner
2. Add `jevons://` QR code generation to jevonsd startup
3. Remember last server in UserDefaults

### Phase 3: Polish

1. Reconnection with backoff
2. Connection status indicator
3. Worker list tab (placeholder or real)
4. iPad layout optimisation for Pippa

## Open Questions

- Should the app support multiple server connections (switch between
  different jevonsd instances)?
- Voice input: should the app capture audio and send it to jevonsd's
  voice pipeline, or use iOS speech-to-text locally?
- Notifications: should jevonsd push notifications for confirmation
  requests when the app is backgrounded?
