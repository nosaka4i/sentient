# Kernel collectors: build plan (Tetragon on Linux, eslogger on macOS)

**Status:** proposed, not yet built

The *what* is in [design.md](design.md#linux-collector-tetragon) (policy,
gaps, noise) and the evaluation is in [tetragon.md](tetragon.md). This is the
*how*: where the code goes, what it writes, how kernel events get tied to a
session and a prompt, and the order to build it in. The file keeps its name
for the Tetragon-era links; it now covers both platforms.

Platform split in one line: one `Collector` interface and OS-neutral record
types; one implementation per platform, picked at runtime by `GOOS` and what
is installed; build tags only for tiny OS-specific bits (paths, rusage).
Store, queries and web pages never know which collector produced a row.

## Context

Everything sentient shows today comes from the agent's own log, so every row
says `evidence: log`. Two real consequences:

- `rm -rf .claudetmp/e2e` appears on the activity page as one command. The
  log holds the command text, not the files it deleted, so the page can't say
  what the command did. The same goes for `mv`, `sed -i`, a build script, or
  anything an agent does outside its own tools.
- Nothing is independent of the agent. A log can be incomplete or wrong, and
  the point of sentient is that the agent isn't its own witness.

**Release scope.** v1 ships log-only: it turns the agent's own log into
sessions, prompts, files changed, commands and alerts, with nothing to install.
The kernel collectors are an opt-in "verify" mode on top: Tetragon on Linux,
`eslogger` on macOS. With no collector, Health shows "Kernel monitoring: off
(optional)" as a normal state, not a warning, rows keep their `evidence: log`
tag, and the alert rules that depend on kernel evidence do not fire. Everything
below is the verify mode.

## Mechanism

**Collector interface.** `internal/collector/collector.go` defines what a
collector hands the store (processes, kernel events, gaps), the same way
`internal/ingest` will get an adapter interface for Codex (backlog `2aa3ed9`).
`internal/collector/tetragon/` is the Linux implementation; the macOS
`eslogger` and Endpoint Security collectors come later behind the same
interface.

**Files in `internal/collector/tetragon/`:**

- `client.go` (built): one gRPC `GetEvents` stream from Tetragon, asking only
  for exec, exit and kprobe events. Opens a `monitor gap` when Tetragon is
  unreachable or the stream ends, closes it on reconnect, and returns when the
  sink fails. Uses the `github.com/cilium/tetragon/api` Go module (compiles
  here with `GOFLAGS=-p=1 GOMAXPROCS=1`; its own `go.mod` carries a local
  replace that does not affect us because the package never imports the root
  module).
- `normalize.go` (built): Tetragon event to sentient record. Keeps only the
  agent's process tree (a process whose binary matches `AgentPrefixes`, plus
  its descendants), strips ` (deleted)`, joins mount-relative paths to their
  directory, and maps file open/create/unlink/rename/chmod, mkdir/rmdir, TCP
  connect and kill. A rename is recorded as a rename; reading tmp-file +
  rename as an edit of the target is left to the merge step. TCP bytes and DNS
  are not recorded yet. Tested against a trimmed sample of `run1`
  (`testdata/run1.jsonl`).
- `policy.go` and `policy/sentient.yaml.tmpl` (built, embedded): the
  TracingPolicy from design.md, rendered per user by `Policy(homes)`. One
  policy in one file (not four), limited to the hooks the collector records:
  file open, create, unlink, mkdir, rmdir, rename, chmod, TCP connect, kill and
  tgkill. Byte counters, `file_permission`, `chown`, `truncate`, `ptrace`,
  `setuid` and the DNS uprobe from the evaluation are left out until the
  collector records them. Selectors use `matchBinaries` + `followChildren` on the
  agent binaries and path filters for library and `/proc` noise. The binary match
  lists exact file paths (`operator: "In"`): Tetragon rejects a `Prefix` match
  combined with `followChildren`, and a rejected policy stops the whole service.
  Claude's path contains its version, so it changes on every auto-update;
  `sen policy` is rerun after an update (see "When the policy changes").
- Policy-age check (not built): compares each agent root process's start time
  with Tetragon's start and warns on Health and in `sen policy --check` when the
  agent predates the policy. It does not change the policy or touch the agent;
  see "When the policy changes". Health also says "agent running, no kernel
  events" when an agent process exists but nothing matched it.
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
   within a few seconds of the exec). This is exact for one session per
   directory; for concurrent sessions use the session id in the environment
   and the command-text match (see "Concurrent sessions").
