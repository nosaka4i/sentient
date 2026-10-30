# sentient design

**Status: proposed, not yet built.**

## Context

People who code with agents delegate heavily and stop checking each action.
The actions scroll past in the chat, and many are unreadable: a 400-character
`sed -i` or an inline Python script hides what it changed. In one user's
Claude Code history, ~678 of 5,394 shell commands appear to modify files.
Approving each command up front doesn't scale either: during this design
session the user approved half a dozen subagent bash commands with "no idea
what I'm approving". See [brief.md](brief.md).

The agent's own logs are not enough evidence. They are written by the same
process being checked, so they can be incomplete or altered, by accident or
on purpose. sentient therefore records what happened at the kernel level,
independently of the agent, and uses the agent's logs only for the story:
which prompt, which stated reason.

Decisions already made (2026-10-16/17):

- Coding agents only; Claude Code first. Built for the individual developer
  (a manager/team view is punted).
- **Linux first** (the user's own agent runs on Linux; eBPF needs no vendor
  approval); macOS second.
- Kernel capture is in v0.1. On Linux it comes from **Tetragon**, long
  term — we don't write eBPF programs ourselves ([tetragon.md](tetragon.md)).
- **Go** for the backend and the CLI (cobra). The CLI is `sen`.
- UI principle: extremely simple at the top, and the user can always go
  deeper, down to the raw record.
- Mockups before code: [mocks/cli/](mocks/cli/) is the spec for the CLI
  ([sen](mocks/cli/sen.md), [status](mocks/cli/status.md),
  [session](mocks/cli/session.md), [activity](mocks/cli/activity.md)).

## The mechanism

### Components

```
 platform collector (thin)            shared Go core (monitor, root)
 ┌─────────────────────────────┐     ┌──────────────────────────────────┐
 │ Linux: Tetragon (gRPC)      │     │ ingester   ── agent logs         │
 │ macOS (later): Swift ES/NE  │ ──► │ correlator ── trees, prompts     │──► store (SQLite, root-only)
 │   system extension          │     │ rules      ── alerts             │
 └─────────────────────────────┘     │ local API  ── gRPC, Unix socket  │◄── sen (CLI), sen serve (web UI)
        normalized events            └──────────────────────────────────┘
```

- **Collector** — platform-specific, thin: turns kernel events into one
  normalized event format. That format is the contract between collectors
  and the core.
- **Monitor** — the shared Go core: one long-running process, root, started
  by systemd (launchd on macOS). Runs the ingester, the correlator and the
  rules engine, owns the store (the only process that opens it), and serves
  the local API. On Linux it reads Tetragon's event stream directly.
- **`sen`** — the CLI and the web UI server (`sen serve`). Runs as the
  user, talks to `sentientd` through the local API, never touches the
  user's projects.

Two binaries from one Go module: **`sentientd`** (the monitor; root,
started by systemd, no network port) and **`sen`** (user). Both import the
same `internal/` packages; the root daemon carries none of the CLI or web
code, and the CLI none of the capture code.

### Local API

`sentientd` owns the database; everything else asks it. A small **gRPC**
API over a **Unix domain socket** (`/run/sentient/sentientd.sock`), not a
network port — the same pattern as Docker's `docker.sock`. gRPC because
both ends are our Go code (typed calls from shared protobuf definitions),
it streams (live events to the web UI), and `sentientd` already uses gRPC
for Tetragon.

- Only local processes can reach it; socket file permissions decide which
  users (the owner of the monitored agent account, e.g. via a `sentient`
  group).
- Narrow by design: the reads the CLI and web UI need (status, sessions,
  activity, alert detail) and a few writes (mark an alert reviewed). No
  general query endpoint.
- Testable by hand with `grpcurl -unix /run/sentient/sentientd.sock …`.
- If `sentientd` is down, `sen` says so — itself a fact worth knowing.

### Web UI

The goal is a rich web UI; the CLI comes first and shares its foundation.

- **`sen serve`** runs as the user, gets its data from the local API, and
  serves the web UI plus a JSON API on `127.0.0.1`. Never served by `sentientd`: a root
  process parsing browser input is the most exposed thing we could build.
  For an always-on dashboard, run it as a systemd user service.
