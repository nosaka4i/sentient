import SwiftUI

@main
struct SentientApp: App {
    @StateObject private var installer = ExtensionInstaller()

    var body: some Scene {
        WindowGroup {
            ContentView(installer: installer)
                .onAppear { installer.activate() }
        }
    }
}
