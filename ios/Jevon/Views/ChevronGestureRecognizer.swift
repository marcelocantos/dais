// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import UIKit

/// Recognises a two-finger ">" (chevron) gesture for triggering safe mode.
/// The gesture consists of two strokes forming a chevron shape:
/// 1. Two fingers move down-right (SE direction)
/// 2. Then change to down-left (SW direction)
/// Must use exactly two fingers. Works at the UIWindow level,
/// independent of any SwiftUI view hierarchy.
final class ChevronGestureRecognizer: UIGestureRecognizer {
    private var phase: Phase = .idle
    private var startPoint: CGPoint = .zero
    private var apexPoint: CGPoint = .zero

    private enum Phase {
        case idle
        case firstStroke   // Moving down-right
        case secondStroke  // Moving down-left
    }

    // Minimum distances to count as a real stroke (points).
    private let minStrokeLength: CGFloat = 60

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent?) {
        guard let allTouches = event?.allTouches, allTouches.count == 2 else {
            state = .failed
            return
        }

        let points = allTouches.map { $0.location(in: view) }
        startPoint = CGPoint(
            x: (points[0].x + points[1].x) / 2,
            y: (points[0].y + points[1].y) / 2
        )
        phase = .firstStroke
    }

    override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent?) {
        guard let allTouches = event?.allTouches, allTouches.count == 2 else {
            state = .failed
            return
        }

        let points = allTouches.map { $0.location(in: view) }
        let current = CGPoint(
            x: (points[0].x + points[1].x) / 2,
            y: (points[0].y + points[1].y) / 2
        )

        switch phase {
        case .idle:
            state = .failed

        case .firstStroke:
            let dx = current.x - startPoint.x
            let dy = current.y - startPoint.y

            // Must be moving right and down.
            if dx < 0 && abs(dx) > 20 {
                state = .failed
                return
            }

            // Once we have enough rightward movement, check if direction
            // has reversed (started going left) — that's the apex.
            if dx > minStrokeLength && dy > 0 {
                // Check if we've started going left relative to previous.
                let prevDx = current.x - apexPoint.x
                if apexPoint != .zero && prevDx < -10 {
                    // Apex detected — transition to second stroke.
                    phase = .secondStroke
                } else if current.x > apexPoint.x || apexPoint == .zero {
                    apexPoint = current
                }
            }

        case .secondStroke:
            let dx = current.x - apexPoint.x
            let dy = current.y - apexPoint.y

            // Must be moving left and down from apex.
            if dx < -minStrokeLength && dy > 0 {
                state = .ended
            }
        }
    }

    override func touchesEnded(_ touches: Set<UITouch>, with event: UIEvent?) {
        if state != .ended {
            state = .failed
        }
    }

    override func touchesCancelled(_ touches: Set<UITouch>, with event: UIEvent?) {
        state = .cancelled
    }

    override func reset() {
        super.reset()
        phase = .idle
        startPoint = .zero
        apexPoint = .zero
    }
}
