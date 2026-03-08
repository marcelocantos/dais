// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation

struct ChatMessage: Identifiable, Sendable {
    enum Role: Sendable {
        case user
        case jevon
    }

    let id: UUID
    let role: Role
    let text: String
    let timestamp: Date
    let isStreaming: Bool

    init(role: Role, text: String, timestamp: Date = Date(), isStreaming: Bool = false) {
        self.id = UUID()
        self.role = role
        self.text = text
        self.timestamp = timestamp
        self.isStreaming = isStreaming
    }
}
