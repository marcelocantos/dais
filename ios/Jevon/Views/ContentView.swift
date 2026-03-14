// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ContentView: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        switch connection.state {
        case .disconnected:
            ConnectView()
        case .connecting:
            if connection.hasConnected {
                // Reconnecting — keep showing the chat view to avoid flicker.
                ChatView()
            } else {
                ProgressView("Connecting...")
            }
        case .connected:
            ChatView()
        case .error:
            if connection.hasConnected {
                ChatView()
            } else {
                ConnectView()
            }
        }
    }
}
