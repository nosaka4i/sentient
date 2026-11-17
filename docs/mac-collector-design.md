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

**The extension does no normalization.** It sends the fields almost as ES gave them (event names, paths verbatim, raw open flags, pid, pid version and start time as separate fields) and applies only what `config` tells it: the agent prefixes, the tree it keeps (the one piece of logic, a set of pids; without it every process on the Mac would cross the socket) and the kernel mutes. All normalization is in `sentientd`.

**Process identity.** A pid is reused, so the extension sends the pid, the process's pid version (from the audit token) and its start time as separate fields. The Go side builds `exec_id` as `es:<pid>:<pidversion>:<start>`, stores it as `store.Process.ExecID`, and builds `ParentExecID` from the parent's pid and version.

**Write coalescing.** A program writing a file in many small writes would produce many events for one change. The extension sends them all; `sentientd` collapses repeated writes by one process to one path into one stored event with a count, per capture level (open question 3, decided).

### The socket and its messages

`sentientd` listens on a Unix socket under a root-only directory (the extension runs as root and connects as a client; it reconnects when the daemon restarts, with a small bounded buffer in between and a drop counter beyond it). Messages are one JSON object per line with a `v` (schema version) and a `t` (kind):

- `hello`: the extension's version and build, the macOS version, a boot id, and the result of `es_new_client` (success, or the failure code). **This is how Health learns about a missing Full Disk Access:** the extension connects and says `es_new_client` failed with the not-permitted code, then exits, instead of dying silently.
- `config` (daemon to extension, in reply): the agent prefixes, the noise prefixes for the configured capture level and the sensitive-path list. The extension keeps the last config in memory and has a built-in default for a first boot with no daemon. Agent paths therefore live in one place, the Go side.
- `exec`, `fork`, `exit`, `file` (with `op`: create, write, unlink, rename, open; `path`; `path2` for a rename; `flags` for an open, raw), carrying the sequence number (`seq`), the time (`ts_sec`, `ts_nsec`) and the process fields (`pid`, `pidver`, `ppid`, `ppidver`, `start`, `uid`, `exe`; `args` and `cwd` on exec; `status` on exit). The wire schema is `internal/collector/es/message.go` (`v` is 1). `fork` was missing from the first list: a forked child shares its parent's image and `exec_id` until it execs, as in Tetragon's model, so file events before the exec belong to the parent's exec.
- **Adoption.** After a `sentientd` restart the extension keeps running and its processes keep reporting without a new exec, so a message from a process the daemon has not seen start registers that process from the fields it carries (a process record with no argv). A message with no `exe` cannot be adopted and is dropped. On a new connection the daemon forgets its tracked processes, because an extension that restarted has a new tree.
- **Gaps from the stream.** The collector records: never connected, disconnected, no message for the ping timeout (30 seconds by default; the extension pings every few seconds), a start failure from `hello` (the reason stays on the Health status even after the extension exits), a jump in `seq` (the lost messages, as a gap between the two messages), and a `stats` report of dropped events (as a gap over the interval). The socket is created root-only (0600), replacing a stale file.
- `ping`: every few seconds, with the latest ES sequence numbers; no `ping` for longer than the timeout is a gap.
- `stats`: counts of dropped events by reason, so a drop is visible (requirement 4) and the sequence numbers are checked for jumps.

**No disk writes by default.** The production extension never writes events to a file (the prototype did, for the first captures). The only path is the socket, and the only copy on disk is what `sentientd` stores as events. A debug switch in `sentientd` (off by default) can record the raw stream to a file, to make fixtures or investigate a problem.

**Crashes and restarts.**
- *Extension crashes:* macOS restarts system extensions (to be confirmed by killing it on the Mac). The daemon sees the socket close and opens a `monitor_gap`; Health shows "not running" until the next `hello`. The restarted extension has lost its tree, so agents that were already running are unknown until they exec again: the gap stays open for them and nothing is claimed about them. Whether the extension can rebuild the tree from the running process list at start is an open question.
- *`sentientd` restarts:* the extension reconnects, buffers a bounded amount and counts drops beyond it; the sequence numbers show the gap.
- *Heartbeats:* there is one heartbeat table, in `sentientd` ([heartbeat.md](heartbeat.md)), with a row for the Mac collector; it covers the daemon being down. The extension writes nothing to disk, so its heartbeat is a `ping` message on the socket every few seconds carrying its sequence numbers. A closed socket opens a gap at once; a socket that stays open but stops pinging (a hung extension) opens one after a timeout. ES numbers its own events (`seq_num`, `global_seq_num`), the extension forwards them, and a jump means ES dropped events. A ping shows the extension is alive, not that ES is delivering; the ES numbers cover that.
- *Reboot:* the heartbeat ([heartbeat.md](heartbeat.md)) covers the daemon, and the extension's `hello` carries a boot id.

