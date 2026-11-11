// PROTOTYPE (throwaway). Host app that asks macOS to install the Endpoint
// Security system extension. Replaces AppDelegate.swift in the Xcode "macOS App"
// target (AppKit, Storyboard). The extension's bundle id must be
// <host bundle id>.extension.

import Cocoa
import SystemExtensions

@main
class AppDelegate: NSObject, NSApplicationDelegate, OSSystemExtensionRequestDelegate {

    func applicationDidFinishLaunching(_ notification: Notification) {
        let id = (Bundle.main.bundleIdentifier ?? "") + ".extension"
        NSLog("sentient-host: activating %@", id)
        let req = OSSystemExtensionRequest.activationRequest(forExtensionWithIdentifier: id, queue: .main)
        req.delegate = self
        OSSystemExtensionManager.shared.submitRequest(req)
    }

    func request(_ request: OSSystemExtensionRequest, didFinishWithResult result: OSSystemExtensionRequest.Result) {
        NSLog("sentient-host: request finished, result %d", result.rawValue)
    }

    func request(_ request: OSSystemExtensionRequest, didFailWithError error: Error) {
        NSLog("sentient-host: request failed: %@", error.localizedDescription)
    }

    func requestNeedsUserApproval(_ request: OSSystemExtensionRequest) {
        NSLog("sentient-host: needs user approval (System Settings > Privacy & Security)")
    }

    func request(_ request: OSSystemExtensionRequest,
                 actionForReplacingExtension existing: OSSystemExtensionProperties,
                 withExtension ext: OSSystemExtensionProperties) -> OSSystemExtensionRequest.ReplacementAction {
        return .replace
    }
}
