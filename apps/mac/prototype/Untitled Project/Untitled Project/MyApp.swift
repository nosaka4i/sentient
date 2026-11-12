import SwiftUI

@main struct MyApp: App {
    @StateObject private var installer = ExtensionInstaller()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .onAppear { installer.activate() }
                .overlay(alignment: .bottom) { Text(installer.status).padding() }
        }
    }
}
