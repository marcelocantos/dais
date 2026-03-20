// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct SessionListView: View {
    let baseURL: URL
    @State private var service: SessionService
    @State private var selectedSession: SessionSummary?
    @State private var detail: SessionDetail?
    @State private var loadingDetail = false
    @Environment(\.dismiss) private var dismiss

    init(baseURL: URL) {
        self.baseURL = baseURL
        self._service = State(initialValue: SessionService(baseURL: baseURL))
    }

    var body: some View {
        NavigationStack {
            Group {
                if service.sessions.isEmpty && !service.isLoading {
                    ContentUnavailableView(
                        "No Sessions",
                        systemImage: "terminal",
                        description: Text("No Claude Code workers are running.")
                    )
                } else {
                    sessionList
                }
            }
            .navigationTitle("Sessions")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .sheet(item: $selectedSession) { session in
                SessionDetailSheet(session: session, detail: detail, isLoading: loadingDetail)
            }
        }
        .onAppear { service.startAutoRefresh() }
        .onDisappear { service.stopAutoRefresh() }
    }

    private var sessionList: some View {
        List {
            ForEach(service.sessions) { session in
                SessionRow(session: session)
                    .contentShape(Rectangle())
                    .onTapGesture {
                        loadingDetail = true
                        detail = nil
                        selectedSession = session
                        Task {
                            detail = await service.fetchDetail(id: session.id)
                            loadingDetail = false
                        }
                    }
                    .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                        Button(role: .destructive) {
                            Task { await service.killSession(id: session.id) }
                        } label: {
                            Label("Kill", systemImage: "xmark.circle")
                        }
                    }
            }
        }
        .refreshable { service.fetchSessions() }
    }
}

// MARK: - Session Row

private struct SessionRow: View {
    let session: SessionSummary

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                styledPath(session.name)
                    .font(.body.weight(.medium))
                Spacer()
                StatusBadge(status: session.status)
            }
            if session.workdir != session.name {
                styledPath(session.workdir)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
        }
        .padding(.vertical, 2)
    }
}

/// Render an abbreviated path with host icon for repo paths.
private func styledPath(_ path: String) -> Text {
    let abbr = abbreviatePath(path)

    switch abbr.host {
    case .github:
        return Text(Image("github-mark")) + Text(" \(abbr.display)")
    case .other(let name):
        return Text(name) + Text("⬥").foregroundColor(.blue) + Text(abbr.display)
    case .none:
        return Text(abbr.display)
    }
}

private struct AbbreviatedPath {
    enum Host {
        case github
        case other(String)
        case none
    }
    let host: Host
    let display: String
}

/// Abbreviate a file path for display.
private func abbreviatePath(_ path: String) -> AbbreviatedPath {
    let parts = path.split(separator: "/")
    guard parts.count >= 2, parts[0] == "Users" else {
        return AbbreviatedPath(host: .none, display: path)
    }
    let afterHome = parts.dropFirst(2)
    let afterArray = Array(afterHome)

    if afterArray.count >= 4,
       afterArray[0] == "work",
       afterArray[1].contains(".")
    {
        let host = String(afterArray[1])
        let remainder = afterArray[2...].joined(separator: "/")
        if host == "github.com" {
            return AbbreviatedPath(host: .github, display: remainder)
        }
        let shortHost = host.split(separator: ".").first.map(String.init) ?? host
        return AbbreviatedPath(host: .other(shortHost), display: remainder)
    }

    return AbbreviatedPath(host: .none, display: "~/" + afterHome.joined(separator: "/"))
}

// MARK: - Status Badge

private struct StatusBadge: View {
    let status: SessionSummary.Status

    var body: some View {
        Text(status.rawValue)
            .font(.caption2.weight(.semibold))
            .textCase(.uppercase)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundStyle(color)
            .clipShape(Capsule())
    }

    private var color: Color {
        switch status {
        case .running: .green
        case .idle: .gray
        case .error: .red
        case .stopped: .orange
        }
    }
}

// MARK: - Detail Sheet

private struct SessionDetailSheet: View {
    let session: SessionSummary
    let detail: SessionDetail?
    let isLoading: Bool
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    LabeledContent("Name") { styledPath(session.name) }
                    LabeledContent("Status", value: session.status.rawValue)
                    LabeledContent("Directory") { styledPath(session.workdir) }

                    if isLoading {
                        ProgressView()
                            .frame(maxWidth: .infinity)
                    } else if let detail, !detail.lastResult.isEmpty {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Last Result")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Text(detail.lastResult)
                                .font(.callout.monospaced())
                                .padding(8)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Color(.systemGray6))
                                .clipShape(RoundedRectangle(cornerRadius: 8))
                        }
                    }
                }
                .padding()
            }
            .navigationTitle("Session Detail")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
            }
        }
    }
}
