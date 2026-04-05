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
