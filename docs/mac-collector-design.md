# Mac kernel collector: design

**Status:** proposed, not yet built. Requirements: [mac-collector.md](mac-collector.md). The prototype ([../apps/mac/prototype/](../apps/mac/prototype/)) proved the Endpoint Security chain and produced three real captures; this design turns it into the collector. Names (package, collector, flags) are proposals and wait for the owner's say. Backlog `fec08b8`.

## Context

The Mac runs in agent logs only mode: nothing confirms what the agent reported. The prototype, a Swift system extension subscribing to Endpoint Security (ES) and writing JSON lines to a file, ran on the owner's Mac against the real Claude Code driven by a local model. Three captures (21,933 events at the largest) showed what the design must handle:

- The agent's tree, shell commands and file changes are all visible, including a write outside the project folder.
- 54% of the events were `close`, 93% of the writes went to the terminal, and 70% of the opens were system libraries or the agent's own state. Most of the volume is not evidence.
- Claude Code's file write is a temp file plus a `rename`; its shell commands arrive as `zsh -c "source <snapshot> … && eval '<command>' … && pwd -P >| /tmp/claude-<n>-cwd"`; the keychain files show as opens by the `security` command.
- The extension did nothing, silently, until Full Disk Access was on, and a rebuilt extension with the same build number installed nothing.

## The mechanism

```
 ES (kernel)          apps/mac (Swift)              sentientd (Go, root)
 ───────────          ────────────────              ────────────────────
 exec fork exit ─►  extension: keeps the agent  ─►  internal/collector/<es>:
 create write         tree, mutes noise, makes       socket server, decode,
 unlink rename        one JSON line per event        normalize, paths
 open                 (Unix socket, root-only)    ─► FilterSink ─► GateSink ─► StoreSink
                                                      ─► processes, kernel_events
```

Everything right of the socket is the existing pipeline (`internal/collector`: `Collector`, `Sink`, `FilterSink`, `GateSink`, `StoreSink`; `internal/store/kernel.go`: `Process`, `KernelEvent`). The Mac adds one collector and one Swift program, and changes nothing for Tetragon.

### The extension (`apps/mac/`)

