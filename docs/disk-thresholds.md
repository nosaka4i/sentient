# Disk thresholds and the recording pause

**Status:** built: the pause (`internal/daemon/disk.go`, `internal/collector/gate.go`, `Paused` in ingest, attribution and merge), the site-wide banner and Health state (`internal/web/alert.go`), and the "not compared" mark with one band per pause (`internal/query/gaps.go`, `internal/web/prompt.go`). Not built: the heartbeat that records when sentientd itself was down (undecided), `sen status` on real data, and the same band on the Agents timeline. Mocks: `.mocks/health-04.html` (Health and the site-wide banner), `.mocks/prompt-23.html` (the band on the prompt page), `docs/mocks/cli/sentientd-verbosity.md` (flag and log lines). Backlog `bbee4b1`.

What sentientd does as the disk it writes to fills up: warn, then pause recording, then resume, and how the pages tell the difference between "nothing happened" and "nothing was recorded". The levels that decide how fast the disk fills are in [capture-verbosity.md](capture-verbosity.md); where the space goes is in [storage-design.md](storage-design.md).

## Context

On 2026-10-29 one day of repeated `go build` and `go test` runs wrote 741,698 kernel events, about 330 MB. The database was 720 MB on a 58 GB disk. Sentient is meant to run for months beside an agent, so the disk it shares with the agent is a real limit, and the agent is also what fills it: the agent's own transcripts, its build output and its caches live on the same disk. Reports of Claude Code failing at a full disk make the point from the other side. A write failure at ENOSPC has crashed it, and at 0 bytes free a failed write zeroed its settings file ([anthropics/claude-code issue 16720](https://claudeissues.com/issue/16720-bug-claude-code-crashes-with-unhandled-enospc-error-when-disk-space-is-low), [issue 24207](https://claudeissues.com/issue/24207-bug-no-disk-space-management-claude-grows-unbounded-cascade-failure-destroys-set); community reports, not official behavior). A monitor that fills the disk can take the monitored agent down with it.

## Thresholds

Two flags, named and behaving like kubelet's image garbage-collection thresholds (`--image-gc-high-threshold` and `--image-gc-low-threshold`), both a percent of the data directory's filesystem used:

- `sentientd --disk-high-threshold <percent>`: recording pauses at this share. Default 80; valid values are 10 to 99.
- `sentientd --disk-low-threshold <percent>`: recording resumes at this share. Default 5 below the high threshold (75); valid values are 1 up to one below the high threshold. Setting only the high one moves the low one with it.

The warning is not a flag: it is 10 points under the high threshold.

| used | what happens |
|---|---|
| below high minus 10 (70) | nothing |
| high minus 10 (70) and above | Health warns: "Disk filling up", with sentient's own database size in the message. The Health icon gets a dot. Recording continues. |
| high (80) | recording pauses; Health is critical; every page shows a banner |
| low (75) or below, after a pause | recording resumes |

The measure is the filesystem's used share (`statfs` on the data directory, checked every 15 seconds), not sentient's own size. The message names sentient's size because 70% or 80% of a big disk is often not sentient's doing.

## What the pause does

- **Kernel events** are dropped at the gate (`internal/collector/gate.go`), outermost, before the filter and the queue. Tetragon does not wait for us, so those events are gone. The gate keeps the stream connected and discards, so nothing builds up in Tetragon and no burst arrives on resume.
- **Agent transcripts** are not read (`claudecode.Config.Paused`). Ingest keeps its read position per file, so on resume it carries on from where it stopped and catches up on everything the agent logged meanwhile. The transcripts stay on disk in the meantime.
- **Attribution and merge** make no passes (`Attributor.Paused`, `Merger.Paused`), so the derived tables do not grow either.
- **The API, the pages and Health keep working.** Reads are not paused.
- **Nothing is deleted.** Freeing space is the user's, or `sudo sentientd prune` later (backlog `073733a`).
- **The pause is one gap** in `monitor_gaps` (collector `sentientd`, reason "disk 81% used: recording paused"), written when it starts and closed when it ends. That one small write is the only one allowed while paused.
- **Started above the threshold,** sentientd starts paused and says so. On a machine already over 80% for other reasons it records nothing until space is freed or `--disk-high-threshold` is raised; that is deliberate and loud, not silent.
- **A failed measurement keeps recording.** If `statfs` fails, the guard does not pause on a reading it does not have.

## Resuming

Recording resumes when used space falls to the low threshold or lower. Keeping it below the high one stops it flapping when usage hovers around the threshold. On resume the gap is closed, the log says how long the pause lasted, and ingest catches up on the transcripts. The kernel side stays empty for the paused span.

## How the pages treat the pause

The agent log is caught up and the kernel log is not, so without help every action in the paused span would show as "unverified" (logged, but the kernel never saw it), and "unverified" is supposed to mean something is wrong.

Any logged action whose time falls inside a gap is marked **not compared**: in the agent log, not confirmed and not unverified. The numbers still add up:

`AgentLog = Confirmed + Unverified + NotChecked + Pending + NotCompared`

