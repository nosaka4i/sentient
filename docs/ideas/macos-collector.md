# macOS kernel collector: brief

**Status:** brainstorm, not yet decided. Requirements and design come after this; no code before them. Backlog `fec08b8` (collector), `346867d` (entitlement), `710d18f` (signing and release).

## Problem

On Linux, sentient compares what the agent's log says with what the kernel saw (Tetragon). On macOS it runs in agent logs only mode: the pages show what the agent reported, with no kernel evidence. A Mac user who wants the independent record has nothing. Most of the owner's own coding sessions are on a Mac.

## What changed

Apple approved the owner's team for the development Endpoint Security entitlement (2026-11-10). This was the thing the earlier plan waited on. The earlier plan used `eslogger` (Apple's command-line tool, no entitlement) as a stand-in; with the approval we can skip it and build the real collector. `docs/tetragon-integration.md` and `docs/design.md` still describe the `eslogger` stage and must be updated when this is decided.

## Goal

A Mac with sentient installed shows the same kernel evidence as Linux: the agent's process tree, file writes, deletes and renames, matched to what the agent's log says, with the same Health card, gaps and integrity checks.

## What we know

- Endpoint Security (ES) is an Apple C/Swift API. A program that uses it must be signed with the entitlement and run as root; the user approves it once and grants Full Disk Access.
- ES has process (exec, fork, exit) and file (create, write, close, unlink, rename) events. It has **no network events**; those need a separate Network Extension with its own capability.
- The collector interface (`internal/collector`: `Collector`, `Status`, the store sink, the gate, the buffer) is already platform-neutral. Only Tetragon implements it today; the macOS one would be the second.
- Agent filtering has to happen in our own code: ES selects by event type, not by process tree. Same job as the Linux collector's tree-keeping.
- The Claude desktop app bundles Claude Code under `~/Library/Application Support/Claude/claude-code/<version>/`, and some users have no `claude` on PATH (backlog `a5fc746`), so the agent binary match is a list of path patterns.
- The Mac's VPN makes Claude sessions unstable there, so testing cannot rely on long real sessions.

## Shape (leading option): two processes

- **Swift extension** (an ES client inside a system extension): subscribes to the events, turns them into a small neutral record, writes them to a root-only local socket. As little logic as possible.
- **`sentientd` (Go)**: reads the socket as a collector, keeps agent trees, stores events, attributes, merges. Everything after the socket is shared with Linux.
- A crash of the Swift side does not touch the evidence database; the daemon sees the stream end and records a gap, as it does for Tetragon.

### Alternative: one Go binary using cgo

Calls ES directly, packaged in a signed bundle. No socket, no second process. Still needs Apple-specific install and approval code, so probably a thin Swift or Objective-C wrapper anyway, and Apple's rules for restricted entitlements on a non-system-extension daemon are unchecked. Kept as an alternative to evaluate in the design.

## What the user installs

A signed, notarized `.pkg` containing:

- `Sentient.app`: a small Swift host app with the system extension inside `Contents/Library/SystemExtensions/`. On first launch it asks macOS to install the extension; the user approves it in System Settings and grants Full Disk Access.
- `sentientd` and `sen`, signed with the same Developer ID.
- A launchd plist in `/Library/LaunchDaemons/` that starts `sentientd` as root at boot.

A `.pkg`, not a dmg, because the launchd daemon needs root to install. Homebrew cask later (`710d18f`). The current tarball stays for agent logs only mode. Bundle layout and the rules above are from memory of Apple's docs and must be checked in the design.

## Scope options

1. **Endpoint Security only first (recommended).** Processes and files. Health shows "network: not captured on macOS yet". Least to sign and approve; evidence sooner.
2. Endpoint Security plus a Network Extension content filter from the start. More capability and approval, more to test.

## What I cannot do from the Linux box

Build, sign or run the Swift side, or see real ES output. The Go side (socket reader, normalizer, tree filter, tests) builds and tests here. The owner builds the extension in Xcode and runs it on the Mac; real captures come back as fixtures. Distribution signing waits for the distribution profile.

## Open questions

1. Two processes (Swift extension plus Go daemon) or one Go binary? **Decided (owner, 2026-11-10): two processes, a small Swift extension and the Go daemon.** Reasons: only the small extension holds the Endpoint Security entitlement; a crash of it is a recorded gap, not lost evidence; the Linux build and release pipeline stay unchanged (no cgo, no Mac runner for the Go side); Swift is Apple's supported route. Cost: a second language and a second thing to sign and ship, kept small by keeping the extension to subscribe, serialize, send. The cgo one-binary option is rejected.
2. Network in the first version? **Decided (owner, 2026-11-10): no, Endpoint Security first.** The Network Extension comes later.
3. Distribution entitlement: does the current approval cover Developer ID distribution, or is a second request needed? To be checked in the owner's developer account. **Account (decided, 2026-11-11):** the owner's individual Apple developer account, not an organization one (nosaka is not a registered company, and the project is open source). Users will see the owner's personal name as the publisher in Gatekeeper and the System Settings approval prompt; the README should say so. An organization account would need a legal entity and probably a new entitlement request (unverified).
4. How the extension reaches the daemon: **Decided (owner, 2026-11-10): a Unix socket under a root-only path.** XPC was the alternative; the Go side cannot speak it easily.
5. Test data without Claude: a stand-in agent binary in the match list for a development run (parser, socket reader, tree filter; runs on the owner's M2, no network). For the end-to-end run the Mac cannot reach Anthropic reliably (VPN), and a proxy would not last under the Great Firewall, so the options are: a local model on the M2 behind LiteLLM (`ANTHROPIC_BASE_URL`, keeps the Claude log format; untried) or driving opencode or Codex `--oss`; or a rented Mac outside China (EC2 Mac, MacStadium, MacinCloud), used over screen sharing because approving a system extension is a GUI step, with its device registered in the developer account (or SIP/AMFI relaxed). Rent only when the end-to-end run is reached. Real Claude sessions for final acceptance.
6. How the Mac daemon runs today (as the user, data in `~/Library/Application Support/sentient`) and how that moves to root with launchd and `DefaultHomes`.

## Related

- [../design.md](../design.md) "macOS collector (later)", [../tetragon-integration.md](../tetragon-integration.md) "macOS: eslogger".
- [../kernel-capture-card.md](../kernel-capture-card.md): the Health card the Mac collector must fill.
- Backlog: `fec08b8`, `346867d`, `710d18f`, `18c4000`, `a5fc746`.
