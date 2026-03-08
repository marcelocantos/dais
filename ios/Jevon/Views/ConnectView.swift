// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ConnectView: View {
    @Environment(Connection.self) private var connection
    @State private var host: String = ""
    @State private var portText: String = "13705"
    @State private var showScanner = false

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                Spacer()

                Image(systemName: "terminal")
                    .font(.system(size: 64))
                    .foregroundStyle(.secondary)

                Text("Connect to Jevon")
                    .font(.title)

                if case .error(let msg) = connection.state {
                    Text(msg)
                        .foregroundStyle(.red)
                        .font(.callout)
                }

                VStack(spacing: 12) {
                    TextField("Host (e.g. 192.168.1.10)", text: $host)
                        .textFieldStyle(.roundedBorder)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)

                    TextField("Port", text: $portText)
                        .textFieldStyle(.roundedBorder)
                        .keyboardType(.numberPad)
                }
                .padding(.horizontal, 40)

                HStack(spacing: 16) {
                    Button("Connect") {
                        let port = Int(portText) ?? 13705
                        connection.connect(to: host, port: port)
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(host.isEmpty)

                    #if !targetEnvironment(simulator)
                    Button {
                        showScanner = true
                    } label: {
                        Label("Scan QR", systemImage: "qrcode.viewfinder")
                    }
                    .buttonStyle(.bordered)
                    #endif
                }

                Spacer()
                Spacer()
            }
            .navigationTitle("Jevon")
            .navigationBarTitleDisplayMode(.inline)
            .sheet(isPresented: $showScanner) {
                QRScannerSheet { scannedHost, scannedPort in
                    host = scannedHost
                    portText = String(scannedPort)
                    showScanner = false
                    connection.connect(to: scannedHost, port: scannedPort)
                }
            }
        }
        .onAppear {
            if let last = connection.lastServer {
                host = last.host
                portText = String(last.port)
            }
        }
    }
}

/// Sheet wrapper for the QR scanner with a dismiss button.
private struct QRScannerSheet: View {
    let onScan: (_ host: String, _ port: Int) -> Void
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ZStack {
                QRScannerView(onScan: onScan)
                    .ignoresSafeArea()

                VStack {
                    Spacer()
                    Text("Point camera at the QR code\nshown in the jevond terminal")
                        .multilineTextAlignment(.center)
                        .font(.callout)
                        .padding()
                        .background(.ultraThinMaterial, in: RoundedRectangle(cornerRadius: 12))
                        .padding(.bottom, 40)
                }
            }
            .navigationTitle("Scan QR Code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
    }
}
