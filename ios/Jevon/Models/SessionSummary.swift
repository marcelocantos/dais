// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation

struct SessionSummary: Codable, Identifiable, Sendable {
    let id: String
    let name: String
    let status: Status
    let workdir: String
    let active: Bool?
    let score: Double?

    enum Status: String, Codable, Sendable {
        case idle
        case running
        case error
        case stopped
    }
}

struct SessionDetail: Codable, Sendable {
    let id: String
    let name: String
    let status: SessionSummary.Status
    let workdir: String
    let lastResult: String

    enum CodingKeys: String, CodingKey {
        case id, name, status, workdir
        case lastResult = "last_result"
    }
}