2. Every descendant, by `exec_id` ancestry, belongs to that session.
3. A process belongs to the prompt in effect when it was exec'd, and its
   later events inherit that; other events belong to the prompt whose time
   window contains them.
4. **Merging the log's commands with kernel execs** (`internal/merge`). The
   log stamps a Bash call when the agent issues it; the command runs only after
   any approval, which can take minutes or days. So there is no time window.
   Claude Code runs each Bash call as `bash -c "source snapshot && eval '<the
   whole command>' < /dev/null && pwd -P >| file"`, so the kernel's record of
   that shell holds the same command text the log has.
   - A Bash call matches the wrapper shell of the same session whose `eval`
     text equals the call's command (quotes, backslashes and whitespace
     ignored). Equal calls in one session pair in order, each shell once.
   - The shell must have started after the call was logged and before the call
     ended: the call's result was logged, or its prompt ended (an interrupted
     call), whichever is first, with a few seconds' slack. A call with neither
     is still waiting for approval or still running, and matches whenever its
     shell appears.
   - Each part of a chained command pairs, in order, with a direct child of
     that shell of the same program and arguments. A match sets
     `actions.evidence` to `both` and `exec_id`. Parts with no match stay `log`
     (never run, denied, or the kernel was not watching).
   - A direct child of a matched wrapper shell that matches no part, and a
     direct child of the agent itself (the routine commands below), become
     `kernel` command actions. So does a wrapper shell no call matched, as one
     action showing the command it ran (its children are not listed
     separately). Grandchildren (a compiler under `go test`) stay in the
     process tree and are not actions. A kernel action the agent's tools can't
     explain is what the alert rules look for.
   - A kernel exec is turned into an action only once the session's log has
     reached its start time, so a call that is logged late can still claim it.
     Kernel actions are not counted in a prompt's action total; the views count
     and group them.
   - A call is settled, and no longer examined, when it ended before the
     kernel was being watched, or when the kernel has been seen past its end.
     Calls logged before this existed have no stored command and are not merged.

**Routine commands of the agent itself** (backlog `00b522e`, to do). Claude
Code runs commands on its own that are in no tool call: in one session 296
`git` runs (the same hardened `git -c core.askPass= ... status` form) and 319
helper copies of the `claude` binary. The log has none of them, only the text
of about ten status snapshots. They are evidence of what the harness does
unasked, so nothing is filtered: all of it is stored. The views group
identical commands from the agent's own process into one row with a count and
time range, per subcommand, so a new one (`git fetch`, `git config`) is a new
row the first time it runs. Their file and network events stay attached to the
process. The signatures that may be grouped are a short list in the repo that
contributors can extend; a command that matches none is shown as an action. A
kernel exec with no log match that is not grouped becomes an `evidence:
kernel` action.

**Built-in tools.** Edit, Write and Read run inside the agent process, so the
kernel reports only "this process created or opened this path", not which tool
or prompt. The merge pairs the log's tool call with the kernel event by path
and time (about 2 seconds). A kernel write with no log call, or a log edit
with no kernel event, is a mismatch to flag (a gap or an agent started before
the policy explains the second). Not yet checked against a live Edit; whether
a write is distinguishable from a read in `security_file_open` is open.

**Log-only counts.** Without the kernel collector the totals are what the
agent reported, a lower bound: no routine commands, no files a command
touched, nothing the model did not route through a tool. Pages that show totals
say so.

**File effects of a command** (backlog `65d950c`). File unlink, write, rename
and chmod events carry the exec that caused them, so `query` can return "this
command deleted 4 files, wrote 2". `/activity` and the prompt trace get an
expand control on a command row. No new capture is needed beyond the policy.

