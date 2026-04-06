// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
#if canImport(Network)
import Pigeon
#endif

/// A received message from the transport layer.
enum TransportMessage: Sendable {
    case text(String)
    case binary(Data)
}

/// Abstraction over WebSocket and QUIC relay transports.
protocol Transport: Sendable {
    func send(text: String) async throws
    func send(binary: Data) async throws
    func receive() async throws -> TransportMessage
    func close()
}

// MARK: - WebSocket Transport

/// WebSocket transport using URLSessionWebSocketTask.
final class WSTransport: Transport, @unchecked Sendable {
    private let task: URLSessionWebSocketTask

    init(url: URL) {
        let session = URLSession(configuration: .default)
        task = session.webSocketTask(with: url)
        task.resume()
    }

    func send(text: String) async throws {
        try await task.send(.string(text))
    }

    func send(binary data: Data) async throws {
        try await task.send(.data(data))
    }

    func receive() async throws -> TransportMessage {
        let msg = try await task.receive()
        switch msg {
        case .string(let s): return .text(s)
        case .data(let d): return .binary(d)
        @unknown default: throw TransportError.unknown
        }
    }

    func close() {
        task.cancel(with: .goingAway, reason: nil)
    }
}

// MARK: - QUIC Relay Transport

#if canImport(Network)

/// QUIC relay transport using PigeonConn from the Tern package.
final class QUICTransport: Transport, @unchecked Sendable {
    private let conn: PigeonConn

    init(conn: PigeonConn) {
        self.conn = conn
    }

    /// Connect to a tern relay as a client.
    static func connect(host: String, port: UInt16, instanceID: String) async throws -> QUICTransport {
        let conn = try await PigeonConn.connect(host: host, port: port, instanceID: instanceID)
        return QUICTransport(conn: conn)
    }

    func send(text: String) async throws {
        try await conn.send(Data(text.utf8))
    }

    func send(binary data: Data) async throws {
        try await conn.send(data)
    }

    func receive() async throws -> TransportMessage {
        let data = try await conn.recv()
        // Try interpreting as UTF-8 text first (JSON messages).
        // Fall back to binary (sqlpipe sync frames).
        if let text = String(data: data, encoding: .utf8),
           text.first == "{" || text.first == "[" {
            return .text(text)
        }
        return .binary(data)
    }

    func close() {
        conn.close()
    }
}

#endif

// MARK: - Errors

enum TransportError: LocalizedError {
    case unknown
    case invalidURL(String)

    var errorDescription: String? {
        switch self {
        case .unknown: "Unknown transport message type"
        case .invalidURL(let url): "Invalid URL: \(url)"
        }
    }
}
