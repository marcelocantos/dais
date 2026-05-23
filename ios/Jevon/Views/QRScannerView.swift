// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import AVFoundation
import Pigeon
import SwiftUI
import UIKit
import Vision

/// A camera-based scanner that detects pigeon PairingArtifacts (QR or
/// text via OCR fallback).
struct QRScannerView: UIViewControllerRepresentable {
    var onScanArtifact: ((PairingArtifact) -> Void)?

    func makeUIViewController(context: Context) -> QRScannerViewController {
        let vc = QRScannerViewController()
        vc.onScanArtifact = onScanArtifact
        return vc
    }

    func updateUIViewController(_ vc: QRScannerViewController, context: Context) {}
}

final class QRScannerViewController: UIViewController {
    var onScanArtifact: ((PairingArtifact) -> Void)?

    private let captureSession = AVCaptureSession()
    private var previewLayer: AVCaptureVideoPreviewLayer?
    private var hasScanned = false

    private var metadataDelegate: MetadataDelegate?
    private var videoDelegate: OCRVideoDelegate?
    private let videoOutputQueue = DispatchQueue(label: "ocr", qos: .userInitiated)

    override func viewDidLoad() {
        super.viewDidLoad()

        let qrDelegate = MetadataDelegate(
            onResult: { [weak self] result in
                self?.handleResult(result)
            }
        )
        self.metadataDelegate = qrDelegate

        let ocrDelegate = OCRVideoDelegate(
            onResult: { [weak self] result in
                Task { @MainActor in
                    self?.handleResult(result)
                }
            }
        )
        self.videoDelegate = ocrDelegate

        guard let device = AVCaptureDevice.default(for: .video),
              let input = try? AVCaptureDeviceInput(device: device),
              captureSession.canAddInput(input) else {
            return
        }

        captureSession.addInput(input)

        // QR code detection (primary, instant).
        let metadataOutput = AVCaptureMetadataOutput()
        if captureSession.canAddOutput(metadataOutput) {
            captureSession.addOutput(metadataOutput)
            metadataOutput.setMetadataObjectsDelegate(qrDelegate, queue: .main)
            metadataOutput.metadataObjectTypes = [.qr]
        }

        // Video frames for OCR fallback.
        let videoOutput = AVCaptureVideoDataOutput()
        videoOutput.setSampleBufferDelegate(ocrDelegate, queue: videoOutputQueue)
        videoOutput.alwaysDiscardsLateVideoFrames = true
        if captureSession.canAddOutput(videoOutput) {
            captureSession.addOutput(videoOutput)
        }

        let preview = AVCaptureVideoPreviewLayer(session: captureSession)
        preview.videoGravity = .resizeAspectFill
        preview.frame = view.bounds
        if let connection = preview.connection, connection.isVideoRotationAngleSupported(0) {
            connection.videoRotationAngle = currentRotationAngle()
        }
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
        if let connection = previewLayer?.connection,
           connection.isVideoRotationAngleSupported(currentRotationAngle()) {
            connection.videoRotationAngle = currentRotationAngle()
        }
    }

    private func currentRotationAngle() -> CGFloat {
        guard let scene = view.window?.windowScene else { return 0 }
        switch scene.interfaceOrientation {
        case .portrait: return 90
        case .portraitUpsideDown: return 270
        case .landscapeLeft: return 180
        case .landscapeRight: return 0
        default: return 90
        }
    }

    override func viewWillDisappear(_ animated: Bool) {
        super.viewWillDisappear(animated)
        let session = captureSession
        if session.isRunning {
            session.stopRunning()
        }
    }

    private func handleResult(_ artifact: PairingArtifact) {
        guard !hasScanned else { return }
        hasScanned = true
        captureSession.stopRunning()

        let generator = UINotificationFeedbackGenerator()
        generator.notificationOccurred(.success)

        onScanArtifact?(artifact)
    }
}

// MARK: - QR Metadata Delegate

private final class MetadataDelegate: NSObject, AVCaptureMetadataOutputObjectsDelegate {
    private let onResult: (PairingArtifact) -> Void

    init(onResult: @escaping (PairingArtifact) -> Void) {
        self.onResult = onResult
    }

    func metadataOutput(
        _ output: AVCaptureMetadataOutput,
        didOutput metadataObjects: [AVMetadataObject],
        from connection: AVCaptureConnection
    ) {
        guard let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
              object.type == .qr,
              let value = object.stringValue,
              let artifact = parseArtifact(value) else {
            return
        }
        onResult(artifact)
    }
}

// MARK: - OCR Video Delegate

/// Runs VNRecognizeTextRequest on video frames to find URLs.
/// All state is accessed only from the video output queue — no
/// main actor isolation issues.
private final class OCRVideoDelegate: NSObject, AVCaptureVideoDataOutputSampleBufferDelegate {
    private let onResult: (PairingArtifact) -> Void
    private var lastTime: CFAbsoluteTime = 0
    private var found = false

    init(onResult: @escaping (PairingArtifact) -> Void) {
        self.onResult = onResult
    }

    func captureOutput(_ output: AVCaptureOutput, didOutput sampleBuffer: CMSampleBuffer, from connection: AVCaptureConnection) {
        guard !found else { return }

        let now = CFAbsoluteTimeGetCurrent()
        guard now - lastTime >= 0.5 else { return }
        lastTime = now

        guard let pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer) else { return }

        let request = VNRecognizeTextRequest { [weak self] request, _ in
            guard let self, !self.found else { return }
            guard let results = request.results as? [VNRecognizedTextObservation] else { return }

            for observation in results {
                guard let candidate = observation.topCandidates(1).first else { continue }
                let text = candidate.string.trimmingCharacters(in: .whitespaces)
                if let artifact = parseArtifact(text) {
                    self.found = true
                    self.onResult(artifact)
                    return
                }
            }
        }
        request.recognitionLevel = .fast
        request.usesLanguageCorrection = false

        let handler = VNImageRequestHandler(cvPixelBuffer: pixelBuffer, options: [:])
        try? handler.perform([request])
    }
}

// MARK: - Artifact Parsing

/// Decodes a pigeon PairingArtifact from its canonical base64url text
/// encoding — the form emitted by `pigeon pair --format=text` and by
/// jevonsd's `--pair` flag. Returns nil if the input does not look
/// like a token or fails to decode.
private func parseArtifact(_ string: String) -> PairingArtifact? {
    let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
    guard trimmed.count >= 64,
          trimmed.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" })
    else { return nil }
    return try? PairingArtifact.fromText(trimmed)
}