- **One query layer** (`internal/query`) backs both the CLI views and the
  API, so they never disagree.
- **Self-contained:** the frontend (built in `web/`, framework TBD) is
  compiled and embedded with `go:embed`; no Node at runtime.
- **Live:** new events are pushed to the browser (SSE or WebSocket).
- **Remote access** via SSH tunnel or Tailscale, including phones.
- **Look:** dashboard mockup `.mocks/dashboard-21.html` (empty states:
  `dashboard-no-review-03/04`) is the current baseline — dark-first with light mode, single-hue palette built
  on nosaka's cyan (`#04d9ff`; `#007e96` in light mode): severity by
  lightness/fill (high solid cyan, medium cyan outline/tint, low grey).
  No amber; no timeline.
- **Server-rendered pages** (`html/template`, no framework): dashboard,
  health, prompt, session, sessions (one row per session; filters and sort
  live in the URL, `.mocks/sessions-01.html`) and activity (every
  resource/action count of a window as tiles, plus the events of the
  selected tile, `.mocks/activity-01.html`; the dashboard tiles link to it).
  The header links Dashboard, Sessions, Prompts, Agents and Health. **Agents**
  (`/agents`, `/agent/<id>`; mocks `agents-12`, `agent-06`, `agent-07`) lists
  the agent processes and, per agent, the background commands it runs on its own
  (git polling, helper runs of its own binary): grouped per command, or as a
  timeline filtered by text and time window. They are not part of any prompt
  (see `tetragon-integration.md`); a prompt links to the agent's timeline for its
  window. **Pages do not reload themselves**: a reload folds away what the reader
  opened. (Backlog `fd3e854`: an optional "new activity" notice.) The prompt
  page's trace and files-changed list are collapsed drawers. Responses are gzipped and
  static files revalidate by ETag. **The prompts page is the exception:** it is a
  shell plus `static/prompts.js`. The server returns a *window* of prompts as
  compact JSON (`/prompts/data`: the last N days, default 3, widened to the
  newest 100 if quiet; or one session; always at most 5,000, older pages via
  `before`), and the browser searches, filters, sorts and pages on that data.
  It is one flat list, newest first, with a Session column linking to each
  session's page (a per-session view is the session page, or `?session=`);
  an earlier grouped-by-session layout was dropped because looking across
  sessions for patterns is the point of this page. Spec:
  `.mocks/prompts-08.html` (the grouped layout; the built page is the flat
  list, as in `prompts-11.html`). Per-prompt duration and
  action count are stored columns (`prompts.last_activity_at`,
  `action_count`) so a window is cheap to build. Search beyond the loaded
  window needs the server (backlog: FTS5). Script tests: `make jstest`.

### Linux collector: Tetragon

Tetragon (Cilium/CNCF, Apache-2.0) runs as its own root service. Evaluated
on the dev box in [tetragon.md](tetragon.md): in a planted Claude Code
session it caught every planted action, attributed to the claude process
tree, with 0 lost events; ~48 MB RSS and <1% CPU idle.

- **Installed and run the canonical way.** Tetragon is installed and
  started by its own standard method (its release install / systemd
  service), not launched by `sentientd`. `sentientd` is a gRPC client: it
  loads sentient's TracingPolicy, documents the minimum tested Tetragon
  version, and treats Tetragon being unreachable as a `monitor gap`.
- **Policy.** A sentient-owned TracingPolicy, adapted from the evaluated
  one: `process_exec`/`process_exit` (always on), kprobes for file open,
  create, write, unlink, rename, chmod/chown, mkdir/rmdir; `tcp_connect`/
  `tcp_close` plus send/receive byte counts; kill, ptrace, setuid; a libc
  `getaddrinfo` uprobe, which gives hostnames for most programs. Where it
  doesn't (static binaries, Go's resolver), v0.1 shows the IP address;
  fuller hostname resolution (DNS answers from systemd-resolved) is
  backlogged. Every hook selector uses
  `matchBinaries` + `followChildren` on the agent binaries, so filtering to
  agent trees happens in the kernel.
