# Capture verbosity

**Status:** policy generator built and verified on Tetragon 1.7.1 (`sen policy --verbosity`); the `sentientd` setting, `capture_levels`, Health checks, `extended` summaries and `complete` export are proposed, not yet built. Backlog `bbee4b1` (the setting), `27f6732` (the policy generator). Levels: `essential` (default), `extended` and `complete`, named like the lean and complete data-collection presets of endpoint products.

How much of what the kernel reports sentient records, as one setting with a default that leaves out the noise. Part of the storage work: see [storage-design.md](storage-design.md) "Containing the size" for the other levers (compression, large records by reference, retention, the archive tier). What the collector and the merge do with the events that are recorded is in [tetragon-integration.md](tetragon-integration.md).


## Context
On this machine on 2026-10-29, 741,698 of 842,987 kernel events (88%) came from one day of repeated `go build` and `go test` runs. 693,470 of the events are `file_open`, and 544,421 of those are opens of the Go build cache (275,000), the Go toolchain and standard library (106,000), system libraries and `/usr/share`, and Claude polling `/proc/<pid>/stat` (105,000). The database is 680 MB (kernel events 363 MB with indexes, transcripts 255 MB). The disk is 58 GB with 40 GB free, of which about 15 to 20 GB is a sensible budget once headroom and the working space `VACUUM` and backups need are left out. Quiet days cost 0.5 to 5 MB; a build-heavy day costs hundreds.

## Levels
One setting, three values. The default records the evidence and leaves out the noise; the most verbose keeps everything, for a deployment that has the storage and the retention process for it.

| level | what is recorded | who filters |
|---|---|---|
| **essential** (default) | every `exec` and `exit`, `net_connect`, file create, unlink, rename and chmod, `kill`, and `file_open` except the noise locations below | the Tetragon policy, so the kernel does not report the noise at all |
| **extended** | essential, and the noise is counted per command instead of dropped: "`go test` opened 41,000 files: 38,000 in the build cache, 2,100 in the repo" (one summary row per command and location group) | `sentientd` counts events it does not store |
| **complete** | every event Tetragon reports, and the rotating raw export | nobody |

## The noise locations

Never recorded at essential. The Go build cache, module cache and toolchain; the equivalents for other ecosystems (cargo registry, npm and nvm caches, pip caches); system library directories (`/usr/lib`, `/lib`, `/usr/share`, `/etc/ld.so*`); and `/proc/<pid>/stat` and `statm`. **Kept even at essential:** the rest of `/proc`, because `/proc/<pid>/environ` and `mem` are how one process reads another's secrets; the repo; the home directory; and everything not named above. This is a block list, not an allow list, so a path nobody thought of is still recorded.

## Mechanism