**Daemon wiring.** `internal/daemon/daemon.go` starts the collector beside the
ingester and the API server. Tetragon is installed and run on its own by its
standard method, following Tetragon's own install docs; sentient does not
install or configure it. What sentient relies on from that install: the gRPC
socket (`unix:///var/run/tetragon/tetragon.sock` in a standard install, which
`sentientd --tetragon` reads), and, optionally, the JSON export turned off
(`export-filename` empty in `/etc/tetragon/tetragon.conf.d/`) to save disk. The
policy is generated, not static (the agent's install path
differs per user), and `sen policy` prints it. It writes nothing and needs no
privileges; the user installs it, e.g. `sen policy | sudo tee
/etc/tetragon/tetragon.tp.d/sentient.yaml`, then restarts Tetragon (it does not
reload a changed policy; seen 2026-10-22). `sentientd` never writes the policy
and only reads the stream over gRPC. Packages (deb, rpm, Homebrew) can drop the
file in place later. Tetragon's JSON export file is off: it wrote about 4.3 KB per event
(5.6 MB in 71 s on a light session) and the collector doesn't need it.

**When the policy changes.** Short version: change the policy, restart
Tetragon, then restart the agents. `sentientd` is never restarted.

| What happens | Effect |
|---|---|
| You install or edit the policy file | Nothing, until Tetragon restarts. Tetragon does not reload it (seen 2026-10-22). |
| Tetragon restarts | The new policy is live. `sentientd` sees the stream end, records a monitor gap for the restart (a second or two), and reconnects by itself. |
| An agent was already running when the policy loaded | Its own file and network events are captured, and every command it runs is still recorded as an exec (Tetragon's base events cover all processes). The files and connections those commands touch are not: only processes started after the policy loads get `followChildren`. Restart the agent and it is complete. |
| An agent starts after the policy loaded | Fully captured, children included (checked 2026-10-24 with a fresh `claude -p`: `mkdir`, `mv`, `chmod`, `rm`, `rmdir` and a shell redirect all produced file events). |
| An agent auto-updates | The policy lists the old binary path and matches nothing, silently. Rerun `sen policy`, install it, restart Tetragon, restart the agent. |
| `sentientd` is down | Nothing is stored for that time. Tetragon keeps running, and nothing marks the stretch before the first connection as a gap (to do). |

The rule that follows: load the policy first, then start the agents. Adding
running agents to the policy at runtime would mean writing the file or using
`tetra`, which sentient does not do. Restarting the user's agent is not
sentient's job either. So sentient detects the state and recommends (to do):

- **Detection.** The policy loads when Tetragon starts, so Tetragon's process
  start time is the load time. An agent root process (stored in `processes`
  with its `started_at`) that started before that has incomplete capture.
- **Where it shows.** The Health page and `sen policy --check`: "claude (pid
  118708) started before Tetragon loaded the policy. The files and connections
  its commands touched aren't recorded; restart it for full capture." The
  commands themselves are still recorded.
- **Clears itself** once that agent restarts. The user decides whether to.

**Testing.** Tetragon can't run inside `go test`, so the logic is tested with
recorded event fixtures (normalization, session attribution, evidence
merging, gap detection, hash chain). A manual end-to-end check on the Linux
box (68 to 80 MB RSS with the policies loaded, 120 MB at policy load; see
the Volume question) comes at each build step below. Coverage stays at 70%
or more per package.

## macOS: `internal/collector/eslogger/`

No Apple entitlement is needed for `eslogger` (macOS 13+), so this ships
before the native Endpoint Security extension; the extension later replaces
it behind the same interface (`internal/collector/es/`).

- **Capture.** `sentientd` runs `eslogger exec fork exit create write
  close unlink rename setmode signal open` as a root subprocess (launchd
  daemon, Full Disk Access granted to it) and reads JSON lines. It
  restarts the subprocess if it dies and records the downtime as a gap.
- **Normalization.** Same OS-neutral records as the Tetragon path, so
  `processes`, `kernel_events` and the attribution steps are shared.
  Parser fixtures come from real captures, not yet made (the rly file
  `20261016-100826-eslogger.txt` is only the man page). Needs `events.txt` and
  a short live capture from the Mac, which requires Full Disk Access for the
  terminal app. Compiles and tests on any OS.
- **Agent filtering is in user space.** eslogger selects by event type only,
  not by process tree, so the collector tracks the tree itself: start on an
  `exec` whose binary matches an agent, follow `fork`/`exec` descendants,
  drop everything else before it reaches the store. `open` is high volume and
  is dropped for system library paths as in the Linux policy.
- **Gaps.** ES messages carry sequence numbers (to confirm in the JSON); a
  jump is a drop, which is a better signal than Tetragon's loss counters.
  Subprocess exit or a permission failure is also a `monitor gap`.
