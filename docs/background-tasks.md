# What sentientd does in the background

**Status:** reference doc. Matches the code as of v0.0.3 (`internal/daemon/daemon.go` starts them). Update it when a task is added or its interval changes.

sentientd is one process with several loops. This lists them, what each costs, and which setting controls it, for someone running it on a small machine.

## The one thing they share

The store has **one database connection** (`SetMaxOpenConns(1)`, `internal/store/store.go`), and SQLite allows one writer at a time for the whole file. So every task below, and every request from the Health page and the API, takes turns on it. A task that holds the connection for a long time makes the others wait. That is why the long ones work in slices and let go between them ([integrity-check.md](integrity-check.md)), and why a write that cannot get in time is dropped, not queued ([heartbeat.md](heartbeat.md)). The first import of a big history is the worst case (backlog `42c435d`).

## The tasks

| task | runs | touches the database | setting | more |
|---|---|---|---|---|
| **Transcript ingest** | every 2 s | batches of 200 records, one transaction each; a large first import logs its progress | `--poll-interval` | `internal/ingest/claudecode` |
| **Disk guard** | every 15 s | one read of the disk; a gap write only when it pauses or resumes | `--disk-high-threshold`, `--disk-low-threshold` (the interval is fixed) | [disk-thresholds.md](disk-thresholds.md) |
| **Heartbeat** | every 30 s, only with a kernel collector | one small write; dropped if the database is busy | none (the interval is a constant, `heartbeatEvery`) | [heartbeat.md](heartbeat.md) |
| **Integrity check** | a full pass 30 s after start, then hourly; a small one on a Health page load | reads in slices of 2,000 rows, letting go of the connection between them | `--integrity-interval`, `--integrity-slice`, `--integrity-pause`, `--integrity-start-delay` | [integrity-check.md](integrity-check.md) |
| **Attribute** (tie kernel processes to agent sessions) | every 5 s, only with a kernel collector | one transaction per pass | none | [tetragon-integration.md](tetragon-integration.md) |
| **Merge** (match logged commands with kernel execs) | every 5 s, only with a kernel collector | one transaction per pass | none | [tetragon-integration.md](tetragon-integration.md) |
| **Kernel collector** | continuous; retries a lost connection every 2 s | none itself: it feeds the queue below | `--tetragon`, `--verbosity` | [tetragon-integration.md](tetragon-integration.md), [kernel-capture-card.md](kernel-capture-card.md) |
| **Kernel write queue** | continuous | batches of up to 500 events, one transaction each; drops and records a gap if the queue (50,000) overflows | none | `internal/collector/buffer.go` |
| **Health monitor** | CPU sampled every 30 s (a 5-minute average) | none | none | `internal/daemon/health.go` |
| **Sudo check** | when Health asks, cached for 10 minutes | none (runs `sudo -l`) | none | Health page, "passwordless sudo" |

Everything except the ingest, the disk guard, the health monitor and the sudo check runs only when a kernel collector is on (`--tetragon`). In agent logs only mode sentientd runs just those four, plus the integrity check.

## While recording is paused

When the disk guard pauses recording, the ingest, attribute, merge and the kernel gate stop (see [disk-thresholds.md](disk-thresholds.md)). The disk guard, the heartbeat and the integrity check keep running: they write almost nothing.

## Related

- [integrity-check.md](integrity-check.md), [heartbeat.md](heartbeat.md), [disk-thresholds.md](disk-thresholds.md): the three with their own docs.
- Backlog: `42c435d` (startup contention).
