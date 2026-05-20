// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import Pigeon
import os

private let logger = Logger(subsystem: "com.marcelocantos.jevons", category: "PigeonAccount")

/// Process environment variable carrying a base64url-encoded
/// PairingArtifact, populated by the developer-flow xcrun deploy:
///
/// ```bash
/// xcrun devicectl device process launch \
///   --environment-variables PIGEON_PAIRING_ARTIFACT="$(pigeon pair ...)"
/// ```
private let pairingArtifactEnvKey = "PIGEON_PAIRING_ARTIFACT"

/// Owns the device's persisted PairingArtifact via the Keychain. One
/// per app — there's no design need for multiple credentials.
@MainActor
final class PigeonAccount {
    static let shared = PigeonAccount()

    private let store: any CredentialStore

    init(store: any CredentialStore = KeychainCredentialStore(
        service: "com.marcelocantos.jevons.pairing"
    )) {
        self.store = store
    }

    /// Returns the persisted artifact, or nil if none is stored or the
    /// load fails. Callers should treat nil as "needs pairing".
    func load() -> PairingArtifact? {
        do {
            return try store.load()
        } catch CredentialStoreError.noCredential {
            return nil
        } catch {
            logger.error("load credential failed: \(error.localizedDescription)")
            return nil
        }
    }

    /// Persists the given artifact, replacing any existing one.
    func save(_ artifact: PairingArtifact) throws {
        try store.save(artifact)
    }

    /// Removes the stored artifact, e.g. after the user signs out or
    /// the artifact is detected as expired.
    func reset() {
        do { try store.delete() } catch { logger.warning("delete credential failed: \(error.localizedDescription)") }
    }

    /// Reads `PIGEON_PAIRING_ARTIFACT` from the process environment;
    /// when present, decodes and persists it (replacing any existing
    /// credential). Designed for the developer-flow xcrun deploy that
    /// passes a freshly minted artifact at launch — first-run becomes
    /// zero-touch with no QR scan.
    ///
    /// Returns true when an env-var artifact was ingested (caller may
    /// want to log or refresh UI). Errors are logged and swallowed:
    /// a malformed env var should not crash the app.
    @discardableResult
    func ingestEnvArtifactIfPresent() -> Bool {
        guard let text = ProcessInfo.processInfo.environment[pairingArtifactEnvKey],
              !text.isEmpty else {
            return false
        }
        do {
            let artifact = try PairingArtifact.fromText(text)
            try save(artifact)
            logger.info("ingested PIGEON_PAIRING_ARTIFACT for peer \(artifact.record.peerInstanceID)")
            return true
        } catch {
            logger.error("ingest PIGEON_PAIRING_ARTIFACT failed: \(error.localizedDescription)")
            return false
        }
    }
}
