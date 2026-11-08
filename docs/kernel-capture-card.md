# The Kernel capture card

**Status:** built: `internal/collector/status.go` (what a collector reports about itself), `internal/collector/tetragon/client.go` (the Tetragon collector fills it in), `internal/query/collector.go` (what the database holds), `internal/web/capturecard.go` and `web/templates/health.html` (the card), `internal/health/health.go` (the warning). Mock: `.mocks/health-05.html`. Not built: a collector for macOS or Windows (the card is designed for them, see "One card for every platform"), the heartbeat that records when sentientd itself was down (undecided), and Tetragon's own lost-event counters. Backlog `3ffc94f`.

The one Health card that answers "is the kernel side of sentient working?". It replaces the two cards (Capture and Collector) that the first mocks had, which both said "off" when there was no collector and both carried the policy.

## What it shows

| tile | says |
|---|---|
| capture system | the collector's name and version ("Tetragon v1.7.1"), its platform, and "capturing since" |
| events / stored | kernel events in the database, and since when |
| last event | how long ago, and the rate over the last minute |
| level / policy | the level sentientd runs at and the installed policy's; one tile while they agree, two (with the fix) when they do not. Tetragon's concept: other collectors do not show it |
| disk used | the **whole disk** sentient's data is on, not what sentient or the collector wrote; recording pauses at the high threshold ([disk-thresholds.md](disk-thresholds.md)) |
| paused since | only while paused |
| processes / tracked | all time, and how many belong to an agent |
| dropped | what **sentient** loses itself (its write queue overflowing), the queue's depth, and the reconnects. It does not claim Tetragon dropped nothing: Tetragon's own counters are in its Prometheus metrics, which sentient does not read |
| gaps / kernel not recorded | all gaps, and the latest one |
| socket (or process) | where the collector reads from |

The chip says the worst state: `recording`, `connecting`, `reconnecting` (amber for the first 30 seconds, red after), `recording paused`, or `agent logs only`.

## When the collector is lost

The collector retries every 2 seconds and records the outage as a gap. After **30 seconds** unreachable (`health.CollectorGrace`, so a routine Tetragon restart does not flash red) Health raises a critical warning, "Tetragon is not reachable", with the last error ("permission denied", "no such file or directory": found by a plain dial), headline "Kernel capture lost", and the Health icon gets a red dot. There is no banner: that is for the disk pause. The agent's actions in that time are shown as logged and marked not compared (see [tetragon-integration.md](tetragon-integration.md)).

## Agent logs only

sentientd works without a kernel collector, as a nicer view of the agent's own logs. That is a normal mode, not a fault, so nothing warns: no dot, no red, and "Lost" never applies. But it is not the same product: without the kernel, sentient can only repeat what the agent says it did. The card is muted and says so: "Actions are shown as the agent logged them. Nothing independent confirms them. Start sentientd with `--tetragon` to add kernel evidence."

## One card for every platform

`collector.Status` is the same for every collector: its `System` ("Tetragon", "eslogger"), `Platform`, one `Detail` line with a label (a socket, a process), a version if it can say, and the state, the last error, the event count and rate. The Health code never imports a platform package. A collector adds its own tiles only where the concept is its own (Tetragon's level and policy). A macOS eslogger collector, and later a Windows one, fill in the same fields and the card needs no change.

## Why this way, not that

- **One card, not two.** Two cards said the same thing when off, shared the policy tile, and made someone read both to learn why nothing was recorded.
- **The name is a tile of its own.** The version was first a small line under the socket path, where it was easy to miss.
- **"Capturing since", not "connected since" or "running since".** Tetragon is a separate service that was probably running long before sentientd, so "running since" is wrong for it; "connected since" is accurate but is a transport word. "Capturing since" is true for each collector (for eslogger it is when sentientd started it).
- **A grace period before the warning.** A restart of Tetragon takes a few seconds; a red alarm for each is noise that teaches people to ignore it.
- **The status times use the real clock, not the collector's injectable one,** so the tests that count the collector's clock calls for gap times are unaffected.

## Known limits

- **A gap is only recorded while sentientd runs.** While sentientd is down no gap is written, so the gaps tile does not count that downtime and the agent's actions in it look unverified. The heartbeat that fixes this is next (backlog).
- **"Processes tracked" and "events stored since"** are all-time, from the database, so they include history from before the last restart.
- **While the database is busy** (a start after a long import) the database-backed tiles read "still counting", and a notice says sentientd is processing logs since the last startup (backlog `42c435d`).

## Related

- [capture-verbosity.md](capture-verbosity.md): the level and the policy.
- [disk-thresholds.md](disk-thresholds.md): the pause and the disk tile.
- [tetragon-integration.md](tetragon-integration.md): the collector and the "not compared" accounting.