**macOS versions.** Apple's event structures never reach Go: the extension flattens each event into our JSON with a schema version, and the Go side ignores unknown fields. What varies by macOS version is which ES calls exist (target-path muting is newer than the nine events we subscribe to; without it the extension drops those events itself), and behaviour (paths, firmlinks, how `zsh` is launched, Rosetta). `hello` reports the macOS version and the ES message version, so every capture says what produced it. A minimum version is set (open question 4), and other versions are covered by fixtures from users' captures, since the owner can test only one.

JSON lines, not protobuf, for the same reason the store keeps `KernelEvent.Payload` verbatim as JSON: it is readable with `tail` and `jq`, a capture is a fixture as it is, and the volume is small once the noise is cut.

### The Go side (`internal/collector/<es>`)

**Code layout across platforms.** `internal/collector` holds the neutral parts; each platform is a sibling package: `tetragon/` (Linux, gRPC `GetEvents` with protobuf messages), the Mac package (Unix socket, JSON lines), and a Windows one later. The Mac package imports no Apple code, so it builds and tests on Linux with no build tags or cgo. `cmd/sentientd` chooses the collector from a flag and `runtime.GOOS`; build tags (`_darwin.go`, `_linux.go`) are only for code that cannot compile elsewhere, such as launchd or service startup. Platform differences in shared code (path rules, plumbing rules, default homes) are data or functions chosen by the collector, not scattered `GOOS` checks.

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

1. **Go side with fixtures (builds and tests on Linux). Built 2026-11-17.** `internal/paths` (canonical forms); `internal/collector/es` (`message.go` the wire schema, `normalize.go`, `collector.go` the socket server with its gaps and status, `noise.go` the Mac agent prefixes, noise and sensitive lists and the kernel mutes); in `internal/collector`: `CoalesceSink` (repeated writes counted once per process and path, reported on exit, at a gap and on a flush; at `complete` nothing is collapsed), `SensitivePaths` (the shared core list) and `FilterSink.Sensitive` (a sensitive open is kept even inside a noise location); `policy.NoisePath.Anywhere` (a path-segment match for dependency caches such as `/pkg/mod/`); and the `--endpoint-security` flag in `sentientd` (`daemon.Config.EndpointSecurity`, excluding `--tetragon`). The pipeline is gate, filter, coalesce, buffer, store. Kernel mutes are sent in `config` only at the `essential` level, and a mute prefix that holds a sensitive path (`/Library/` holds the keychains) is not muted in the kernel; those opens reach `sentientd`, which drops the noise and keeps the sensitive ones. Not yet done: the production extension (step 2), the Mac plumbing and credential rules and the `eval` pairing (step 3), and the Health card states (step 4).
2. **The extension, production form (Mac). Sources written 2026-11-17 in `apps/mac/Sentient/SentientES/` (`main.swift`, `Config.swift`, `Sender.swift`, `Events.swift`) and the host's `ExtensionInstaller.swift`; project set up and signing fixed (see the section below); not yet run.** The socket client, `hello` and `config`, kernel muting, the tree, the counters, a version-bump check in the build script. Replaces the prototype's file writer.
3. **Matching and rules.** The Mac plumbing rules, the `eval` pairing, the rename pairing, the credential rule.
4. **Health.** The card states, the approval and Full Disk Access messages.
5. **Packaging.** The pkg, launchd plist, signing and notarization (`710d18f`).
6. **Network** (backlog `cd35122`), separately.

## Setting up the Xcode project and signing (one-time, done 2026-11-17)

What it took to get `apps/mac/Sentient` signing with the Endpoint Security entitlement, so it is not rediscovered. This is the owner's individual developer account (team `RYJT9BBCQ8`), development signing. Distribution signing is a separate pass (`710d18f`, `346867d`).

**The project.** Created in Xcode as App (SwiftUI) named `Sentient`, saved into `apps/mac/` (the folder becomes `apps/mac/Sentient/`), organization identifier `ai.nosaka`, so the host's bundle id is `ai.nosaka.Sentient`. The extension was added with File > New > Target > macOS > System Extension > Endpoint Security Extension, named `SentientES`, id `ai.nosaka.Sentient.SentientES` (a system extension's id must start with its host's id). Version `0.1`, build `1` for both; the build number must rise on every extension build.

