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
            ProgressView("Connecting...")
        case .connected:
            ChatView()
        case .error:
            // Show chat view if we were previously connected (reconnecting),
            // otherwise show connect view.
            if connection.hasConnected {
                ChatView()
            } else {
                ConnectView()
            }
        }
    }
}