Rules:
- The mark applies to **every** gap, not only the disk pause: a Tetragon outage or a restart has the same false-alarm problem. The gap's reason says why.
- An action that did get a kernel match stays **confirmed**, wherever it falls. Tetragon buffers a few thousand events (internal queue 10,000, ring-buffer channel 65,535 by default), so a few can survive a gap. Partial kernel data therefore gives more confirmed and fewer not-compared, never a contradiction.
- Gap edges are widened by 5 seconds (the merge's clock slack), so an action logged just after a gap ends is not called unverified because the clocks disagree a little. That can hide a real unverified action within 5 seconds of a gap; that is accepted.
- The kernel's detail is missing for those actions: process duration, exit status, child processes, files and connections touched. What the agent's log says is all there, and nothing is invented to fill it.
- The prompt page shows **one hatched band per pause**, not a tag on every row. The actions inside it are listed as usual; a long pause is collapsed with a summary by kind ("62 commands · 31 file reads · 7 edits"). The tiles show "N not compared". The Files changed drawer, the Agents timeline and the search (a match inside a collapsed band opens it) work as for any other rows.

## Where the user sees it

- **At the warning (70%):** the Health icon in the top bar gets a cyan dot; the Health page has a "Disk filling up" notice. No banner.
- **At the pause (80%):** a banner under the top bar on every page ("Recording is paused: the disk is 81% used. Nothing is recorded until it is below 75%." with a link to Health), a red dot on the Health icon, the headline "Recording paused" and a critical notice on Health, and the Kernel capture card's chip reads "recording paused". The banner is not dismissable while the pause lasts and goes away by itself.
- **In the log:** `recording paused: disk 81% used` at ERROR with the threshold, the resume point, the free space and a hint; `recording resumed` with how long it lasted.
- **In the CLI,** once `sen status` is wired to real data (backlog `a4e3e68`): one line above its output while paused.

## Why this way, not that

- **Pause, not delete.** The evidence tables are append-only and hash-chained, so nothing can free space by itself, and automatic deletion of evidence is the wrong default for this tool. Space comes back through a separate, recorded command (`prune`), as in [capture-verbosity.md](capture-verbosity.md).
- **Stop at a share of the disk, not at sentient's size.** The disk is shared with the agent, and a full disk takes the agent's own writes down, so the whole disk is what matters. A size cap on sentient alone would not protect it. The cost is that a machine already over the threshold records nothing; the flag and the loud start are the answer.
- **Catch up the agent log, do not skip it.** Skipping ahead would be the cleanest code, but it would leave the conversation and everything the agent did during the pause out of sentient for good, which is dishonest about what happened. The agent's own files can also be edited or deleted later, so a catch-up at least saves whatever survives. The price is the accounting mark above.
- **Mark the gap, do not hide the actions.** The catch-up actions stay in the agent log, in the timeline and in the totals, in their own hatched style, so they read as "nobody checked these", never as "checked and fine".
- **One band, not a tag per row.** A pause can cover a hundred actions. One band says it once.
- **A banner on every page.** A pause that shows only on Health would go unnoticed by someone reading Sessions.
- **A high and a low threshold, not one `--stop-at`.** Precedent: kubelet's `--image-gc-high-threshold` and `--image-gc-low-threshold` (collect above the high mark, until the low one), Elasticsearch's low, high and flood-stage disk watermarks (and its flood-stage block lifts once usage falls back), and auditd's `space_left` and `disk_full_action = suspend`, which stops writing audit records when the disk is full. None of them says "stop at". Two thresholds keep the state from flapping around one, and make the resume point something the user can set.

## Known limits

- **The agent can trigger the pause.** Anything that pushes the disk past the threshold blinds sentient for as long as it stays there, and what the agent does meanwhile is not recorded by the kernel. The gap, the banner and the critical notice make it visible after the fact. A degraded mode (keep exec, network and file-write events until a hard stop at 95% used, so filling the disk cannot cheaply hide commands) would close the hole; it is not built, and the choice for now is the simple full stop (backlog `bbee4b1`).
- **The guard looks every 15 seconds,** so sentient can pass the threshold by what it writes in that time. That is a few KB on a busy day, and a few hundred MB at most for the first import of a large transcript history (under 1% of a 40 GB disk).
- **SQLite needs working space** for a `VACUUM`, a backup or an integrity pass, comparable to the database. At 80% used that is normally there; it is one reason the threshold is not 95%.
- **Kernel events during a pause are unrecoverable,** except the few that Tetragon's queues or an optional export file still hold.
- **Space is not measured per user or quota.** The guard watches the filesystem sentient writes to, nothing else. It does not watch the agent's own disk: that would be monitoring the agent, which sentient does not do. When the two share a disk the guard protects the agent as a side effect; when they do not, sentient cannot fill the agent's disk.

## Open questions

- **How the banner gets its data on every page.** The plan is for the web server to poll Health in the background (about every 15 seconds) and cache it, so no page pays an extra call per request.
- **Where "how to free space" links** until `prune` and the knowledge base exist (README for now).
- **Whether 70/80/75 are the right defaults** for small disks and for disks that other things fill; they are two flags, and the warning is fixed at 10 points under the high one.
- **A runtime change of the thresholds** needs a restart, like the level.
- **An absolute free-space floor** (also pause below about 2 GB free). It does nothing on a normal disk (80% of 40 GB leaves 8 GB) and only matters on a very small one (80% of 5 GB leaves 1 GB). Low priority (backlog `7992377`).

## Related and deferred

- [capture-verbosity.md](capture-verbosity.md): the levels, the policy, and `prune`.
- [storage-design.md](storage-design.md): size containment, retention and the archive tier; the Tetragon export file as a catch-up source.
- [tetragon-integration.md](tetragon-integration.md): the two logs and the accounting the "not compared" mark extends.
- [mocks/cli/sentientd-verbosity.md](mocks/cli/sentientd-verbosity.md): the flag and the log lines.
- Backlog: `bbee4b1` (verbosity and the disk work), `073733a` (`sudo sentientd prune`), `cff5870` (capture-loss detection, which the gap-aware accounting also serves), `a4e3e68` (`sen status` on real data), `3ffc94f` (the Health collector card).