A system extension (the prototype's `SentientES` target) with a small host app that installs it. It does four things and nothing else.

1. **Subscribe** to exec, fork, exit, create, write, unlink, rename and open, all notify events (it can never block anything). `close` is not subscribed.
2. **Mute noise in the kernel.** ES can mute events by target path prefix (`es_mute_path` with the target-prefix type, to be confirmed on the minimum macOS), so `open` of `/System`, `/usr`, `/Library`, `/dev`, `/bin`, `/opt/homebrew` and the agent's own state never reaches the extension. Sensitive paths (keychains, `~/.ssh`, `.env`, cloud and token files) are never muted. Terminal and `/dev/null` writes are muted the same way.
3. **Keep the agent's tree.** A process whose executable path starts with an agent prefix is a root; fork and exec descendants are followed; an exit ends it. Only events of the tree leave the extension (the prototype did exactly this).
4. **Send**, one JSON line per event, to `sentientd` over a Unix socket (below), counting what it drops.

**Process identity.** A pid is reused, so the extension builds an `exec_id` from the pid, the process's pid version (from the audit token) and its start time: `es:<pid>:<pidversion>:<start>`. The Go side stores it as `store.Process.ExecID`, with `ParentExecID` from the parent's pid and version.

**Write coalescing.** A program writing a file in many small writes would produce thousands of events for one change. The extension reports the first write to a path by a process and a count when the process closes or exits that file; the exact rule is an open question.

### The socket and its messages

`sentientd` listens on a Unix socket under a root-only directory (the extension runs as root and connects as a client; it reconnects when the daemon restarts, with a small bounded buffer in between and a drop counter beyond it). Messages are one JSON object per line with a `v` (schema version) and a `t` (kind):

- `hello`: the extension's version and build, the macOS version, a boot id, and the result of `es_new_client` (success, or the failure code). **This is how Health learns about a missing Full Disk Access:** the extension connects and says `es_new_client` failed with the not-permitted code, then exits, instead of dying silently.
- `config` (daemon to extension, in reply): the agent prefixes, the noise prefixes for the configured capture level and the sensitive-path list. The extension keeps the last config in memory and has a built-in default for a first boot with no daemon. Agent paths therefore live in one place, the Go side.
- `exec`, `exit`, `file` (with `op`: create, write, unlink, rename, open; `path`; `path2` for a rename; `flags` for an open), carrying the sequence number, the time, the `exec_id` and the process fields.
- `stats`: counts of dropped events by reason, so a drop is visible (requirement 4) and the sequence numbers are checked for jumps.

JSON lines, not protobuf, for the same reason the store keeps `KernelEvent.Payload` verbatim as JSON: it is readable with `tail` and `jq`, a capture is a fixture as it is, and the volume is small once the noise is cut.

### The Go side (`internal/collector/<es>`)

A package implementing `collector.Collector` (`Name`, `Run(ctx, sink)`). It owns the socket server, decodes the lines, and maps them to the `Sink` calls the Tetragon normalizer makes (`tetragon/normalize.go`):

| from the extension | to the sink |
|---|---|
| `exec` | `Process` (collector name, `ExecID`, parent, binary, argv, cwd, uid, start) and an `exec` event |
| `exit` | `Exit(execID, at, code)` |
| `file` create, write, unlink | `Event` types `file_create`, `file_write`, `file_unlink`, `Path` |
| `file` rename | `file_rename`, `Path` the source and `Path2` the destination |
| `file` open | `file_open`, `Path`, with the flags in the payload |
| stream ended, never connected, sequence jump, extension not running | `GapOpened` and `GapClosed` (a `monitor_gaps` row, as for Tetragon) |

`FilterSink` still applies as the safety net behind the extension's mutes, with a macOS noise list beside `policy.systemNoise`, so a more verbose extension cannot grow the database, and the capture level (`essential`, `extended`, `complete`) means the same thing. `GateSink` (disk pause) and `StoreSink` are unchanged.

**Status for Health.** The collector reports a `collector.Status` (system "Endpoint Security", detail "extension 1.0/3, connected") and one of the states: connected, never connected, disconnected, extension failed to start with the reason from `hello` (Full Disk Access missing). As root, `sentientd` can also run `systemextensionsctl list` to say "installed but waiting for approval", which the extension itself cannot tell it. The Kernel capture card (`internal/web/capturecard.go`) gets the new states and the line "network: not captured on the Mac yet".

**Paths.** The kernel reports `/private/tmp/x`; the agent's log says `/tmp/x`. Kernel paths are stored verbatim, as evidence, and one small function (`internal/paths`, proposed) canonicalizes both sides at comparison time: `/tmp`, `/var` and `/etc` into `/private`, and the `/System/Volumes/Data` firmlink prefix removed. The merge, attribution and the outside-the-repo rule use it.

### Matching the agent's actions

Same pairing as Linux (`internal/merge/merge.go`), with the Mac's shapes:

- **Shell commands.** The Bash tool's command is in the kernel's `zsh -c` argument as `eval '<command>'`, inside the snapshot wrapper. The pairing extracts the quoted command (undoing the shell quoting) and compares it with the logged command, as it does for the Linux wrapper shells. The wrapper is plumbing, and the command's own children (`ls`, `git`) are the evidence.
- **File changes by the agent's own tools.** The write is a temp file plus `rename` (`<name>.tmp.<pid>.<hex>` to `<name>`). A logged file change is confirmed by a kernel `file_rename` whose `Path2` equals it, or a `file_write` or `file_create` on it, whichever comes in the window.
- **Other writers.** The Python script that wrote `data.txt` ran as the Homebrew `Python.app`, not a shell; it is attributed by its own `exec_id`, as a child of the shell command that started it.
- **Plumbing** (`internal/background`, `IsToolRun`): Claude's start-up and housekeeping are classified by rule so they are not shown as agent actions: `security` lookups of its own login, `ioreg -rd1 -c IOPlatformExpertDevice`, `ps aux | grep` for IDEs, `rg --files`, `git ls-files`, and its own files under `~/.claude` and `/private/tmp/claude-<uid>/`. These rules are added to the existing set with a version bump (`RulesVersion`).
- **Credential reads.** A rule flags an agent tree reading a keychain file or running `security find-generic-password`, `gh auth token` and similar. The agent's own known login lookups are labeled as that, not hidden. It joins the rules in `docs/rules.md`.

### Install, run and update

`sentientd` runs as root under launchd on the Mac, with `--home` covering `/Users/*`, as on Linux. A new flag turns the collector on, beside `--tetragon` (`cmd/sentientd/commands.go`); its name is for the owner to choose. The host app, the extension, `sentientd`, `sen` and the launchd plist ship in one signed, notarized pkg ([mac-collector.md](mac-collector.md), requirements 15 to 19). The version is one number for the whole product ([decisions.md](decisions.md)); macOS replaces the extension only when the build number rises, which the build script must guarantee.

## Why this way, not that

- **ES target-path muting plus a user-space tree, not process inversion.** ES can invert muting so only listed processes report, which would cut the events at the source for the tree. But every fork would need the child added to the list from inside the handler, and a missed one is a lost process. Muting known-noisy target prefixes and filtering the tree in the extension is simpler and loses nothing; measure before the complicated version.
- **The tree in the extension, not in the daemon.** The extension drops 90% of events before they cross the socket, and it keeps working (and counting) when the daemon is down. The agent list still comes from the daemon, so there is one source of truth. If this turns out to be wrong, the daemon can keep a second tree as a check.
- **`open` for reads, not unmodified `close`.** `close` with `modified: false` was the only read signal in the first capture and was 54% of all events, with no flags. `open` says read or write and carries the flags; `write`, `create` and `rename` carry the changes.
- **Verbatim kernel paths, canonicalized at comparison.** Rewriting paths on the way in would change the evidence and its hash chain. Resolving at comparison keeps the record as the kernel gave it.
- **A Unix socket with JSON lines, not XPC or protobuf** (decided in the brief): the Go side cannot speak XPC easily, and JSON lines can be read by a person and replayed from a file.
- **The extension reports its own startup failure,** instead of the daemon guessing from silence: the first prototype run showed a missing Full Disk Access is the likeliest first failure, and "enabled but silent" is the worst thing for an evidence tool to show.
- **Not `eslogger`:** the entitlement was approved, so the interim tool is gone; `eslogger` has no kernel-side muting and the same events.

## Build order

1. **Go side with fixtures (builds and tests on Linux).** The collector package, socket server and decoder, normalizer, `internal/paths`, the Mac noise list, tests from the sanitized captures. The extension can be replaced by a file replayer, so this needs no Mac.
2. **The extension, production form (Mac).** The socket client, `hello` and `config`, kernel muting, the tree, the counters, a version-bump check in the build script. Replaces the prototype's file writer.
3. **Matching and rules.** The Mac plumbing rules, the `eval` pairing, the rename pairing, the credential rule.
4. **Health.** The card states, the approval and Full Disk Access messages.
5. **Packaging.** The pkg, launchd plist, signing and notarization (`710d18f`).
6. **Network** (backlog `cd35122`), separately.

## Open questions

1. **Names.** The package (`internal/collector/es`?), the collector name stored in `processes.collector` (it is `tetragon` or `eslogger` today), the `sentientd` flag, and the Mac noise-list file. For the owner.
2. **Agent list when the daemon is not connected:** a built-in default in the extension, or capture nothing until the first `config`. Leaning built-in, since a boot with no daemon must not lose evidence.
3. **Write coalescing rule:** first write plus a count on close or exit, or a fixed time window per path.
4. **Kernel muting reach:** whether target-prefix muting covers `open` for every prefix on the minimum macOS, and the oldest macOS supported.
5. **How many opens are left on a heavy workload** (a build or a test run). The three captures were small; one realistic run decides whether cache directories need muting too (backlog item for it).
6. **Several users:** ES sees every user's processes; the tree filter only keeps agents, and attribution ties them to a session by cwd and time as on Linux, but a second logged-in user running an agent has not been tried.
7. **Sensitive paths:** the list, and whether it is shared with Linux's "never filtered" classes.

## Related

- [mac-collector.md](mac-collector.md): the requirements this implements.
- [ideas/mac-collector.md](ideas/mac-collector.md): the brief, the decisions and the three captures.
- [tetragon-integration.md](tetragon-integration.md), [capture-verbosity.md](capture-verbosity.md), [kernel-capture-card.md](kernel-capture-card.md), [rules.md](rules.md), [design.md](design.md).
- Code: `internal/collector/` (interfaces, `FilterSink`, `GateSink`, `StoreSink`), `internal/collector/tetragon/normalize.go` (the model for the normalizer), `internal/store/kernel.go`, `internal/merge/merge.go`, `internal/attribute/attribute.go`, `internal/background/background.go`, `apps/mac/prototype/`.
- Backlog: `fec08b8`, `cd35122`, `346867d`, `710d18f`.
