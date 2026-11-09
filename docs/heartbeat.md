# Heartbeat: recording when sentientd was not running

**Status:** built: `internal/store/heartbeat.go` and migration 13 (the table), `internal/daemon/heartbeat.go` (the beat, the startup gap, the log line) wired in `internal/daemon/daemon.go`, `health.StoppedReason`, and the card's sentence in `internal/web/capturecard.go`. Backlog `38e8dec`. No mock: it adds no screen, only a new gap the pages already show, and one new sentence on the card's latest-gap line.

When sentientd is down, nothing watches the kernel and nothing writes a gap, so the downtime is invisible. This records it: a small "last seen" row that sentientd keeps current, and a gap written from it when sentientd starts again.

## Context

A gap is a stretch of time the kernel was not being recorded. Every page that compares the agent's log with the kernel's reads `monitor_gaps` to tell "nothing happened" from "nothing was recorded" ([disk-thresholds.md](disk-thresholds.md), "How the pages treat the pause"). Gaps are written by sentientd itself: the collector writes one when Tetragon is unreachable, the disk guard writes one for a pause, and a gap that was open when sentientd stopped is closed at the next start (`closeStaleGaps`, `internal/daemon/disk.go`).

Nothing covers sentientd being down. Tetragon keeps running and the agent keeps working, but no gap exists for that time, so:
- the agent's actions during the downtime are in the agent's log (ingest catches up on start) and have no kernel match, so they show as **unverified**, which is meant to mean something is wrong;
- the gaps tile on the Kernel capture card ([kernel-capture-card.md](kernel-capture-card.md), "Known limits") does not count the downtime.

Seen on 2026-11-06 while restarting sentientd to test the card.

## The mechanism

**A heartbeat table.** `heartbeat(collector TEXT PRIMARY KEY, seen_at TEXT, clean INTEGER)`, one row per collector, in a new migration in `internal/store/migrate.go`. It is monitoring state like `monitor_gaps`, not evidence, so it is updated in place and stays out of the hash chain; the evidence tables remain append-only.

**Kept current.**
- Every 30 seconds (`heartbeatEvery`, a constant next to `diskCheckEvery` in `internal/daemon/disk.go`), one small write. The guard's own 15 second tick is unchanged; the beat runs on its own interval, so the guard's checks can change without moving the beat.
- Exactly at a clean shutdown: sentientd writes the current time as it stops and marks the row clean, so after a clean stop the downtime is measured to the second. Every other beat marks the row not clean, so a row still not clean at the next start means the last run did not stop on purpose.
- After a crash or a kill the last write is up to 30 seconds old, so the gap starts up to 30 seconds early. It can be older if beats were skipped: the store has one database connection (`SetMaxOpenConns(1)`, `internal/store/store.go`) and SQLite allows one writer at a time for the whole file, so a long write (a big import) can hold a beat past its deadline. That over-covers: those actions are marked "not compared" when they could have been compared. It is the safe direction, and an action that did get a kernel match stays confirmed wherever it falls ([disk-thresholds.md](disk-thresholds.md), rules).
- A heartbeat write has its own short deadline and is dropped on failure with a debug log. It must not wait behind the startup writers (backlog `42c435d`) and it must not stop Health.

**At startup.** If a heartbeat row exists for the collector, sentientd writes one gap in `monitor_gaps` from `seen_at` to now: collector `sentientd`, reason `sentientd was stopped`. The reader sees it as "sentientd was stopped around 14:02": the time is the gap's `started_at`, shown in local time like every other time on the pages, and "around" because after a crash it can be up to 30 seconds early, or more if beats were skipped. The time is not baked into the stored reason, so a change of zone or of daylight saving time cannot make it wrong. The gaps tile and the "not compared" mark read `monitor_gaps` and pick the gap up unchanged; the card's latest-gap line builds the sentence for gaps whose collector is `sentientd`, a small change in `internal/web/capturecard.go`.

