// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import AVFoundation
import Foundation
import WebKit
import os

private let logger = Logger(subsystem: "com.marcelocantos.jevon", category: "JevonBridge")

/// Bridges the web UI running in WKWebView to jevond.
///
/// JSON messages flow through the JS bridge (postMessage / evaluateJavaScript).
/// Audio bytes stay in the native layer — JS only sees opaque handles + RMS values.
@MainActor
final class JevonBridge: NSObject, WKScriptMessageHandler {

    private weak var webView: WKWebView?

    /// Base URL of jevond (http://host:port).
    private let serverURL: URL

    // MARK: - Chat WebSocket

    private var chatWS: URLSessionWebSocketTask?
    private var chatSession: URLSession?

    // MARK: - Voice WebSocket

    private var voiceWS: URLSessionWebSocketTask?
    private var voiceSession: URLSession?

    // MARK: - Audio engine (mic capture)

    private var audioEngine: AVAudioEngine?

    // MARK: - Audio handle buffers

    /// Monotonic handle counter.
    private var nextHandle: Int = 0
    /// Outbound audio buffers (mic → jevond). Keyed by handle string.
    private var outBuffers: [String: Data] = [:]
    /// Inbound audio buffers (jevond → speaker). Keyed by handle string.
    private var inBuffers: [String: Data] = [:]
    /// Max retained outbound handles (~2s at 43ms/chunk ≈ 46 chunks).
    private let maxOutBuffers = 50

    // MARK: - Audio playback

    private var audioPlayer: AVAudioPlayerNode?
    private var playerEngine: AVAudioEngine?
    private let playbackFormat = AVAudioFormat(
        commonFormat: .pcmFormatFloat32, sampleRate: 24000, channels: 1, interleaved: false
    )!

    // MARK: - Init

    init(serverURL: URL) {
        self.serverURL = serverURL
        super.init()
    }

    /// Attach to a WKWebView. Call this after the web view is created.
    func attach(to webView: WKWebView) {
        self.webView = webView
        webView.configuration.userContentController.add(self, name: "jevon")
    }

    // MARK: - WKScriptMessageHandler

    nonisolated func userContentController(
        _ controller: WKUserContentController,
        didReceive message: WKScriptMessage
    ) {
        guard let body = message.body as? [String: Any],
              let action = body["action"] as? String else { return }

        Task { @MainActor in
            self.handleAction(action, body: body)
        }
    }

    private func handleAction(_ action: String, body: [String: Any]) {
        switch action {
        case "connect":
            connectChat()
        case "disconnect":
            disconnectChat()
        case "send":
            if let data = body["data"] as? String {
                sendChat(data)
            }
        case "startVoice":
            startVoice()
        case "stopVoice":
            stopVoice()
        case "sendAudio":
            if let handle = body["handle"] as? String {
                sendAudioHandle(handle)
            }
        case "playAudio":
            if let handle = body["handle"] as? String {
                playAudioHandle(handle)
            }
        default:
            logger.warning("Unknown bridge action: \(action)")
        }
    }

    // MARK: - Chat connection

    private func connectChat() {
        disconnectChat()

        var wsURL = serverURL
        wsURL = wsURL.appendingPathComponent("ws/chat")
        var components = URLComponents(url: wsURL, resolvingAgainstBaseURL: false)!
        components.scheme = serverURL.scheme == "https" ? "wss" : "ws"

        guard let url = components.url else {
            injectError("Invalid chat WebSocket URL")
            return
        }

        let session = URLSession(configuration: .default)
        chatSession = session
        let ws = session.webSocketTask(with: url)
        chatWS = ws
        ws.resume()

        injectJS("window._jevonTransport._onOpen()")
        logger.info("Chat WebSocket connected")

        Task { await chatReceiveLoop(ws) }
    }

    private func disconnectChat() {
        chatWS?.cancel(with: .goingAway, reason: nil)
        chatWS = nil
        chatSession = nil
    }

    private func sendChat(_ text: String) {
        chatWS?.send(.string(text)) { error in
            if let error {
                logger.error("Chat send failed: \(error.localizedDescription)")
            }
        }
    }

