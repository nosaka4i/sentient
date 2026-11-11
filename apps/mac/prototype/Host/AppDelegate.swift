// PROTOTYPE (throwaway). Host app that asks macOS to install the Endpoint
// Security system extension. Add this file to the Xcode "macOS App" target, and
// in the template's SwiftUI App struct (the file with @main) add the line
//     @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate
// as a property of the struct. (An AppKit/Storyboard template that already has
// an AppDelegate.swift can use this file's contents instead, keeping its @main.)
// The extension's bundle id must be <host bundle id>.extension.

import Cocoa
import SystemExtensions

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