**Traps, in the order we hit them.**
1. **Xcode's "Create Git repository" box** puts a `.git` inside `apps/mac/Sentient`, and the outer repo then adds it as an embedded repository (one `new file: apps/mac/Sentient` entry). Delete `apps/mac/Sentient/.git`, then `git rm --cached -f apps/mac/Sentient` and add the folder again. Leave that box unticked next time.
2. **The template's placeholders** must be replaced: the entitlements file has a fake app group (`com.example.app-group`, remove it; the extension needs only `com.apple.developer.endpoint-security.client`), and `Info.plist` has `SYSTEM_EXTENSION_USAGE_DESCRIPTION` (the text of the approval prompt).
3. **The host needs its own entitlement**, `com.apple.developer.system-extension.install` (`Sentient/Sentient.entitlements`), or it cannot ask for the install. The folder is synchronized into the target, so the file is listed as a membership exception in the project.
4. **Deployment target:** Xcode defaulted to the newest macOS; set to 26.0, the minimum we support.
5. **"PLA Update available"** from automatic signing: Apple's Program License Agreement had a new version. Accept it in the developer account (and check App Store Connect > Business for a second pending agreement), then use "Try Again" on each target.
6. **Automatic signing could not make an Endpoint Security profile** for the new extension id: errors said the "Mac Team Provisioning Profile: *" lacks the Endpoint Security capability and the `endpoint-security.client` entitlement. What worked: in the portal, Identifiers > `ai.nosaka.Sentient.SentientES` > tick **Endpoint Security (development)**, then Profiles > + > macOS App Development, pick that App ID, your development certificate and your Mac, download, double-click to install, and choose it in the SentientES target (automatic signing off) as `Sentient ES dev 2`. The host stays on automatic signing; its System Extension capability worked after the agreement was accepted.
7. **The wrong App ID in the manual profile.** The prototype's lowercase `ai.nosaka.sentientes.SentientES` sits beside the new one in the list; the first profile was made for it and Xcode said the app ID did not match the bundle id. The new one has a capital S and a dot. Remove the wrong profile in the portal (Remove) and delete its copy in `~/Library/Developer/Xcode/UserData/Provisioning Profiles` if you can tell which it is.
8. **Stale build errors** after a fix: Product > Clean Build Folder.

**Needed again later.** A development profile covers only the registered Macs. Distribution (Developer ID) needs its own App IDs capability check and profiles, and the extension then needs the distribution variant of the entitlement. The Network Extension target will need the same App ID capability and profile steps. The prototype's App IDs and its installed extension (`ai.nosaka.sentientes.SentientES`) can be removed once the production extension works.

## Open questions

