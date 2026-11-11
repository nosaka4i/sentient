# Endpoint Security prototype (throwaway)

**Status:** spike, not the real collector. It exists to produce real Endpoint Security events from an agent session on the Mac (shapes, volume, where the process tree starts). The real extension comes after the requirements ([../../../docs/ideas/macos-collector.md](../../../docs/ideas/macos-collector.md)). Written without being built or run: expect a few compile fixes.

## What it does

A system extension subscribes to exec, fork, exit, create, write, close, unlink and rename (notify events only, so it can never block anything). It keeps the pids of processes whose executable path contains `/claude-code/` (the Claude desktop app's bundled Claude Code) and their descendants, and appends one JSON line per event to `/var/tmp/sentient-es.jsonl`. A small host app installs the extension.

## Build (Xcode, on the Mac)

The owner builds with **Xcode 27.2** (noted 2026-11-11). Its New Project flow differs from older Xcodes: it creates an `untitled` project without asking for a name, and the macOS App template is SwiftUI with no `AppDelegate.swift`. The steps below are adjusted for that; the Xcode 27 specifics (how to rename the project, where the System Extension target template lives) are not known to me and are to be filled in from what works.

1. New project: macOS, App, AppKit, Storyboard, Swift. Name it `SentientES`; bundle id e.g. `ai.nosaka.sentientes`. Team: yours. Save it under `apps/mac/prototype/` (next to the `Host/` and `Extension/` source folders; Xcode creates the `.xcodeproj`, which I cannot generate from here).
2. Add a target: macOS, System Extension, type **Endpoint Security**. Name `extension`; its bundle id must be `<host bundle id>.extension`.
3. Add `Host/ExtensionInstaller.swift` to the host target. In the template's SwiftUI `App` struct add `@StateObject private var installer = ExtensionInstaller()` and call `installer.activate()` from `.onAppear` on the `ContentView` (no AppDelegate; the header of the file shows the exact lines). Replace the extension's `main.swift` with `Extension/main.swift`. Remove the template's other generated extension sources if they conflict.
4. Signing and capabilities: host target gets **System Extension**; extension target gets **Endpoint Security**. Automatic signing with your team (your approved entitlement). Paste any signing error back here.
5. Build, copy `SentientES.app` to `/Applications`, run it from there. (`systemextensionsctl developer on` is the way to run it from elsewhere.)
6. Approve it: System Settings, Privacy & Security, Allow. Also grant the extension Full Disk Access. Check with `systemextensionsctl list` (it should say activated, enabled).

## Run

In `~/cctest`, run the bundled Claude Code (`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`) against the local model and give it a few prompts. Then `wc -l /var/tmp/sentient-es.jsonl` and send the file back (rly).

## Things to look for

- Whether `es_new_client` succeeds (the extension log, `log stream --predicate 'eventMessage contains "sentient-es"'`).
- Events for the agent's shell commands and the files they touch; whether a detached process stays in the tree; writes outside the project folder.
- Volume per prompt, and what the `/claude-code/` match misses or catches too much.