- **Transport.** The monitor reads events over Tetragon's gRPC API, not the
  JSON export (10 KB/event with ancestry was too big).
- **Noise.** 77% of raw events were shared-library and `/proc` opens (the
  loader pulling in what `ldd` lists); path selectors drop them in-kernel,
  but only for system library directories — a library loaded from `/tmp`,
  the project, or via `LD_PRELOAD` is a hijack signal and is kept. Claude Code's own housekeeping
  (`~/.claude`, lock files, temp files) is kept but classified as agent
  internals, folded in every view.
- **Must start first.** Tetragon only follows an agent exec'd after its
  policy loads, so it starts at boot, before any agent. On startup the
  monitor scans `/proc` for agent processes already running and records
  them as "partially observed" (a gap), never silently.
- **Gap detection.** Tetragon has no per-event sequence numbers. The
  monitor polls its loss counters (Prometheus) and checks it is running; any
  loss, or Tetragon down, is a `monitor gap` — a high-severity alert.
- **Matching the agent.** Policies match the agent binary by path. v0.1
  supports the native Claude Code install only. npm installs run as
  `node /…/@anthropic-ai/claude-code/cli.js`: identifiable from the
  arguments, but Tetragon's in-kernel filter keys on the binary (`node`),
  so it would need user-space filtering — deferred.
- **Normalization quirks.** Claude Code writes files atomically
  (tmp file + rename): a rename onto a path is a modification of that path.
  New-file paths may carry a ` (deleted)` suffix to strip; exit status 0 is
  omitted.
- **File contents.** Kernel events say a file was written, not how.
  sentient keeps its own copy of every version (in the shadow git repo, see
  Store) for any
  file the agent changes, in a repo or not (`/etc/hosts`, `~/.zshrc`) — git
  alone can't cover untracked files. The "after" copy is read when the
  write completes (rename/close). The "before" copy is the last stored
  version, else git HEAD, else read when the agent opens the file for
  writing. That can miss: `echo x > file` empties the file at open, before
  we can read it. Then we still record the two facts that matter — the old
  contents were replaced, and what the file contains now — with the old
  contents marked unknown. sentient tracks changes; recovering or
  restoring old contents is out of scope (that's a backup tool's job) —
  "before" copies are kept only when cheap, to make diffs readable. An
  existing file emptied and rewritten (old contents unknown) is itself an
  alert, showing the command and the new contents — the destruction can't
  be hidden, only not recovered. Later: snapshot a watchlist of sensitive
  files at session start.
  Stored: text files up to 200 KB (in this user's history, files Claude
  edited averaged 7.9 KB, 99% under 63 KB, max 99 KB). Larger or binary
  files: the change, sizes and hashes are recorded, no contents. Git isn't
  used as the store: it only has committed versions, the agent can rewrite
  it, and writing into the user's repo would break read-only.

### macOS collector (later)

A small Swift system extension using Endpoint Security (processes, files)
and a Network Extension content filter (connections, bytes), streaming
normalized events to the Go monitor over a root-only local socket. Endpoint
Security has no TCP/IP events, hence the Network Extension. Needs Apple's
Endpoint Security entitlement (requested 2026-10-18; forum reports of
long waits). Until then `eslogger` + `nettop` are the Mac source behind
the same interface, and they ship at launch.

### Ingester (agent logs)

