// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI
import WebKit

/// Wraps the jevond web UI in a WKWebView with mic access enabled.
struct WebUIView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()

        // Allow inline media playback (required for AudioContext).
        config.allowsInlineMediaPlayback = true
        // Don't require user gesture to start audio capture/playback.
        config.mediaTypesRequiringUserActionForPlayback = []

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.isOpaque = false
        webView.backgroundColor = .clear
        webView.scrollView.backgroundColor = .clear

        // Allow back/forward navigation.
        webView.allowsBackForwardNavigationGestures = true

        webView.load(URLRequest(url: url))
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        // If the URL changed, reload.
        if webView.url != url {
            webView.load(URLRequest(url: url))
        }
    }
}
