# Endpoint Security prototype (throwaway)

**Status:** spike, not the real collector. It produced the first real Endpoint Security capture from an agent session on the Mac; the findings and the requirements built on them are in [../../../docs/ideas/mac-collector.md](../../../docs/ideas/mac-collector.md) and [../../../docs/mac-collector.md](../../../docs/mac-collector.md). The Xcode project lives in this folder (project name `Untitled Project`, to be renamed for the real app), with a SwiftUI host app and a System Extension target `SentientES`. The Swift sources are the project's own files; there are no separate copies.

## What it does

The extension subscribes to Endpoint Security notify events (exec, fork, exit, create, write, close, unlink, rename, open). It keeps the pids of processes whose executable path contains `/claude-code/` (the Claude desktop app's bundled Claude Code) and their descendants, and appends one JSON line per event to `/var/tmp/sentient-es.jsonl`. The host app (`ExtensionInstaller`) asks macOS to install the extension and shows the status in its window.

## What it took to get running (Xcode 27.2, individual team)

- Extension target type: System Extension, Endpoint Security. Its entitlements file already has `com.apple.developer.endpoint-security.client`. The extension's bundle id only has to start with the host's (`ai.nosaka.sentientes.SentientES`); the installer finds it inside the app bundle.
- The host needs `com.apple.developer.system-extension.install`, and the template has no entitlements file for it: create `Host.entitlements` and point **Code Signing Entitlements** (Build Settings, filter **All**) at it.
- `ExtensionInstaller.swift` needs `import Combine` (`ObservableObject`, `@Published`).
- The app must run from `/Applications` (copy the built app there), then approve it in System Settings, General, Login Items & Extensions, Endpoint Security Extensions (it is listed under the host's name).
- Grant the extension **Full Disk Access** (Privacy & Security). Without it the process starts and dies, and nothing is logged.
- **Bump the Build number** (extension and host) on every rebuild, or macOS keeps the old extension running. Check with `systemextensionsctl list | grep -i sentient` and `ps aux | grep -i '[S]entientES'`.
- To read the extension's own log lines, `sudo log show --last 15m --info --predicate 'eventMessage contains "sentient-es"'`.

## Run

In a scratch folder, run the bundled Claude Code (`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`) against a local model (see [../../../docs/ideas/test-environments.md](../../../docs/ideas/test-environments.md)), give it a few prompts, then copy `/var/tmp/sentient-es.jsonl` off the Mac.
