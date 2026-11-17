import SwiftUI

struct ContentView: View {
    @ObservedObject var installer: ExtensionInstaller

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Sentient").font(.title2)
            Text("Extension: \(installer.status)")
            Text("Version \(Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "?") build \(Bundle.main.infoDictionary?["CFBundleVersion"] as? String ?? "?")")
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .padding()
        .frame(minWidth: 320)
    }
}
