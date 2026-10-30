# Storage design

**Status:** proposed, not yet built. Backlog `1d2c2b3`; related `889e10d`, `6efed70`, `bce526c`. Stage 1 (one hot SQLite file) is built first; stage 2 (flush sealed segments to a Parquet archive tier) is the long-term direction.

## Context

One import of this machine's Claude transcripts (412 MB on disk) produced a 570 MB `sentient.db`. Where it went (scratch database, 2026-10-24):

| Table | Rows | Size |
|---|---|---|
| `raw_log_records` | 156,867 | 518 MB |
| `kernel_events` | 23,510 | 7.6 MB |
| `actions` | | 5.9 MB |
| `prompts` | | 3.3 MB |

Inside `raw_log_records`, `assistant` (111 MB), `file-history-snapshot` (103 MB, 4,941 records of about 22 KB), `user` (93 MB) and `api-request` (55 MB) are most of it. The 4,295 records over 20 KB are 175 MB, a third of the table. In `kernel_events`, `file_open` is 22,003 of 23,510 events.

This compares years of transcript history with a few hours of kernel capture, so it does not say which source grows faster. A heavy run measured about 220 kernel events/s. Bytes per active hour of each source is measured before retention and the `file_open` policy are set (see Measure first).

Other things that went wrong, or are about to:

- The import and the kernel collector shared one write connection. The collector fell behind, then failed with `SQLITE_BUSY` (fixed with `_txlock=immediate`, one connection and an in-memory queue, `internal/collector/buffer.go`; the full import then ran with 0 gaps).
- Process exits are not in the hash chain. `internal/store/kernel.go` `ExitProcess` updates `processes.exited_at` and `exit_code` in place, and no `exit` event is appended to `kernel_events`.
- `processes` and `actions` are updated after they are inserted (attribution, merge), next to evidence tables that are meant to be append-only.
- Retention is undefined. Deleting rows from a hash chain breaks its verification.
- The Tetragon stream is live only (no replay), so a slow or absent reader loses events; the collector records a gap when it knows.

Goals: contain the size, keep the evidence immutable and verifiable, never let two writers wait on each other, and be able to throw away and rebuild everything derived.

## Mechanism

