# Tetragon integration: build plan

**Status:** proposed, not yet built

The *what* is in [design.md](design.md#linux-collector-tetragon) (policy,
gaps, noise) and the evaluation is in [tetragon.md](tetragon.md). This is the
*how*: where the code goes, what it writes, how kernel events get tied to a
session and a prompt, and the order to build it in.

## Context

Everything sentient shows today comes from the agent's own log, so every row
says `evidence: log`. Two real consequences:

- `rm -rf .claudetmp/e2e` appears on the activity page as one command. The
  log holds the command text, not the files it deleted, so the page can't say
  what the command did. The same goes for `mv`, `sed -i`, a build script, or
  anything an agent does outside its own tools.
- Nothing is independent of the agent. A log can be incomplete or wrong, and
  the point of sentient is that the agent isn't its own witness.

## Mechanism

**Collector interface.** `internal/collector/collector.go` defines what a
collector hands the store (processes, kernel events, gaps), the same way
`internal/ingest` will get an adapter interface for Codex (backlog `2aa3ed9`).
`internal/collector/tetragon/` is the Linux implementation; the macOS
`eslogger` and Endpoint Security collectors come later behind the same
interface.

**Files in `internal/collector/tetragon/`:**

- `client.go`: one gRPC `GetEvents` stream from Tetragon, with reconnect.
  Uses the `github.com/cilium/tetragon/api` Go module.
- `normalize.go`: Tetragon event to sentient record (strip ` (deleted)`,
  treat tmp-file + rename as a change to the target path, drop zero exit
  status), tested against fixtures from the evaluation's `run1`.
- `policy.go` and `policy/sentient.yaml` (embedded): the TracingPolicy from
  design.md. Selectors use `matchBinaries` + `followChildren` on the agent
  binaries and path filters for library and `/proc` noise.
- `procscan.go`: at startup, find agent processes already running and add a
  `matchPIDs` + `followForks` policy for them; record a `monitor gap` for the
  time before.
- `gaps.go`: poll Tetragon's loss counters and its pid/start time; any loss or
  restart is a `monitor gap`, and an unreachable Tetragon shows on Health.

**Store.** Migration 5 adds the three tables design.md already lists:
`processes` (exec_id, parent_exec_id, pid, binary, argv, cwd, session and
prompt links), `kernel_events` (hash-chained like `raw_log_records`) and
`monitor_gaps`. `internal/store/write.go` gets `AddProcess`, `AddKernelEvent`
and `AddGap`.

**Tying a kernel event to a session and prompt.**

1. The agent's root process is the one whose binary matches. Its `cwd` and
   start time are matched to the log's session (same `cwd`, first record
   within a few seconds of the exec).
2. Every descendant, by `exec_id` ancestry, belongs to that session.
3. A kernel event belongs to the prompt whose time window contains it.
4. A `command execute` from the log and a kernel exec with the same argv in
   the same session within about 2 seconds are one action:
   `actions.evidence` becomes `both`. A kernel exec with no log match is added
   as `kernel` only, and one the agent's tools can't explain is what the alert
   rules look for.

**File effects of a command** (backlog `65d950c`). File unlink, write, rename
and chmod events carry the exec that caused them, so `query` can return "this
command deleted 4 files, wrote 2". `/activity` and the prompt trace get an
expand control on a command row. No new capture is needed beyond the policy.

**Daemon wiring.** `internal/daemon/daemon.go` starts the collector beside the
ingester and the API server. Tetragon is installed and run on its own by its
standard method; `sentientd` only loads the policy and reads the stream.

**Testing.** Tetragon can't run inside `go test`, so the logic is tested with
recorded event fixtures (normalization, session attribution, evidence
merging, gap detection, hash chain). A manual end-to-end check on the Linux
box (48 MB RSS measured) comes at each build step below. Coverage stays at 70%
or more per package.

## Build order

1. Migration 5, store writes, `normalize.go` and its fixtures.
2. `client.go`: stream into `kernel_events` and `processes`, hash chain
   verified by the existing integrity check.
3. Session and prompt attribution.
4. Merge with log actions (`evidence: both` / `kernel`).
5. Gap monitor, Health card, `/proc` scan and the `matchPIDs` policy.
6. UI: evidence tags back on the trace, file effects on command rows.

## Why X, not Y

- **Tetragon, not our own eBPF programs.** Decided in design.md: we need
  about a fifth of Tetragon, but writing and maintaining kprobes, uprobes and
  the process tree is a lot more than a gRPC client.
- **gRPC, not the JSON export.** An event with ancestry was about 10 KB as
  JSON; gRPC with ancestors off and our own tree from `exec_id` is far
  smaller.
- **Our own process tree, not Tetragon's ancestors.** Exec events are few and
  the tree is cheap to keep; per-event ancestors are most of the size.

## Open questions

- **Session to process mapping.** Matching on `cwd` + start time works for one
  session per directory. Two Claude sessions started in the same directory in
  the same second need another key (the agent's pid is not in its log).
- **Who installs the policy.** `sentientd` runs as root and could write
  `/etc/tetragon/tetragon.tp.d/sentient.yaml`, or `sen install` does it once.
- **`cilium/tetragon/api` module size** against a 2 GB box's build memory;
  measure the first build with `GOFLAGS=-p=1 GOMAXPROCS=1`.
- **Volume.** design.md estimates about 180 MB per hour of agent activity;
  `kernel_events` needs a retention or compression plan (backlog `6efed70`).
- **npm installs** of Claude Code run as `node`, so the in-kernel binary
  filter doesn't fit; filter by ancestry in user space (deferred).

## Related and deferred

- `docs/rules.md`: the alert rules that consume kernel evidence.
- Backlog: file effects of a command (`65d950c`), adapter interface for Codex
  (`2aa3ed9`), compressing raw records (`6efed70`), database backup
  (`477932f`).
- Deferred: DNS answers from systemd-resolved for static binaries, the
  macOS collector, a watchlist of sensitive files snapshotted at session start.