    private func chatReceiveLoop(_ ws: URLSessionWebSocketTask) async {
        while !Task.isCancelled {
            let message: URLSessionWebSocketTask.Message
            do {
                message = try await ws.receive()
            } catch {
                if !Task.isCancelled {
                    logger.info("Chat WebSocket closed: \(error.localizedDescription)")
                    await MainActor.run {
                        injectJS("window._jevonTransport._onClose()")
                    }
                }
                return
            }

            guard case .string(let text) = message,
                  let data = text.data(using: .utf8),
                  let json = try? JSONSerialization.jsonObject(with: data) else {
                continue
            }

            await MainActor.run {
                injectMessage(json)
            }
        }
    }

    private func injectMessage(_ json: Any) {
        guard let data = try? JSONSerialization.data(withJSONObject: json),
              let str = String(data: data, encoding: .utf8) else { return }
        injectJS("window._jevonTransport._onMessage(\(str))")
    }

    // MARK: - Voice

    private func startVoice() {
        stopVoice()

        // Connect voice WebSocket.
        var wsURL = serverURL.appendingPathComponent("ws/voice")
        var components = URLComponents(url: wsURL, resolvingAgainstBaseURL: false)!
        components.scheme = serverURL.scheme == "https" ? "wss" : "ws"

        guard let url = components.url else {
            injectVoiceEvent(["type": "error", "error": "Invalid voice WebSocket URL"])
            return
        }

        let session = URLSession(configuration: .default)
        voiceSession = session
        let ws = session.webSocketTask(with: url)
        voiceWS = ws
        ws.resume()

        injectVoiceEvent(["type": "status", "status": "connected"])

        // Start mic capture.
        startMicCapture()

        // Start voice receive loop.
        Task { await voiceReceiveLoop(ws) }
    }

    private func stopVoice() {
        stopMicCapture()
        stopPlayback()
        voiceWS?.cancel(with: .goingAway, reason: nil)
        voiceWS = nil
        voiceSession = nil
        outBuffers.removeAll()
        inBuffers.removeAll()
    }

    private func voiceReceiveLoop(_ ws: URLSessionWebSocketTask) async {
        while !Task.isCancelled {
            let message: URLSessionWebSocketTask.Message
            do {
                message = try await ws.receive()
            } catch {
                if !Task.isCancelled {
                    logger.info("Voice WebSocket closed: \(error.localizedDescription)")
                    await MainActor.run { stopVoice() }
                }
                return
            }

            await MainActor.run {
                switch message {
                case .data(let data):
                    // Binary audio from Grok — store and send handle to JS.
                    let handle = storeInBuffer(data)
                    injectJS("window._jevonTransport._onAudio('\(handle)')")

                case .string(let text):
                    // JSON voice event — forward to JS.
                    if let data = text.data(using: .utf8),
                       let json = try? JSONSerialization.jsonObject(with: data) {
                        injectVoiceEventRaw(text)
                    }

                @unknown default:
                    break
                }
            }
        }
    }

    // MARK: - Mic capture