**Built so far (migration 7):** `exit` appended to the kernel chain by `ExitProcess`; `process_attribution` replaces the session, prompt and attribution columns on `processes`; triggers reject `UPDATE` and `DELETE` on `kernel_events` and `raw_log_records`. Exits recorded before this migration exist only in the `processes` index. **Compression built:** records of 256 bytes or more are stored as zstd BLOBs in `raw_log_records.record` (smaller ones stay TEXT); the hash covers the original text; readers use `store.DecodeRecord`; Rows stored as text before this are not converted (the schema is not released yet; the scratch database is deleted and re-imported). Measured, with a one-off conversion of the scratch database, on the 570 MB scratch database: 106,138 records compressed in 59 s (peak 150 MB resident for the whole test program), record bytes 518 MB to 163 MB (3.2 times), chain verification 8 s. The file does not shrink until a `VACUUM`: `VACUUM INTO` gave 262 MB, of which `raw_log_records` is 235 MB. The rest of that table is per-row overhead: two hex hashes (`prev_hash` repeats the previous row's `hash`), the `source_file` path on every row, and page overhead. Next levers there: drop `prev_hash` or store hashes as 32-byte blobs, and store `source_file` once.

Not yet built: by-reference large results, the `file_open` policy, the import throttle, the action links table, `monitor_gaps` immutability (closing a gap still updates its row).

The goals are properties of tables, not of files, so stage 1 changes tables and keeps one `sentient.db` (one connection, WAL).

Three classes of table:

1. **Evidence** (append-only): `kernel_events` (hash chain, including process `exec` and `exit`), `monitor_gaps`, `raw_log_records` (hash chain, all agents, with an `agent` column). SQLite triggers reject `UPDATE` and `DELETE` on them, so immutability is enforced by the database and not by convention.
2. **Derived** (rebuildable from evidence): `sessions`, `prompts`, `actions`, the `processes` index, `process_attribution`, `action_links` (the merge), alert rows, import offsets. Correlation code can drop and regenerate all of it. Improving a rule means recomputing, never touching evidence.
3. **Irreplaceable**: alert review marks and user settings. The only rows a backup must not miss.

Changes that make this true:

- `exit` becomes a chained `kernel_events` row with status and time. The `processes` index is built from `exec` and `exit`, and no longer updated by the collector.
- Links live in their own tables, never as columns on evidence rows. `processes.session_id`, `prompt_id` and `attribution` and the planned `actions.exec_id` / `evidence` move to `process_attribution` and `action_links` (migration 7). They may be stale or missing, never wrong in a way that affects evidence.
- Evidence is written only by its collector or ingester, through the queue and batched transactions (`BufferedSink`). Nobody else writes it.
- One file means one transaction order across both chains, so no cross-anchoring is needed.

### Containing the size

In order of payoff, none of them lossy to the evidence:

1. **Compress the record text** (zstd, per record or per block) in `raw_log_records`. JSON compresses 5 to 10 times. Hash input stays the original bytes, so verification decompresses first.
2. **Large tool results by reference.** Records over a threshold (for example 20 KB: file snapshots, big command output) are stored once, compressed, content-addressed by hash, and the chained record holds the hash. Identical snapshots are stored once.
3. **Kernel `file_open` volume.** 83% of the events (measured 2026-10-30), and about 65% of all events are opens of build caches, toolchains, system libraries and `/proc/<pid>/stat`. Nothing filters them today: neither the policy nor the collector. Handled by a verbosity setting: see [capture-verbosity.md](capture-verbosity.md).
4. **Import throttle.** A short pause between batches caps the import's write rate, so a large backlog drains over minutes without saturating a small disk.
5. **Retention.** Stage 2 below.

### Measure first

Before choosing thresholds, retention or the file layout:

- Bytes per active hour of kernel events and of log records over a real working day.
- Import I/O: bytes written, peak rate, duration.
- The same data as SQLite, compressed JSONL and Parquet (`bce526c`).

### Stage 2: flush to an archive tier (the long-term direction)

SQLite is the hot tier: a recent window (for example the last weeks) that is live-appended and queried by the UI. Older evidence is flushed into an archive of Parquet files (zstd), one sealed segment per source and period. Each segment's first record carries the previous segment's head hash, and a hashed manifest lists the segments, so the chain verifies across the archive and expiring evidence deletes whole segments. Once a segment is archived and verified, its rows leave the hot tier. Derived and irreplaceable tables stay in `sentient.db`; the archive is read through a small index for "events of this exec_id" and for offline analysis. Writer in pure Go (`parquet-go` or `arrow-go`); `go-duckdb` needs CGO and is excluded as the store, but DuckDB can query the Parquet files offline.

There is one SQLite database, not several. Removing an archived period from it is a controlled operation (the archiver, after verifying the segment, deletes those rows in one transaction, with the immutability triggers allowing deletes only for rows already in a verified segment). Whether that needs anything beyond that is decided with the measurements.

### Startup and the live stream

The collector subscribes first and the import runs behind it; the in-memory queue (50,000 operations) absorbs the difference. Tetragon replays nothing, so the time `sentientd` is not running is a gap, to be recorded from the last stored event time to the next connection. The Tetragon export file is the only replay source; see the open questions.

## Why X, not Y

- **One file now, not three.** Write contention is already solved by the queue. Immutability, rebuildable derived data and size containment are table-level changes. Three files add costs with no payoff yet: three migration paths, WAL files and connections; API reads and state writes still share one connection; a crash can leave the files out of step; every backup covers all of them; `ATTACH` is limited (10 by default), which segments would use up; cross-file transactions are lost (acceptable only because links are rebuildable); and the chains need cross-anchoring to order events between files.
- **Two files (evidence + state), not now.** Same costs on a smaller scale. It becomes attractive with stage 2.
- **Loki, Elasticsearch (via Filebeat), Fluent Bit.** They ship Tetragon's JSON export to a server. They need a service and object storage, depend on the export file, and store raw events rather than events correlated with sessions and prompts. Wrong size for a developer's machine.
- **DuckDB.** Fits the data shape (immutable, scanned, aggregated), but its Go driver needs CGO. Possible later as an offline analysis tool over Parquet segments, not as the store.
- **Parquet everywhere.** Cannot append, so a live tail would still need SQLite; weak point lookups for the UI's "events of this exec_id". Hence a hybrid, later.
- **Plain append-only files with a hash manifest.** Simplest for immutability and rotation, but queries need an index we would write ourselves. SQLite gives that for free on the hot tier.
- **Deleting old rows from one chain.** Breaks verification; segments with carried-over head hashes avoid it.

## Open questions

- **Tetragon export file.** Off by default (simple, gaps when `sentientd` is down), or on with rotation and a catch-up reader that fills the gap from the file. Current box: on, about 9 MB across rotated files.
- **One log table for all agents or per agent.** Leaning one with an `agent` column.
- **Triggers vs retention.** Triggers block deletes, so stage 2 expiry drops whole segment files rather than rows.
- **Segment size and rollover trigger**, and who verifies a chain across segments (daemon hourly, or `sen verify`).
- **Capture verbosity:** its own questions (names, Tetragon operator behaviour, raw-export caps, free-space threshold) are in [capture-verbosity.md](capture-verbosity.md).
- **Migration.** Reshape the existing `sentient.db` in place (migration 7), or start fresh and re-import (the transcripts are still on disk).
- **Backup.** A consistent snapshot of one file (SQLite `VACUUM INTO`).

## Related and deferred

- `docs/tetragon-integration.md` (what the collector and the merge write; the merge and attribution links move to link tables).
- `docs/design.md` "Store" (the original single-database layout).
- [capture-verbosity.md](capture-verbosity.md) (how much of the kernel stream is recorded: levels, the noise list, estimated size).
- Backlog: `1d2c2b3` (this design), `889e10d` (split files, now stage 2), `6efed70` (retention), `bce526c` (Parquet), `00b522e` (grouping background commands), `bbee4b1` (capture verbosity).