**Rules.**
- **Only while a kernel collector is enabled.** The heartbeat is written, and the gap is made, only when sentientd runs with a collector (`--tetragon` today). In agent logs only mode a "kernel not recorded" gap means nothing, and it would put "not compared" marks on pages that have no kernel check, against the rule that log-only mode shows no such marks ([kernel-capture-card.md](kernel-capture-card.md), "Agent logs only"). Because the row is per collector and only moves while that collector runs, switching between modes is right: run with Tetragon, then without, then with again, and the gap covers all the time the kernel was not watched.
- **No double counting.** If `closeStaleGaps` just closed a gap the last run left open (a disk pause, an unreachable Tetragon), that gap already covers the downtime, so no heartbeat gap is written.
- **A crash goes in the log, not the table.** If the row is not clean at startup, sentientd logs a warning that the last run did not shut down cleanly (a crash, a kill or a power cut) and points at the log before it. A Go panic's message and stack are already in sentientd's own log (stderr, so the journal under systemd); the table keeps no error text. The table is only used to work out the gap, and the gap's reason is the same either way: "sentientd was stopped".
- **First run.** No heartbeat row, so no gap.
- **A clock that went backwards.** A `seen_at` after now is ignored (no gap), with a log line.
- **While paused.** The heartbeat is still written. Without it, a stop during a disk pause would leave a stale `seen_at` and the downtime would show as two overlapping bands (the pause gap and the heartbeat gap). It is one tiny write every 30 seconds, in addition to the pause gap's own write ([disk-thresholds.md](disk-thresholds.md), "What the pause does").
- **Short restarts count.** A restart that takes 3 seconds is a 3 second gap. It is real, and the gap's 5 second widening ([disk-thresholds.md](disk-thresholds.md)) already absorbs the clock slack.

## Why this way, not that

- **A heartbeat row, not a gap held open for the whole run.** An open gap for as long as sentientd runs, closed on shutdown, would need `closeStaleGaps` to tell a real gap from the run's own, and every reader of `monitor_gaps` would have to ignore the live one. A separate table keeps `monitor_gaps` a list of real gaps.
- **A heartbeat row, not the last event's time or the log's last line.** An idle machine writes nothing for hours, which would read as hours of downtime.
- **Not systemd's own record** (`journalctl`, `systemctl show`). It only exists where systemd runs. sentientd also runs by hand and on macOS, and a record in the database travels with the data.
- **30 seconds, not 15 or 60.** The beat is one single-row write, so its cost is the wait for the one database connection, not the write: about 2,880 a day at 30 seconds (5,760 at 15, 1,440 at 60), against tens of thousands of event rows. The error after a crash is the beat interval, and it only over-covers (a few more actions marked "not compared"), small next to the minutes a restart usually takes. 15 seconds was the first guess only because the disk guard already ticks at that rate; it was not measured, and a faster beat is more writes for a smaller error. 60 seconds would halve the writes again for a gap that starts up to a minute early. It is one constant, `heartbeatEvery`, if real use says otherwise.
- **Per collector, not one for sentientd.** A macOS collector next to a Linux one on another machine, or a collector added later, has its own "last watched" time.
- **Only while a collector runs.** See Rules: log-only mode must not grow "not compared" marks.
- **No crash detail in the table, and one reason for every gap.** A copy of the panic text in the database would be a second record of what the log already has, and the `clean` flag is overwritten by the next run, so a "stopped unexpectedly" wording would have to be copied into the gap row to last. Nothing needs it: the pages only need to know the time was not recorded, and whoever wants the cause reads the log. `clean` is only there so the next start can say in the log that the last run ended badly.

## Tests

- store: write and read a heartbeat; an update moves it; per collector.
- daemon: a gap after a stale heartbeat; no gap on a first run; none without a collector; none when a stale open gap was just closed; none when `seen_at` is in the future; the exact time written at a clean shutdown and the row marked clean; a warning logged when the row is not clean at start, and none when it is; the beat keeps going while the disk is paused.
- web: the card's latest-gap line says "sentientd was stopped around <time>" for a `sentientd` gap; the other gap tests are unchanged.

## Open questions

- **A stale gap from another mode.** "No double counting" skips the downtime gap when the start closed any open gap. A pause that was open in agent logs only mode, followed by a start with a collector, would skip it even though the heartbeat is older than that pause started. Rare; the fix would be to skip only when a closed gap started at or before `seen_at`.
- **Gap reason wording (decided):** "sentientd was stopped around 14:02", the same for a clean stop and a crash. "Around" fits both: a clean stop is exact, a crash is up to 30 seconds early. The `clean` flag is for the startup log line only.
- **Heartbeat write cost on a slow disk.** One small write every 30 seconds is small. A beat that waits behind a big writer (backlog `42c435d`) is dropped at its own deadline, not retried, and the next one goes out on schedule.

## Related and deferred

- [disk-thresholds.md](disk-thresholds.md): the pause, the guard's 15 second tick, and how pages treat any gap.
- [kernel-capture-card.md](kernel-capture-card.md): the gaps tile and the "Known limits" this closes.
- [tetragon-integration.md](tetragon-integration.md): the "not compared" accounting.
- Backlog: `38e8dec` (this), `42c435d` (startup contention), `3ffc94f` (the card).