- **Network.** `eslogger` has no outbound-connect event, so `network
  connect` on macOS is coarse until the native extension plus a Network
  Extension: poll `nettop -P -L 1` and attribute by pid. Documented
  limitation, shown as such on Health.
- **Agent binary.** The desktop app bundles Claude Code under
  `~/Library/Application Support/Claude/claude-code/<version>/` and the
  user has no `claude` on PATH (backlog `a5fc746`), so matching is by a list
  of path patterns, not one name.
- **Root.** The Mac daemon runs as the user today (dev mode, data in
  `~/Library/Application Support/sentient`). With `eslogger` it needs root:
  a launchd daemon with `DefaultHomes` already covering `/Users/*`.

## Concurrent sessions: where attribution is ambiguous

Attribution is exact when each agent has one session at a time. It breaks
when **several sessions run at the same time**. Several different agents, each
with one session, are fine: separate process trees, different binaries. An
agent that launches another nests the inner one under the outer, and the event
belongs to the innermost.

| # | Situation | What goes wrong | Remediation |
|---|---|---|---|
| 1 | One agent, one session | Nothing. | None needed. |
| 2 | Several agents at once, one session each | Nothing; the trees never mix. | None needed. |
| 3 | Sessions one after another in the same directory | Nothing; start and end times separate them. | Time window. |
| 4 | **Two sessions of the same agent start in the same directory within a few seconds** (two terminals in one repo, a script launching several). In different directories (or git worktrees) the `cwd` separates them and this row does not apply. | The root process matches both sessions on `cwd` and start time; events go to the wrong one. | 1. Session id from the process environment. 2. Match the command text to the log. 3. Else low-confidence flag. |
| 5 | **One process hosts several live sessions** (a desktop app or IDE with several agent panes) | There is no root process per session. | Same as 4; the environment id or command match is the only key. |
| 6 | **One process changes session** (`/clear`, `/resume`) | Process to session is not fixed; later events go to the old session. | Pick the session by event time; the environment id, if it follows the change (unchecked). |
| 7 | A command runs at the boundary of two prompts | Log timestamps can lag the kernel's; it lands in the wrong prompt. | Match the command to the log's tool call, which names its prompt. |
| 8 | A background process outlives its prompt (dev server) | Its later events fall in the next prompt's window. | A process takes the prompt in effect when it was exec'd; its events inherit that. |
| 9 | A kernel-only action (never logged) on the boundary of an ambiguous session | Nothing to match against; either prompt could be right. | Attribute to the likelier one and flag low-confidence. Rare. |

**The signals, strongest first:**

1. **Session id in the environment.** Commands run by Claude Code's Bash tool
   carry `CLAUDE_CODE_SESSION_ID` (the same id as the transcript file name) and
   `CLAUDE_PID`; seen in this development session. `eslogger` exec events
   include the environment. Tetragon does not, so on Linux read
   `/proc/<pid>/environ` right after the exec (a very short command may exit
   first). Measured 2026-10-22 on the dev box (Tetragon 1.7.1, `tetra
   getevents` over gRPC, then a read of `/proc/<pid>/environ`): two concurrent
   `claude -p` sessions, in the same directory and in different directories,
   each got their own id on their shell and on `sleep`; the `claude` root
   processes carry no id. Reading an instant command is unreliable: of 300
   `/bin/true` execs only 66 (22%) could be read before the process was gone.
   So read the id on the Bash tool's shell, which lives as long as its
   command, and let the commands it spawns inherit it by ancestry. Not yet
   checked: whether the id follows `/clear` inside one process, and what other
   agents set.
2. **Command text.** A logged Bash call and a kernel exec with the same argv,
   close in time, are one action. It pins the process to the log's session, and
   everything the command spawns inherits it by ancestry. File edits pair the
   same way through the path.
3. **Time window.** The session whose log shows activity at that moment.
4. **Paths.** An unlogged write or delete in a path that only one candidate
   session has touched goes to that session, marked inferred.
5. **Low-confidence flag.** Never a silent guess. If two sessions both touched
   the path and neither logged the action, the row shows both candidates.

