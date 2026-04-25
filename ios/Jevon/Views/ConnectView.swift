// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ConnectView: View {
    @Environment(Connection.self) private var connection

    var body: some View {
        ZStack {
            // Full-screen QR scanner.
            #if !targetEnvironment(simulator)
            QRScannerView(
                onScanArtifact: { artifact in
                    // Persist for the next launch — the production
                    // artifact-driven WKWebView path picks it up. Full
                    // Connection-level artifact integration is the
                    // remaining T14.1 work; for now Keychain hand-off
                    // covers the developer flow.
                    try? PigeonAccount.shared.save(artifact)
                },
                onScan: { host, port in
                    connection.connect(to: host, port: port)
                },
                onScanURL: { url in
                    connection.connect(to: url)
                }
            )
            .ignoresSafeArea()
            #endif

            // Overlay with status.
            VStack {
                Spacer()

                if case .error(let msg) = connection.state {
                    Text(msg)
                        .foregroundStyle(.white)
                        .font(.callout)
                        .padding()
                        .background(.red.opacity(0.8), in: RoundedRectangle(cornerRadius: 12))
                        .padding(.bottom, 8)
                }

                Text("Scan the QR code from jevond")
                    .multilineTextAlignment(.center)
                    .font(.callout)
                    .foregroundStyle(.white)
                    .padding()
                    .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12))
                    .padding(.bottom, 40)
            }
        }
    }
}
