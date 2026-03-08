// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ContentView: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        Group {
            switch connection.state {
            case .disconnected, .error:
                ConnectView()
            case .connecting:
                ProgressView("Connecting...")
            case .connected:
                ChatView()
            }
        }
    }
}
