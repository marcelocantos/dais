// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI
import WebKit

/// Wraps the jevond web UI in a WKWebView with the native JS bridge.
///
/// In native mode, the web UI uses the bridge for all communication
/// (chat messages via JS, audio bytes via native handles).
struct WebUIView: UIViewRepresentable {
    let serverURL: URL

    func makeCoordinator() -> Coordinator {
        Coordinator(serverURL: serverURL)
    }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()

        // Allow inline media (for fallback browser-mode audio if needed).
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.isOpaque = false
        webView.backgroundColor = .clear
        webView.scrollView.backgroundColor = .clear
        webView.scrollView.isScrollEnabled = false

        // Attach the native bridge.
        context.coordinator.bridge.attach(to: webView)

        // Load the web UI from jevond.
        webView.load(URLRequest(url: serverURL))

        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {}

    @MainActor
    class Coordinator {
        let bridge: JevonBridge

        init(serverURL: URL) {
            bridge = JevonBridge(serverURL: serverURL)
        }
    }
}
