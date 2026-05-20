// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Pigeon
import SwiftUI
import WebKit

/// Wraps the jevond web UI in a WKWebView with the native JS bridge.
///
/// The web UI is bundled in the app — the WKWebView loads it from
/// the app bundle, and all transport flows through the native bridge
/// (chat messages over pigeon QUIC, audio bytes via native handles).
struct WebUIView: UIViewRepresentable {
    let mode: BridgeMode

    /// Convenience for the legacy direct-connection path.
    init(serverURL: URL) {
        self.mode = .direct(serverURL)
    }

    /// Artifact-driven (production) path: bridge connects via
    /// PigeonConn.connect(artifact:).
    init(artifact: PairingArtifact) {
        self.mode = .relayArtifact(artifact)
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(mode: mode)
    }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()

        // Allow inline media (for fallback browser-mode audio if needed).
        config.allowsInlineMediaPlayback = true
        config.mediaTypesRequiringUserActionForPlayback = []

        // NOTE: programmatic input.focus() in JS does NOT bring up the
        // iOS keyboard without a prior user gesture. The private
        // WKPreferences key `keyboardDisplayRequiresUserAction` was
        // removed in iOS 26 (NSUnknownKeyException on setValue). User
        // must tap the input to surface the keyboard.

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.isOpaque = false
        webView.backgroundColor = .clear
        webView.scrollView.backgroundColor = .clear
        webView.scrollView.isScrollEnabled = false

        // Attach the native bridge before any page load so the JS
        // bridge is ready when the page evaluates.
        context.coordinator.bridge.attach(to: webView)

        // Load the bundled web UI. WKWebView needs read access to the
        // enclosing directory so it can resolve scripts/transport.js.
        if let indexURL = Bundle.main.url(forResource: "index", withExtension: "html", subdirectory: "web") {
            webView.loadFileURL(indexURL, allowingReadAccessTo: indexURL.deletingLastPathComponent())
        }

        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {}

    @MainActor
    class Coordinator {
        let bridge: JevonsBridge

        init(mode: BridgeMode) {
            bridge = JevonsBridge(mode: mode)
        }
    }
}