1. **Names (decided by the owner, 2026-11-14).** Package `internal/collector/es`; collector name stored in `processes.collector`: `endpointsecurity`; `sentientd` flag `--endpoint-security`; the extension and its bundle `SentientES`, with `sentientes` for scripts and paths; the future network extension `SentientNet` (placeholder). The Mac noise list is Go code beside `policy.systemNoise`, delivered in `config`; there is no policy file in v1, and a user-customizable policy for advanced users is backlog `9e99919`.
2. **Agent list when the daemon is not connected (decided by the owner, 2026-11-14):** v1 has a built-in default in the extension, used only until the first `config`; the daemon's list is authoritative and changes with a `sentientd` restart, not an extension rebuild. Later, when `sentientd` gets a config file (backlog `9e99919`), the agent list comes from it, and the extension saves the last `config` to a small file (not evidence) so a boot with no daemon still uses the user's list; the built-in copy then shrinks to a first-boot default.
3. **Write coalescing (decided by the owner, 2026-11-14):** in `sentientd`, not the extension. The extension sends every write; the daemon collapses repeated writes by the same process to the same path into one stored event with a count, according to the capture level (`complete` can keep every write). Reasons: the level decides how much detail to keep, and a rule change is a daemon restart, not an extension rebuild and possible re-approval. After the terminal writes are muted, the captures held only about 700 real file writes in 11 minutes, so the extra volume is small; question 5 measures a heavy workload. The extension keeps only the tree and the kernel mutes.
4. **Minimum macOS (decided by the owner, 2026-11-14):** macOS 26 only for now, Apple silicon only; older versions can be added later, each one tested or covered by fixtures from users. Kernel muting by target path is therefore always available. Still to confirm on a real 26 build: that target-prefix muting covers `open` for every prefix in the noise list.
5. **How many opens are left on a heavy workload (measured 2026-11-16).** A cold `go test ./...` (stand-in `tools/dev/agent-standin`, Go cache cleared, this repo) produced 294,981 events in 17 seconds: 146,069 writes, 77,843 closes (not subscribed), 55,144 opens, 8,340 creates, 4,659 unlinks, about 975 each of exec, fork and exit (435 `compile`, 411 `vet`, 88 `asm`, 18 `link`).
   - **Writes:** 146,069 events but only 6,880 distinct (process, path) pairs, 94% of them in the per-user temp folder (`/private/var/folders/…`, Go's `go-build` work dir and test databases) and the rest in `~/Library/Caches/go-build`; none in the project. Coalescing in `sentientd` (question 3) takes this to about 7,000.
   - **Opens:** 55,144, of which 19,306 in `/private/var/folders`, 12,967 in the Go module cache (`~/work/pkg/mod`; the owner's GOPATH), 11,034 in `/usr/local/go`, 2,931 in `/System`, 2,876 in `~/Library/Application Support/go`, 1,282 in `~/Library/Caches/go-build`, and 1,750 in the project. Distinct (process, path) pairs are still 44,452, so de-duplication barely helps; muting by prefix is the lever. Muting `/System`, `/usr`, `/dev`, `/Library`, `/private/var/folders` and `~/Library` leaves 15,741, of which 12,967 are the module cache.
   - **Decisions this gives:** add `/private/var/folders` and `~/Library/Caches` to the kernel mute list for opens; the module cache has no fixed prefix (GOPATH can be anywhere), so it is a user-space rule in `FilterSink` (a `/pkg/mod/` segment, and similar dependency caches such as `node_modules`, `~/.cargo/registry`, `~/.npm`) and the policy item `9e99919` for machine-specific ones. Writes under the temp folder are not muted at the kernel (an agent writing there is evidence); coalescing handles the volume.
   - **Rate:** about 17,000 events per second raw during the build, which is why the extension mutes in the kernel and does not rely on the daemon to drop.
6. **Several users (moved out, 2026-11-16):** not specific to the Mac; Linux has the same situation. Tracked as backlog `47145c2` for all platforms. The Mac collector behaves as the Linux one does.
7. **Sensitive paths (decided by the owner, 2026-11-16):** a shared core list in Go (`~/.ssh`, `.env*`, `~/.aws`, `~/.config/gh`, `~/.gnupg`, token and credential files) plus platform extras, which for the Mac are `~/Library/Keychains` and `/Library/Keychains`. A new core entry reaches every platform. It is Go code for now and moves under the user-editable policy later (`9e99919`). The exact core list is settled when it is written, against the Linux "never filtered" classes.
8. **Tree after an extension restart (decided by the owner, 2026-11-16: deferred, not in v1).** v1 leaves the gap open: after a restart the extension's `hello` opens a gap and agents already running are not tracked until they exec again, so nothing is claimed about them. The agreed later design is in the backlog (p1): `sentientd`'s stored live processes are the source of truth and prime the extension's local set through `config` at `hello`; the extension validates each (pid, start time, executable path) with `libproc`, adopts the matches and scans for unknown agent processes; with the daemon down it scans only and sends a tree-rebuilt marker. The extension keeps a local set and extends it itself (a prefix match makes a root, a child of a member is added at fork), with no per-event query to the daemon, because of the event rate (about 17,000 a second in a build), a daemon that may be down, and the race on a new child's first events.

## Related

- [mac-collector.md](mac-collector.md): the requirements this implements.
- [ideas/mac-collector.md](ideas/mac-collector.md): the brief, the decisions and the three captures.
- [tetragon-integration.md](tetragon-integration.md), [capture-verbosity.md](capture-verbosity.md), [kernel-capture-card.md](kernel-capture-card.md), [rules.md](rules.md), [design.md](design.md).
- Code: `internal/collector/` (interfaces, `FilterSink`, `GateSink`, `StoreSink`), `internal/collector/tetragon/normalize.go` (the model for the normalizer), `internal/store/kernel.go`, `internal/merge/merge.go`, `internal/attribute/attribute.go`, `internal/background/background.go`, `apps/mac/prototype/`.
- Backlog: `fec08b8`, `cd35122`, `346867d`, `710d18f`.
