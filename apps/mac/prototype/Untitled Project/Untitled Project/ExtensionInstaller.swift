// PROTOTYPE (throwaway). Asks macOS to install the Endpoint Security system
// extension. Add this file to the host (macOS App) target. SwiftUI, no AppDelegate:
// in the template's App struct (the file with @main) use it like this:
//
//     @StateObject private var installer = ExtensionInstaller()
//     ...
//     WindowGroup { ContentView().onAppear { installer.activate() } }
//
// The extension's bundle id must be <host bundle id>.extension.

import Combine
import Foundation
import SystemExtensions

final class ExtensionInstaller: NSObject, ObservableObject, OSSystemExtensionRequestDelegate {

    @Published var status = "not requested"

    func activate() {
        let id = "ai.nosaka.sentientes.SentientES"
        NSLog("sentient-host: activating %@", id)
        status = "requesting \(id)"
        let req = OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: id, queue: .main)
        req.delegate = self
        OSSystemExtensionManager.shared.submitRequest(req)
    }

    func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        NSLog("sentient-host: request finished, result %d", result.rawValue)
        status = "finished (\(result.rawValue))"
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
        NSLog("sentient-host: request failed: %@", error.localizedDescription)
        status = "failed: \(error.localizedDescription)"
    }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        NSLog("sentient-host: needs user approval (System Settings > Privacy & Security)")
        status = "needs approval in System Settings"
    }

    func request(_ request: OSSystemExtensionRequest,
                 actionForReplacingExtension existing: OSSystemExtensionProperties,
                 withExtension ext: OSSystemExtensionProperties) -> OSSystemExtensionRequest.ReplacementAction {
        return .replace
    }
}
