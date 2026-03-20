// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// Fetches and manages Claude Code worker sessions from jevond.
@Observable
@MainActor
final class SessionService {
    private(set) var sessions: [SessionSummary] = []
    private(set) var isLoading = false
    private(set) var error: String?

    private let baseURL: URL
    private var refreshTimer: Timer?

    init(baseURL: URL) {
        self.baseURL = baseURL
    }

    func startAutoRefresh() {
        fetchSessions()
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.fetchSessions()
            }
        }
    }

    func stopAutoRefresh() {
        refreshTimer?.invalidate()
        refreshTimer = nil
    }

    func fetchSessions() {
        let url = baseURL.appendingPathComponent("api/sessions")

        Task {
            isLoading = true
            defer { isLoading = false }
            do {
                let (data, _) = try await URLSession.shared.data(from: url)
                sessions = try JSONDecoder().decode([SessionSummary].self, from: data)
                error = nil
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    func killSession(id: String) async {
        let url = baseURL
            .appendingPathComponent("api/sessions")
            .appendingPathComponent(id)
            .appendingPathComponent("kill")
        var request = URLRequest(url: url)
        request.httpMethod = "POST"

        do {
            let _ = try await URLSession.shared.data(for: request)
            fetchSessions()
        } catch {
            self.error = error.localizedDescription
        }
    }

    func fetchDetail(id: String) async -> SessionDetail? {
        let url = baseURL
            .appendingPathComponent("api/sessions")
            .appendingPathComponent(id)
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            return try JSONDecoder().decode(SessionDetail.self, from: data)
        } catch {
            self.error = error.localizedDescription
            return nil
        }
    }
}
