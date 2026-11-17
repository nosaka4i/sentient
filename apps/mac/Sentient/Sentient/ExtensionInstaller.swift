// Asks the system to install (or replace) the SentientES system extension.
// The system then asks the user to approve it in System Settings; Full Disk
// Access is a separate switch the user turns on for the extension.

import Combine
import Foundation
import SystemExtensions

final class ExtensionInstaller: NSObject, ObservableObject, OSSystemExtensionRequestDelegate {
    // the extension's bundle id: the host's id plus ".SentientES"
    static let extensionID = "ai.nosaka.Sentient.SentientES"

    @Published var status = "not requested"

    func activate() {
        NSLog("Sentient: activating %@", Self.extensionID)
        status = "requesting"
        let req = OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: Self.extensionID, queue: .main)
        req.delegate = self
        OSSystemExtensionManager.shared.submitRequest(req)
    }

    func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        NSLog("Sentient: request finished, result %d", result.rawValue)
        status = "installed (\(result.rawValue))"
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
        NSLog("Sentient: request failed: %@", error.localizedDescription)
        status = "failed: \(error.localizedDescription)"
    }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        NSLog("Sentient: needs approval (System Settings > Privacy & Security)")
        status = "needs approval in System Settings"
    }

    func request(_ request: OSSystemExtensionRequest,
                 actionForReplacingExtension existing: OSSystemExtensionProperties,
                 withExtension ext: OSSystemExtensionProperties) -> OSSystemExtensionRequest.ReplacementAction {
        return .replace
    }
}