Claude Code adapter, per [brief.md](brief.md#data-sources):

- Tails `~/.claude/projects/*/<session>.jsonl`, `<session>/subagents/*.jsonl`
  and `<session>/tool-results/`.
- Copies every raw record into the store as it arrives. Transcripts are
  deleted after ~30 days; sentient's copy is the one that lasts.
- Extracts sessions, prompts (user messages, excluding tool results and `!`
  commands, which are attributed to the user), tool calls and their
  results, subagent starts, the agent's stated text, and `cwd`.
- Unknown record types fail loudly (logged and counted), never skipped
  silently. Tested against fixture files captured from real sessions.

### Correlator and rules

- **Agent vs user.** Anything in an agent's process tree is the agent's.
  The user's own terminal, editor etc. are not recorded. `!` commands typed
  into Claude Code are under the agent's tree but attributed to the user
  via the log.
- **Prompt attribution.** Each kernel event is assigned to the prompt that
  was running in that agent's session at that time.
- **Subagents.** Claude Code subagents run inside the same `claude`
  process, so the kernel can't tell them apart; the split comes from the
  log only.
- **Actions.** Kernel events and log tool calls become normalized actions:
  `resource` + `action` + `path`, using the vocabulary in
  [status.md](mocks/cli/status.md). Each action records its evidence
  source (`kernel`, `log`, or both) and links to its raw records.
- **Counting commands.** A command line counts as one `command execute`
  per simple command, split with a shell parser (`mvdan.cc/sh`):
  `cd x && ls | sort` is 3, across `&&`, `||`, `;` and `|`. `bash -c` /
  `sh -c` strings are parsed recursively. This counts what the agent asked
  for, so chaining doesn't dodge the per-prompt action count (or hide a
  `curl … | sh` mid-chain). The kernel confirms which ones ran: builtins
  like `cd` never exec, and commands skipped by a failed `&&` are marked
  skipped. Processes a command spawns (`npm test`'s children) don't
  count; they're drill-down detail. Scripts count as one command, with
  their child processes visible underneath. A prompt's ACTIONS count
  (dashboard and `sen status`) counts actions on the system only: file,
  dir, command, process, network, git, web fetch, launch item. Things
  inside the agent are not actions: the prompt itself, subagent start,
  and server-side web search.
- **Claim vs record.** A kernel-observed write with no matching tool call
  or command in the log is the key mismatch ("not in agent's log").
- **Repo root** is resolved by us from the path (nearest `.git`), not taken
  from the log's `gitBranch`/`cwd`.
- **Rules** are deterministic and produce alerts with a severity
  (high/medium/low, as in [status.md](mocks/cli/status.md)). No model
  judges severity. The rule list and each rule's wording are in
  [rules.md](rules.md). Bulk writes under `node_modules/`, `.venv/` etc. are
  collapsed, not dropped. Later: leftover processes (agent-started
  processes still running after the session), user-defined rules.

### Install (v0.1)

Manual steps, documented in the README: install Tetragon the standard way
and enable its service; install the `sentientd` systemd unit; put `sen` on
the PATH. A package or `sen install` can come later.

### Store

SQLite via the pure-Go driver `modernc.org/sqlite` (no cgo, no C
toolchain), in WAL mode. Everything lives under **`/opt/sentient/`**
(self-contained): `bin/` (binaries), `data/sentient.db`, `shadow/` (file
versions, below). Owned by root and readable only by root; `sentientd` is the only process that opens it, and all access goes
through the local API. Records are hash-chained (each includes
the previous record's hash) so later edits are detectable.

Schema (draft; only what `sen status` needs is in scope now):

```sql
sessions       (id PK, agent, agent_session_id, repo_root, cwd, started_at, ended_at)
prompts        (id PK, session_id FK, seq, text, started_at, ended_at)
processes      (exec_id PK, pid, parent_exec_id, binary, argv, cwd, uid,
                started_at, exited_at, exit_code)   -- index of exec/exit events
process_attribution (exec_id PK, session_id FK, prompt_id FK, attribution,
                root_exec_id,            -- the agent process it descends from
                background, background_rules)  -- group key of a background command, rules version; derived links
-- kernel_events and raw_log_records reject UPDATE/DELETE (triggers); see storage-design.md
kernel_events  (id PK, exec_id FK, ts, type, payload JSON, prev_hash, hash)
raw_log_records(id PK, session_id FK, ts, source_file, record JSON, prev_hash, hash)

actions        (id PK, session_id FK, prompt_id FK, exec_id FK, ts,
                resource, action, path, summary, evidence)  -- kernel | log | both
file_changes   (action_id FK, path, commit,                 -- commit in the shadow repo
                size_before, size_after, before_unknown, stored)

alerts         (id PK autoincrement, action_id FK, ts, severity, rule,
                status, reviewed_at)                        -- open | reviewed; id = CLI `#`
monitor_gaps   (id PK, started_at, ended_at, reason)
schema_version (version)
```

- `kernel_events` and `raw_log_records` are the evidence: verbatim,
  hash-chained (`prev_hash` → `hash`).
- `actions` are the normalized rows every view shows.
- **Retention.** Everything is kept forever except raw kernel events, the
  only heavy data (rough estimate ~180 MB per hour of agent activity if
  stored naively). They're stored compactly (binary, compressed) and pruned
  oldest-first by a size and age cap, like journald/auditd — both tunable
  in sentientd's config, with smart defaults: size cap = the smaller of
  1 GB or 5% of the filesystem (scales down on small disks, like
  journald), age cap = 7 days, whichever is hit first. Raw events behind an alert are kept forever. Measure real volume
  during the build and adjust the defaults.
- **File versions live in a shadow git repo** at `/opt/sentient/shadow/`,
  sentient's own (never the user's project), mirroring real paths
  (`shadow/etc/hosts`, `shadow/home/ubuntu/…/x`). Each agent change is one
  commit whose message points back to the action (session, prompt,
  command); deletes are `git rm`. Git gives diffs, per-file history,
  versioning ("the file as of 14:33" = that commit), delta compression,
  and hash-chained commits for free. Written with go-git (pure Go), never
  pushed. `file_changes.commit` links each change to its commit. Bulk
  writes (`node_modules/`, `.venv/`) and files over the cap aren't
  committed.

### CLI

Cobra; `sen` alone prints help. Output via `text/tabwriter`, aligned in a
terminal, raw tabs when piped. Dates `Oct 7 14:52`, local time.

Current focus — only these three:

| Command | |
|---|---|
| `sen` | help — [mock](mocks/cli/sen.md) |
| `sen status` | needs attention, last 24h, prompts — [mock](mocks/cli/status.md) |
| `sen version` | version, set at build time |

Later (not being reviewed or built yet): `sen session [id]`
([draft mock](mocks/cli/session.md)), `sen activity`
([draft mock](mocks/cli/activity.md)), `sen alert <#>`, `sen serve` (web UI
+ JSON API on 127.0.0.1).

### Repo layout

```
cmd/sen/            the CLI + web server (cobra), runs as the user
cmd/sentientd/      the monitor daemon, runs as root under systemd
internal/store/     schema, migrations
internal/query/     read queries shared by CLI views and the web API
internal/collector/ normalized event types; tetragon/ (supervisor + gRPC client)
internal/ingest/    agent log adapters (claudecode/)
internal/correlate/ process trees, prompt attribution, actions
internal/rules/     alerts and severity
internal/render/    CLI tables
internal/web/       HTTP API, embedded frontend assets
web/                frontend source (later)
macos/              Swift ES/NE collector (later)
deploy/             systemd unit, Tetragon TracingPolicy
testdata/           fixtures: Tetragon captures, Claude Code sessions
```

## Build order

Everything in steps 1–3 can be built and tested on the Linux dev box.

1. **Plumbing.** Module, cobra tree, `sen version`; SQLite schema and
   migrations; the Claude Code ingester; `sen status` rendering LAST 24H from
   log-derived actions (evidence `log`). The NEEDS ATTENTION and PROMPTS
   sections show dummy data at first (matching the mock), each clearly
   labeled as sample data in the output, and are replaced with real data as
   correlation and rules land. The footer hints for `sen alert` /
   `sen session` stay.
2. **Web UI first (`sen serve`).** The UI is the main product; `sen` is for
   CLI users and doesn't need full parity. Build the mocked pages
   (`.mocks/`: dashboard, prompt, alert, alerts, prompts, activity,
   session) on log-derived data, with PROMPTS real. Kernel capture then
   deepens the same pages.
3. **Kernel capture.** Tetragon policy and gRPC client, process trees,
   correlation, `/proc` scan at startup, gap detection. Tested with planted
   sessions whose captures become fixtures. Includes re-testing the
   ptrace/kill hooks under the agent tree (they emitted nothing when
   filtered by binary in one evaluation run).
4. **Rules and alerts.** NEEDS ATTENTION (web and `sen status`), review
   state, `sen alert`.
5. **macOS collector via `eslogger`** (+ `nettop` for network): required
   for launch, since most agent users are on Macs. No Apple approval
   needed (root + Full Disk Access). Launch = Linux (Tetragon) + Mac
   (eslogger).
6. Remaining CLI commands as needed; the native Endpoint Security system
   extension once Apple grants the entitlement (requested 2026-10-18).

## Why kernel capture, not the agent's logs alone

The logs come from the process being checked. A model, by accident or by
design, can leave things out or misreport them; a shell script can change
files the log never names. Kernel events come from the OS, which the agent
(running as the user) cannot edit. The logs remain essential for intent —
which prompt, which reason — and the gaps between the two are the most
useful alerts.

## Why Tetragon, not our own eBPF programs

eBPF programs are written in restricted C (or Rust); Go can only load them.
Writing our own would mean maintaining kernel-side C for process tracking,
path resolution, long argv capture and hostname hooks — exactly what
Tetragon has already solved and tested across kernels. We use Tetragon's
core (process tracking, a dozen hooks, in-kernel filtering, export); the
part we don't use is mostly its Kubernetes integration, dormant when run
standalone. Its footprint (~48 MB, <1% CPU idle) is acceptable. The cost is
a second root service to run and upgrade, and dependence on a project whose
priority is Kubernetes security. Tracee was the alternative; its last
release was Nov 2025. AgentSight is agent-aware but young, noisy, and built
around recording one command rather than an always-on service.

Running *inside* Tetragon (as a plugin) was considered: Tetragon has no
plugin system, a fork would mean merging a large fast-moving project
forever, and it would tie the cross-platform core to Linux. Instead:
Tetragon runs as its own standard service, and small general improvements
are contributed upstream (per-event sequence numbers, the ptrace/kill
filter quirk).

## Why a shared Go core, not a native daemon per platform

Only the kernel collector is platform-specific (Tetragon/eBPF on Linux,
Endpoint Security + Network Extension on macOS). The ingester, correlation,
rules and store are identical everywhere; writing them per platform (e.g. a
Swift daemon on macOS) would mean separate codebases for Linux and Windows.

## Why a root daemon, not a user process

The agent runs as the user. A monitor running as the user could be killed,
and its store edited, by the agent. Running as root puts both out of the
agent's reach unless it gains root.

**Assumption: the agent has no root.** Best practice is to never give
agents sudo/root privileges; agents belong in a sandbox. If the agent's
account has passwordless sudo (as on the dev box this was designed on), the
root boundary protects nothing: the agent could kill the monitor or edit the
store. Mitigations: any `sudo` from an agent tree is a high-severity alert;
a stopped monitor or Tetragon shows as a `monitor gap`; stored records are
hash-chained and optionally copied off the machine as written, so later
edits are detectable. sentient also checks for the condition and warns
(e.g. a `sen status` header line: "agent's account has passwordless sudo").
Sandbox status and blocked attempts are reported when available.

## Why SQLite, not flat files

Every view is a query: by time, prompt, session, file, resource/action.
SQLite gives that with one file, no server, and transactional writes from
a single writer. Raw records are kept in it verbatim, so nothing is lost
by normalizing.

## Why deterministic rules, not a model

A model should not vouch for a model. Severity, summaries and diffs are
computed, so the same record always gives the same answer and every alert
can point at its evidence.

## Open questions


## Related and deferred

- [brief.md](brief.md), [use-cases.md](use-cases.md), [mocks/cli/](mocks/cli/),
  [tetragon.md](tetragon.md), [ideas/agent-linux.md](ideas/agent-linux.md)
  (the prevention half: a hardened Linux for agents),
  [capture-verbosity.md](capture-verbosity.md) (how much of the kernel stream is recorded),
  [storage-design.md](storage-design.md).
- Deferred: macOS collector (Swift system extension; Endpoint Security
  entitlement to apply for); user-defined rules (e.g. "agent must not
  commit"); web UI; other agents (Codex CLI, Gemini CLI); Windows;
  team/manager view; independent model-call/token accounting.