**Impact of the ambiguous cases (rows 4 to 6).** Logged actions are not
affected: each session's own log already places its commands and edits, and
the kernel match only adds the `both` tag. The agent is always right (it comes
from the binary). Only unlogged kernel events lose their session or prompt, and
they show a candidate list or an inferred session instead of one exact one.
Two sessions editing the same file are the weakest case; if both logged their
edits, the nearest logged edit by path and time still settles it.

## Build order

1. Migration 5, store writes, OS-neutral record types and the `Collector`
   interface, with `normalize` fixtures (Tetragon and eslogger).
2. **macOS first:** `eslogger` parser and subprocess runner into
   `kernel_events` and `processes`, hash chain verified by the existing
   integrity check. The Mac already runs the log-only version, so this is
   the first place a kernel row shows up end to end.
3. Session and prompt attribution (shared).
4. Merge with log actions (`evidence: both` / `kernel`) (shared).
   Then the attribution system tests (backlog `1868b6a`): scripted
   multi-session scenarios run for real, with ground truth for what each
   session did, scored for attribution and gap detection.
5. Linux: Tetragon `client.go`, gap monitor, Health card, `/proc` scan and
   the `matchPIDs` policy.
6. UI: evidence tags back on the trace, file effects on command rows.

## Why X, not Y

- **Tetragon, not our own eBPF programs.** Decided in design.md and
  reaffirmed 2026-10-22: we need about a fifth of Tetragon, but writing and
  maintaining kprobes, uprobes and the process tree is a lot more than a gRPC
  client. The cost of the extra service was measured (under 1 s of CPU added
  and about 70 to 80 MB of memory on a light session; see the Volume question)
  and is acceptable for an opt-in mode.
- **gRPC, not the JSON export.** An event with ancestry was about 10 KB as
  JSON; gRPC with ancestors off and our own tree from `exec_id` is far
  smaller.
- **Our own process tree, not Tetragon's ancestors.** Exec events are few and
  the tree is cheap to keep; per-event ancestors are most of the size.

## Open questions

- **Session to process mapping.** Matching on `cwd` + start time works for one
  session per directory. Concurrent sessions need the signals in "Concurrent
  sessions" above; check that `CLAUDE_CODE_SESSION_ID` survives `/clear`.
- **`cilium/tetragon/api` module size** against a 2 GB box's build memory;
  measure the first build with `GOFLAGS=-p=1 GOMAXPROCS=1`.
- **Volume.** design.md estimates about 180 MB per hour of agent activity;
  `kernel_events` needs a retention or compression plan (backlog `6efed70`).
  Measured 2026-10-22 (Tetragon 1.7.1, the four evaluation policies, one
  19-second haiku session, about 11 events/s): under 1 s of CPU added, RSS
  68 to 76 MB (78 to 120 MB while loading the policies at startup), 7.7 s of
  CPU at startup. Disk: the JSON export wrote 5.6 MB in 71 s (about 4.3 KB per
  event); the collector reads gRPC and runs with the export off. Not measured:
  a heavier session (`run1` was about 220 events/s), disk writes with the
  export off, steady-state reads.
- **Policy pinned to a binary path breaks on agent updates.** Seen 2026-10-22:
  the evaluation policies matched `.../claude/versions/2.1.302`; after the
  auto-update to 2.1.305 they matched nothing, with no error, and a busy session
  produced 5 events instead of hundreds. Editing the files did not reload
  them; Tetragon needed a restart. `Prefix` with `followChildren` is rejected,
  so the policy lists exact paths and is regenerated after an update (see
  "When the policy changes"). `procscan.go` should report Health "agent running,
  no kernel events" when an agent process exists but nothing matched.
- **npm installs** of Claude Code run as `node`, so the in-kernel binary
  filter doesn't fit; filter by ancestry in user space (deferred).

- **eslogger event coverage.** Confirm from a fresh capture that `fork`,
  `close` (modified), `setmode` and `signal` (not `kill`) come through as the build list
  assumes, and that the sequence number is in the JSON.
- **Full Disk Access for a launchd daemon** is granted per binary; check what
  survives an update of `sentientd`.

## Related and deferred

- `docs/rules.md`: the alert rules that consume kernel evidence.
- Backlog: file effects of a command (`65d950c`), adapter interface for Codex
  (`2aa3ed9`), compressing raw records (`6efed70`), database backup
  (`477932f`).
- Deferred: DNS answers from systemd-resolved for static binaries, the
  macOS collector, a watchlist of sensitive files snapshotted at session start.
