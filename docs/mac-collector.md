# Mac kernel collector: requirements

**Status:** proposed requirements, not yet built. A throwaway prototype exists ([../apps/mac/prototype/](../apps/mac/prototype/)) and produced the first real capture. Decided in [ideas/mac-collector.md](ideas/mac-collector.md): Endpoint Security only first (network is the next milestone, backlog `cd35122`), a small Swift system extension plus the Go daemon, a Unix socket between them, the owner's individual developer account. The design comes after this; no code before it. Backlog `fec08b8`.

## Context

On Linux sentient compares what the agent's log says with what the kernel saw (Tetragon). On the Mac it runs in agent logs only mode: the pages show what the agent reported and nothing confirms it. Apple approved the owner's team for the Endpoint Security entitlement, and the prototype ran on the owner's Mac with the real Claude Code driven by a local model. It captured 11,376 events in about 14 minutes: the agent's process tree, its shell commands, file writes, creates, deletes and renames, and a write outside the project folder. It also showed that most of the volume is not evidence (file reads, terminal output) and that Claude Code reads the keychain at start-up. The findings are in [ideas/mac-collector.md](ideas/mac-collector.md#first-real-capture-2026-11-12).

## Goals

- A Mac with sentient installed shows the same kernel evidence as Linux: the agent's process tree, file changes, and a mark on each logged action the kernel confirms, with the same Health card, gaps and integrity checks.
- Everything after the socket is shared with Linux: the normalizer output, `kernel_events`, `processes`, attribution, merge, flag rules, capture levels.
- The Swift side stays small: subscribe, filter to the agent, serialize, send.
- Claude Code on Linux and agent logs only mode on the Mac do not change.

## Not goals

- **Network capture.** Endpoint Security has no connect events. Next milestone: a Network Extension content filter (backlog `cd35122`). Until then Health says "network: not captured on the Mac yet".
- **File reads.** Not subscribed in this version (`open`); a sensitive-files watchlist is later.
- Intel Macs, the Mac App Store (Endpoint Security cannot be sandboxed), MDM deployment and pre-approved profiles, an organization developer account (the project is open source and not a company).
- Other agents on the Mac (Codex and others use the same collector once their log side exists; see [multi-agent-support.md](multi-agent-support.md)).

## Requirements

### The extension (Swift)

1. **Notify events only, never authorization.** Subscribe to exec, fork, exit, create, write, close, unlink and rename. The extension cannot block anything, and must never hold up the system.
2. **Know which processes are the agent.** A list of agent executable paths, kept in one place and extendable without a code change: the Claude desktop app's bundled Claude Code (`~/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude`, matched by prefix, because the version is in the path) and CLI installs. Same idea as the Linux agent paths.
3. **Keep only the agent's tree.** A process whose executable matches is a root; its fork and exec descendants are kept, and removed on exit. Only events of the agent tree leave the extension (the prototype did this and it worked). Whether the tree is kept in the extension or in `sentientd` is a design question.
4. **Drop what is not evidence, at the source.** A `close` is kept only when the file was modified. Writes to terminals (`/dev/tty*`) and `/dev/null` are dropped. In the first capture these were about 7,000 of 11,376 events. Dropped events are counted, not silently lost.
5. **Each event carries** a sequence number, a timestamp, the pid and the process-instance version (a pid can be reused), the parent pid, the executable path, and for exec the full argv; for file events the path (and the destination for a rename).
6. **Send to `sentientd` over a Unix socket under a root-only path,** one JSON record per line, with a schema version. The extension reconnects when the daemon restarts. If the daemon is not listening, the extension buffers a bounded amount and then drops and counts; it never grows without limit.
7. **Report drops and its own state.** Gaps in the sequence numbers are drops, and a start and stop marker lets the daemon record when the extension was not running.

### The Go side

8. **A collector behind the existing interface** (`internal/collector`), named for Endpoint Security, that reads the socket and feeds the same pipeline as Tetragon: normalizer, filter and gate, buffer, store sink, `kernel_events` and `processes`. Capture levels (`essential`, `extended`, `complete`) apply.
9. **Normalize Mac paths.** `/tmp`, `/var` and `/etc` are links into `/private`, and user data sits behind firmlinks under `/System/Volumes/Data`. The kernel's `/private/tmp/x` must match the log's `/tmp/x`, so paths are resolved the same way for comparison and for the outside-the-repo check.
10. **Match a logged command to its kernel exec** by the command's script text, as on Linux. The shell on the Mac is `/bin/zsh`; how the agent's command reaches it (and whether a shell snapshot sits in between) is to be confirmed from the capture.
11. **Recognize the agent's own plumbing** with rules for the Mac, so start-up and housekeeping do not show as agent actions: `security` lookups of its own login, `ioreg` machine id, `ps aux | grep` for IDEs, `rg --files`, `git ls-files`, and its own files (`~/.claude.json.lock`, the session logs, shell snapshots, `~/.claude/sessions/`, `/private/tmp/claude-<uid>/…`). The existing `internal/background` rules are the model.
12. **Flag credential reads.** An agent running `security find-generic-password`, `gh auth token` or reading a token from the keychain is notable, even when the agent's own login does it, because the log does not show it. A new rule, with the agent's own known login lookups marked as such, not hidden.
13. **The outside-the-repo write works on the Mac,** from the kernel's file events and, in agent logs only mode, from the log's absolute paths, as described in [rules.md](rules.md).
14. **Gaps:** a stream that ends, a socket that never connected, a sequence jump and an extension that is not running are `monitor_gaps`, like Tetragon's. The heartbeat ([heartbeat.md](heartbeat.md)) covers `sentientd` downtime.

### Install and operation

15. **What the user installs:** a signed, notarized `.pkg` with `Sentient.app` (a small Swift host app that asks the system to install the extension), the extension, `sentientd`, `sen` and a launchd plist that starts `sentientd` as root. The user approves the extension in System Settings and grants it Full Disk Access once. Sentient installs nothing else.
16. **A missing approval is shown, not silent.** If the extension is not approved, not running, or lacks Full Disk Access (the client fails to start), Health says so plainly and records a gap. The first run of the prototype showed this failure is easy to hit: the extension was enabled but did nothing until Full Disk Access was on.
17. **`sentientd` runs as root under launchd** with the data directory root-owned and the default homes covering `/Users/*`, as on Linux. The current tarball stays for agent logs only mode.
18. **One version for the whole product.** The Mac app and extension carry the same `major.minor.patch` as `sen` and `sentientd` (the Version field; `internal/version` is the source), and the Build number rises on every build, because macOS replaces an extension only when its version or build is newer (found in the prototype: a rebuild with the same build number installed nothing).
19. **Distribution is outside the Mac App Store,** under the owner's individual Developer ID. Users see the owner's personal name as the publisher in Gatekeeper and in the approval prompt; the README says so. How to remove it (extension and app) is documented.

### Accounting and honesty

20. **The accounting keeps adding up** (`AgentLog = Confirmed + Unverified + NotChecked + Pending + NotCompared`), with the same gap rules.
21. **Never claim more than was checked.** Network and file reads are not captured, and Health says so. The pages say "the command ran" and "the file was written", not more.
22. **Agent logs only mode still works** on the Mac, with no marks, exactly as today.

### Quality

23. **Tests.** The Go side is tested from fixtures made from the real capture (user name and paths made generic, no real work) at 70% coverage or above per package. The Swift side has a short build-and-run checklist, and the build steps are documented, since it can only be built on a Mac.
24. **Docs:** how to build, sign, install and approve it, and how to read a capture.

## Acceptance

Run the scratch experiment (a prompt or two in a scratch folder with the real Claude Code, driven by a local model) with sentient on. The Mac shows one session with its prompts, commands and file changes, the kernel's confirmation on the commands and writes it saw, the credential reads at start-up as notable, and a write outside the project folder flagged. Terminal writes and unmodified closes are not stored, the counts add up, and with the extension not approved Health says so and nothing is claimed as confirmed.

## Open decisions (for the owner)

1. **Where the process tree is kept:** in the extension (fewer events cross the socket; as the prototype did) or in `sentientd` (one place for the logic, shared with Tetragon). Leaning extension.
2. **Whether the current entitlement covers distribution signing,** or a second request is needed. Only the developer account can show it.
3. **Whether the Network Extension needs a separate approval,** and whether it is one extension with the Endpoint Security one or two.
4. **Whether to subscribe to opens for a watchlist** of sensitive paths (`~/.ssh`, keychain files) to see credential reads that do not go through the `security` command.
5. **Capture levels on the Mac:** what `extended` and `complete` mean for Endpoint Security (which events), beyond the filter above.
6. **Minimum system version and architecture:** Apple silicon only (the owner's Mac is an M2) or also Intel, and the oldest macOS supported.

## Related

- [ideas/mac-collector.md](ideas/mac-collector.md): the brief, the decisions, and the first real capture.
- [ideas/test-environments.md](ideas/test-environments.md): how agent workload reaches the Mac (local model through Ollama, the rented Mac option).
- [design.md](design.md) (macOS collector section), [tetragon-integration.md](tetragon-integration.md), [kernel-capture-card.md](kernel-capture-card.md), [rules.md](rules.md), [capture-verbosity.md](capture-verbosity.md).
- Backlog: `fec08b8` (this), `cd35122` (network), `346867d` (entitlement), `710d18f` (signing and release), `a5fc746` (the Claude app's bundled binary).
