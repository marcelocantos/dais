// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import AVFoundation
import SwiftUI

/// A camera-based QR code scanner that detects `jevon://host:port` URLs.
struct QRScannerView: UIViewControllerRepresentable {
    let onScan: (_ host: String, _ port: Int) -> Void

    func makeUIViewController(context: Context) -> QRScannerViewController {
        let vc = QRScannerViewController()
        vc.onScan = onScan
        return vc
    }

    func updateUIViewController(_ vc: QRScannerViewController, context: Context) {}
}

final class QRScannerViewController: UIViewController {
    var onScan: ((_ host: String, _ port: Int) -> Void)?

    private let captureSession = AVCaptureSession()
    private var previewLayer: AVCaptureVideoPreviewLayer?
    private var hasScanned = false

    // Delegate runs on main queue, so we use a separate object that
    // is not @MainActor-isolated to satisfy Swift 6 concurrency.
    private lazy var metadataDelegate = MetadataDelegate { [weak self] host, port in
        self?.handleScan(host: host, port: port)
    }

    override func viewDidLoad() {
        super.viewDidLoad()

        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device),
              captureSession.canAddInput(input) else {
            return
        }

        captureSession.addInput(input)

        let output = AVCaptureMetadataOutput()
        guard captureSession.canAddOutput(output) else { return }
        captureSession.addOutput(output)
        output.setMetadataObjectsDelegate(metadataDelegate, queue: .main)
        output.metadataObjectTypes = [.qr]

        let preview = AVCaptureVideoPreviewLayer(session: captureSession)
        preview.videoGravity = .resizeAspectFill
        preview.frame = view.bounds
        view.layer.addSublayer(preview)
        previewLayer = preview

        let session = captureSession
        DispatchQueue.global(qos: .userInitiated).async {
            session.startRunning()
        }
    }

    override func viewDidLayoutSubviews() {
        super.viewDidLayoutSubviews()
        previewLayer?.frame = view.bounds
    }

    override func viewWillDisappear(_ animated: Bool) {
        super.viewWillDisappear(animated)
        let session = captureSession
        if session.isRunning {
            session.stopRunning()
        }
    }

    private func handleScan(host: String, port: Int) {
        guard !hasScanned else { return }
        hasScanned = true
        captureSession.stopRunning()

        let generator = UINotificationFeedbackGenerator()
        generator.notificationOccurred(.success)

        onScan?(host, port)
    }
}

/// Separate delegate class to avoid @MainActor isolation conflicts
/// with AVCaptureMetadataOutputObjectsDelegate under Swift 6.
private final class MetadataDelegate: NSObject, AVCaptureMetadataOutputObjectsDelegate {
    private let onScan: (_ host: String, _ port: Int) -> Void

    init(onScan: @escaping (_ host: String, _ port: Int) -> Void) {
        self.onScan = onScan
    }

    func metadataOutput(
        _ output: AVCaptureMetadataOutput,
        didOutput metadataObjects: [AVMetadataObject],
        from connection: AVCaptureConnection
    ) {
        guard let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              object.type == .qr,
              let value = object.stringValue,
              let (host, port) = parseJevonURL(value) else {
            return
        }

        onScan(host, port)
    }

    private func parseJevonURL(_ string: String) -> (String, Int)? {
        guard let url = URLComponents(string: string),
              url.scheme == "jevon",
              let host = url.host, !host.isEmpty,
              let port = url.port else {
            return nil
        }
        return (host, port)
    }
}
