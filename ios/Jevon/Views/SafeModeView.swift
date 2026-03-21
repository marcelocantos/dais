// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// Pure-Swift safe mode screen. No Lua dependency.
/// Shows script version info and allows rollback to a previous snapshot.
struct SafeModeView: View {
    @Environment(Connection.self) private var connection
    @Environment(\.dismiss) private var dismiss
    @State private var snapshots: [ScriptSnapshot] = []
    @State private var loading = true
    @State private var error: String?
    @State private var rolledBack = false

    var body: some View {
        NavigationStack {
            List {
                Section {
                    HStack {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .foregroundStyle(.orange)
                        Text("Safe Mode")
                            .font(.headline)
                    }
                    Text("Lua scripts are bypassed. Use this screen to roll back to a previous working version.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }

                if loading {
                    Section {
                        ProgressView("Loading snapshots...")
                    }
                } else if let error {
                    Section {
                        Text(error)
                            .foregroundStyle(.red)
                    }
                } else if snapshots.isEmpty {
                    Section {
                        Text("No snapshots available.")
                            .foregroundStyle(.secondary)
                    }
                } else {
                    Section("Available Snapshots") {
                        ForEach(snapshots, id: \.snapshot) { snap in
                            Button {
                                rollback(to: snap.snapshot)
                            } label: {
                                HStack {
                                    VStack(alignment: .leading) {
                                        Text("Snapshot \(snap.snapshot)")
                                            .font(.body.monospacedDigit())
                                        Text(snap.createdAt)
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                    Image(systemName: "arrow.counterclockwise")
                                        .foregroundStyle(.blue)
                                }
                            }
                        }
                    }
                }

                if rolledBack {
                    Section {
                        HStack {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundStyle(.green)
                            Text("Rollback successful. Dismiss to reload.")
                        }
                    }
                }
            }
            .navigationTitle("Safe Mode")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Dismiss") { dismiss() }
                }
            }
        }
        .task {
            await requestSnapshots()
        }
    }

    private func requestSnapshots() async {
        loading = true
        error = nil

        let result = await connection.sendControl(action: "list_snapshots")
        loading = false

        switch result {
        case .success(let response):
            if let snaps = response["snapshots"] as? [[String: Any]] {
                snapshots = snaps.compactMap { dict in
                    guard let id = dict["snapshot"] as? Int64,
                          let createdAt = dict["created_at"] as? String else {
                        return nil
                    }
                    return ScriptSnapshot(snapshot: id, createdAt: createdAt)
                }
            } else if let err = response["error"] as? String {
                error = err
            }
        case .failure(let err):
            error = err.localizedDescription
        }
    }

    private func rollback(to snapshot: Int64) {
        Task {
            let result = await connection.sendControl(
                action: "rollback",
                value: "\(snapshot)"
            )
            switch result {
            case .success(let response):
                if response["status"] as? String == "ok" {
                    rolledBack = true
                } else if let err = response["error"] as? String {
                    error = err
                }
            case .failure(let err):
                error = err.localizedDescription
            }
        }
    }
}

struct ScriptSnapshot {
    let snapshot: Int64
    let createdAt: String
}
