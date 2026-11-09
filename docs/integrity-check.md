# The hash-chain integrity check

**Status:** built: `internal/store/write.go` and `internal/store/kernel.go` (the paced checks), `internal/daemon/health.go` (the task), the flags in `cmd/sentientd/commands.go`, and the Health tile and warning in `internal/web/health.go` and `internal/health/health.go`. Not built: an anchor outside the database (see "What it does not stop"). Backlog `8a98fe1` (the kernel chain), `42c435d` (startup contention).

What sentientd does to notice that a stored record was changed or removed, and how it keeps that work from getting in the way of everything else.

## What is checked

Two chains, both append-only tables whose rows each carry the hash of the row before (`prev_hash`) and their own (`hash`):

- `raw_log_records`: the agent's log, as the agent wrote it.
- `kernel_events`: what the collector saw, as sentient stored it (including process `exit`s).

A full check walks each chain from its first row and recomputes every hash. A mismatch names the first bad row. Triggers already reject `UPDATE` and `DELETE` through SQL ([storage-design.md](storage-design.md)); the chain is for an edit made outside SQL, directly on the file, and for damage (a bad disk, a bug).

## When it runs

- **A full check** a little after start (`--integrity-start-delay`, default 30s, so the start-up import is not slowed), then every `--integrity-interval` (default one hour).
- **A cheap check on a page load:** the Health page checks only the rows added since the last check, at most every 10 seconds, and skips while a full check runs or after a failure (a failure stands until the next full check).
- **Kernel events only when there are some:** with no kernel events stored (agent logs only mode) nothing is said about them.

## A background task, not a burden

The store has one database connection ([heartbeat.md](heartbeat.md), "The mechanism"), so a check that holds it blocks the Health page, the ingest and the API. So the full check:

- reads in **slices** (`--integrity-slice`, default 2,000 rows, about 80 ms of work on the first database it was timed on) and **lets go of the connection after each one**;
- **waits between slices** (`--integrity-pause`, default 200 ms, `0` for none), which roughly caps it at a quarter of the machine's time and still finishes 175,000 records in well under a minute;
- stops at once when sentientd is stopped, even in the middle of a pause.

A bigger slice and a shorter pause finish sooner and get in the way more; a smaller slice and a longer pause the reverse. The page-load check uses the default slice with no pause, since it only reads what is new.

## What the log says

```
checking that the stored agent log records are unchanged (hash chain)
agent log records verified: none changed or removed  records=175104 took=...
checking that the stored kernel events are unchanged (hash chain)
kernel events verified: none changed or removed  events=913452 took=...
```

A broken chain logs `HASH CHAIN BROKEN: a raw log record was changed or removed` or `... a kernel event ...` with the id, in place of the "verified" line.

## What Health shows

The hash chain tile says how many of each were checked ("2,000 agent log records and 913,452 kernel events checked") and when. A break turns it red ("Broken at kernel event #77", or both ids) and raises a critical warning that names what changed.

## What it does not stop

The chain is not keyed and its head is not kept anywhere outside the database. Someone who can edit the file, and who also recomputes every hash after the row they changed, passes the check. It catches naive edits and corruption, not a determined attacker with root. Closing that needs an anchor outside the database (signing or publishing the chain's head from time to time, or keying the hashes); not built.

`monitor_gaps` and `heartbeat` are not in either chain: gaps are closed by an `UPDATE` and the heartbeat is rewritten every beat, so neither can carry the triggers. Editing a gap would remove a "not compared" mark.

## Why this way, not that

- **Slices and a pause, not one long read.** One open query over every row held the only connection for the whole check (6.6 s on 175,000 records, every hour), which is what made Health wait.
- **A delay before the first check, not at once.** The start-up import and the first merge are already competing for the connection; the check can wait.
- **Settings, not constants.** A small machine and a big one want different pacing, and the right pause is not knowable in advance.
- **Both chains in one task,** one after the other, so there is one more background loop and not two.

## Related

- [heartbeat.md](heartbeat.md), [kernel-capture-card.md](kernel-capture-card.md): the other background work and the Health card.
- [storage-design.md](storage-design.md): the evidence tables and their triggers.
- Backlog: `8a98fe1`, `42c435d`.