    private func startMicCapture() {
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playAndRecord, mode: .default,
                                    options: [.defaultToSpeaker, .allowBluetooth])
            try session.setActive(true)
        } catch {
            injectVoiceEvent(["type": "error", "error": "Audio session: \(error.localizedDescription)"])
            return
        }

        let engine = AVAudioEngine()
        let inputNode = engine.inputNode
        let inputFormat = inputNode.outputFormat(forBus: 0)

        inputNode.installTap(onBus: 0, bufferSize: 2048, format: inputFormat) {
            [weak self] buffer, _ in
            Task { @MainActor [weak self] in
                self?.processMicBuffer(buffer)
            }
        }

        do {
            try engine.start()
        } catch {
            injectVoiceEvent(["type": "error", "error": "Audio engine: \(error.localizedDescription)"])
            return
        }

        audioEngine = engine
        logger.info("Mic capture started")
    }

    private func stopMicCapture() {
        audioEngine?.inputNode.removeTap(onBus: 0)
        audioEngine?.stop()
        audioEngine = nil
    }

    private func processMicBuffer(_ buffer: AVAudioPCMBuffer) {
        guard let channelData = buffer.floatChannelData else { return }
        let srcRate = buffer.format.sampleRate
        let srcCount = Int(buffer.frameLength)
        guard srcCount > 0, srcRate > 0 else { return }

        // Compute RMS.
        var sum: Float = 0
        for i in 0..<srcCount {
            let s = channelData[0][i]
            sum += s * s
        }
        let rms = sqrt(sum / Float(srcCount))

        // Downsample to 24kHz PCM16.
        let dstRate = 24000.0
        let ratio = srcRate / dstRate
        let dstCount = Int(Double(srcCount) / ratio)
        guard dstCount > 0 else { return }

        var pcmData = Data(count: dstCount * 2)
        pcmData.withUnsafeMutableBytes { raw in
            let ptr = raw.bindMemory(to: Int16.self)
            for i in 0..<dstCount {
                let srcIdx = min(Int(Double(i) * ratio), srcCount - 1)
                let sample = max(-1.0, min(1.0, channelData[0][srcIdx]))
                ptr[i] = Int16(sample * 32767)
            }
        }

        // Store buffer, send handle + RMS to JS.
        let handle = storeOutBuffer(pcmData)
        injectJS("window._jevonTransport._onMicFrame('\(handle)', \(rms))")
    }

    // MARK: - Audio handle management

    private func storeOutBuffer(_ data: Data) -> String {
        let handle = "out:\(nextHandle)"
        nextHandle += 1
        outBuffers[handle] = data

        // Evict old handles.
        if outBuffers.count > maxOutBuffers {
            let cutoff = nextHandle - maxOutBuffers
            let keysToRemove = outBuffers.keys.filter { key in
                guard let numStr = key.split(separator: ":").last,
                      let num = Int(numStr) else { return true }
                return num < cutoff
            }
            for key in keysToRemove { outBuffers.removeValue(forKey: key) }
        }

        return handle
    }

    private func storeInBuffer(_ data: Data) -> String {
        let handle = "in:\(nextHandle)"
        nextHandle += 1
        inBuffers[handle] = data
        return handle
    }

    private func sendAudioHandle(_ handle: String) {
        guard let data = outBuffers[handle] else {
            logger.debug("Audio handle not found: \(handle)")
            return
        }
        voiceWS?.send(.data(data)) { error in
            if let error {
                logger.debug("Voice send failed: \(error.localizedDescription)")
            }
        }
    }

    private func playAudioHandle(_ handle: String) {
        guard let data = inBuffers.removeValue(forKey: handle) else {
            logger.debug("Playback handle not found: \(handle)")
            return
        }
        playPCM16(data)
    }

    // MARK: - Audio playback

    private func playPCM16(_ data: Data) {
        if playerEngine == nil {
            let engine = AVAudioEngine()
            let player = AVAudioPlayerNode()
            engine.attach(player)
            engine.connect(player, to: engine.mainMixerNode, format: playbackFormat)
            do {
                try engine.start()
            } catch {
                logger.error("Playback engine start failed: \(error.localizedDescription)")
                return
            }
            playerEngine = engine
            audioPlayer = player
            player.play()
        }

        guard let player = audioPlayer else { return }

        // Convert PCM16 Data to AVAudioPCMBuffer.
        let sampleCount = data.count / 2
        guard let buffer = AVAudioPCMBuffer(pcmFormat: playbackFormat,
                                             frameCapacity: AVAudioFrameCount(sampleCount)) else { return }
        buffer.frameLength = AVAudioFrameCount(sampleCount)

        data.withUnsafeBytes { raw in
            let pcm16 = raw.bindMemory(to: Int16.self)
            guard let floatData = buffer.floatChannelData else { return }
            for i in 0..<sampleCount {
                floatData[0][i] = Float(pcm16[i]) / 32768.0
            }
        }

        player.scheduleBuffer(buffer)
    }

    private func stopPlayback() {
        audioPlayer?.stop()
        audioPlayer = nil
        playerEngine?.stop()
        playerEngine = nil
    }

    // MARK: - JS injection helpers

    private func injectJS(_ js: String) {
        webView?.evaluateJavaScript(js) { _, error in
            if let error {
                logger.debug("JS injection failed: \(error.localizedDescription)")
            }
        }
    }

    private func injectError(_ msg: String) {
        let escaped = msg.replacingOccurrences(of: "'", with: "\\'")
        injectJS("window._jevonTransport._onError('\(escaped)')")
    }

    private func injectVoiceEvent(_ event: [String: Any]) {
        guard let data = try? JSONSerialization.data(withJSONObject: event),
              let str = String(data: data, encoding: .utf8) else { return }
        injectJS("window._jevonTransport._onVoiceEvent(\(str))")
    }

    private func injectVoiceEventRaw(_ json: String) {
        injectJS("window._jevonTransport._onVoiceEvent(\(json))")
    }
}