- `sen policy --verbosity <level>` generates the policy (`internal/collector/tetragon/policy/sentient.yaml.tmpl`) with the exclusions for this machine: it finds them (`go env GOROOT GOCACHE GOMODCACHE`, `$CARGO_HOME`, the npm and nvm caches) and reads extra paths from a config file. The user installs it; sentient never installs anything.
- `sentientd --verbosity <level>` applies the same filter at ingest as a safety net, so a policy that is more verbose than configured cannot grow the database. At `extended` it is the only filter, and it writes the per-command summaries.
- The level is recorded with the data: a `capture_levels` table (time, level, source) gets a row when `sentientd` starts and whenever the level changes, immutable like `monitor_gaps`. The pages and the audit read it, so "never recorded at that level" is not mistaken for "the kernel missed it".
- Health shows the policy's level and `sentientd`'s and warns when they differ, shows free space and warns at 70% used (below 30% free, with sentient's own size and growth in the message), is critical at 90% used, and stops recording at 95% used (5% free) and says so plainly; the thresholds are configurable. Recording must never fail silently.
- At `complete`, the Tetragon export file is on with rotation capped by days and by size. It is the lossless layer for forensics and for replaying the merge and the collector in tests.

## Changing the level

**What to restart.** The level lives in two places, so a change touches one or both:

| you change | restart | effect |
|---|---|---|
| sentientd's `--verbosity` | sentientd | immediate; storage drops, Tetragon still does the extra work |
| the policy (`sen policy --verbosity ...`, installed) | Tetragon only | it does not reload a changed policy; the stream has a gap while it is down, which sentientd records, and sentientd reconnects by itself (it retries every 2 seconds) |
| both (the proper switch) | Tetragon, and sentientd | the levels match and Health shows no warning; sentientd is restarted only because its own setting changed |

To go from `complete` to `essential`: install the new policy, restart Tetragon, then set sentientd to `essential` and restart it. Changing only sentientd gives the storage saving at once, and Health warns that the policy is more verbose than configured. An agent that was running when Tetragon loaded the policy can have its commands recorded but not the files and connections they touch (`sen policy --check` reports it), so restart the agent after restarting Tetragon.

**Lowering the level never deletes anything.** The evidence tables are append-only and hash-chained (triggers reject `UPDATE` and `DELETE`), so a change only affects what is recorded from then on. Events already stored stay, noise included. A `capture_levels` row marks the switch, so the pages and `sen audit` read the period before it as recorded at the old level and the period after it as recorded at the new one.

**Raising it cannot recover the past.** What an `essential` period did not record, an upshift to `complete` does not bring back (at `extended` only the per-command counts exist). Lowering is the safe direction; raising is lossy for the time in between.

**Space comes back only through a separate command, never as a side effect of the setting.** The setting changes what is recorded from then on; removing what is already stored is `sudo sentientd prune`, in the way of `docker prune`:
- **Root only, through the daemon.** The evidence is meant to resist the monitored agent, so the command is not reachable over the user socket that `sen` and the web page use. An agent running as the user could otherwise erase the record of what it did. (An agent account with passwordless sudo can run it, which is the warning Health already shows.)
- **Dry run by default.** It lists what it would remove and the space it frees, and changes nothing until `--force` or an interactive confirmation.
- **Targets:** noise events that the current level would not record, older than a date; everything older than a date (retention); and the raw export files.
- **It is itself recorded.** Every prune appends a permanent row: when, the filter, how many rows and bytes, and the hash span removed. The chain carries across the removed span, so verification still passes and the removal is visible. The archive tier ([storage-design.md](storage-design.md) "Stage 2") is the other way space leaves the hot database, by whole sealed segments.

The raw export is not evidence: it stops when the level drops below `complete`, its files age out by their day and size caps, and `prune` or the user may delete them.

## Estimated size

Estimates (this dataset, 842,987 events over five recorded days; estimates from real row lengths with indexes scaled by row count, to be replaced by measurements from a database actually recorded at each level):

| level | kernel events kept | kernel events size | whole database |
|---|---|---|---|
| complete (today) | 842,987 | about 363 MB | about 680 MB |
| essential | 298,566 | about 125 MB (-66%) | about 410 MB |
| essential and 32-byte hashes (drop `prev_hash`) | 298,566 | about 96 MB | about 375 MB |
| extended | 298,566 and about 5,000 summary rows | about 131 MB | about 415 MB |

The transcripts then dominate and grow with agent use, not with build noise. With a 15 GB budget and 330 MB a day (this machine's worst day, complete), that is about 45 days; at essential about 125 days; on an average day of these five, 200 days at complete and 600 at essential. The raw export adds about 1 KB per event, up to 1 GB a day on a worst day, so it needs its cap. The existing evidence does not shrink when the level is lowered (it is append-only): only new data is smaller, until the archive tier.

## Why this way, not that

- **By location, not by process depth.** "Record only what the agent's own commands open, not what they start" would drop the build noise but also the cases that matter: `npm run build`, `make` or `./deploy.sh` is one command, and a malicious step inside it reads `~/.ssh` a level or two down. A compiler reading the build cache is noise at any depth; reading `~/.ssh` is evidence at any depth.
- **A block list, not an allow list.** An allow list ("only the repo and home") silently misses a sensitive path nobody listed.
- **Filter before the write, not after.** The evidence tables are append-only, so noise stored once is stored until the archive tier. At essential the policy keeps it out of the stream; at extended `sentientd` counts it and drops it.
- **Exec, network, writes and signals are never filtered.** A payload hidden in a build or a test has to run something, connect somewhere or change files, and all of those stay recorded at every level. Only `file_open` of known noise locations is dropped.
- **One setting, not a flag per event type.** Three named levels are easier to reason about, document and verify than a matrix; a finer knob can come later if a real case needs it.


## Open questions

- **Names (decided):** `essential`, `extended` and `complete`. The nosaka app's `concise`/`abridged`/`unabridged` was considered and rejected: those words describe text output for a front end, not a capture level for a system.
- **Testing a policy:** a policy loaded at runtime (`tetra tracingpolicy add`) is validated, but Tetragon does not follow the children of processes that already existed when it was loaded, so the running agent's commands record no file opens. A runtime load proves the file is accepted; whether children's opens survive the exclusions can only be seen with the policy installed in the policy directory and Tetragon restarted (the agent, already running, is then marked at startup), or with a fresh agent process.
- **Tetragon's behaviour (verified 2026-10-30, 1.7.1):** the essential policy loads, and with a fresh agent a `go test` run showed `link` opening 25 files where unfiltered runs showed about 42,000 (40,016 in the build cache), with the repo opens still recorded. Still to check: `/proc/<pid>/environ` stays recorded.
- **`extended` grouping (decided):** by named category (Go build cache, Go toolchain, system libraries, ...), the label each noise location carries; not by directory prefix.
- **Default caps for the raw export** (days and size), and whether 70% used / 90% used / 95% used are the right default thresholds (a percentage is early on a very large disk, hence the size and growth in the message).
- **A level change at runtime:** whether it needs a restart.
- **`sentientd prune`:** how the removal carries the hash chain across the removed span (a record holding the removed rows' chain end and count), whether it can drop single noise rows or only whole sealed segments, and how a root-only command reaches the daemon (a separate admin socket, or run offline against the database file). Decided with the archive tier.
- **Evidence recorded at `complete`** after a switch to `essential`: how the pages show it (the `capture_levels` rows say where the change was).
- **Upgrade default:** an existing database was recorded at `complete` before this setting existed; the first start records "complete (before this setting)" and then runs at the configured level, essential unless set otherwise.

## Related and deferred

- [storage-design.md](storage-design.md): size containment, retention and the archive tier; the raw export file question.
- [tetragon-integration.md](tetragon-integration.md): the collector, the merge, and which events each feature reads (file reads confirm the agent's Read calls, so repo opens must stay recorded).
- [mocks/cli/audit.md](mocks/cli/audit.md): `sen audit` reads `capture_levels` to tell "not recorded at this level" from "missed".
- Backlog: `bbee4b1` (verbosity levels), `27f6732` (`sen policy --verbosity`), `71372a4` (`sen audit`), `647e1c0` (corpus tooling), `65d950c` (file effects of a command).
