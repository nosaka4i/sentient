package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations are applied in order; migrations[i] brings the schema to
// version i+1. Never edit a released migration — append a new one.
var migrations = []string{
	// 1: plumbing (build step 1). Tables from docs/design.md "Store" that
	// sen status needs; processes, kernel_events, file_changes and
	// monitor_gaps arrive with kernel capture.
	`
CREATE TABLE sessions (
	id               INTEGER PRIMARY KEY,
	agent            TEXT NOT NULL,
	agent_session_id TEXT NOT NULL,
	repo_root        TEXT,
	cwd              TEXT,
	started_at       TEXT,
	ended_at         TEXT,
	UNIQUE (agent, agent_session_id)
);

CREATE TABLE prompts (
	id         INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL,
	text       TEXT NOT NULL,
	started_at TEXT NOT NULL,
	ended_at   TEXT,
	UNIQUE (session_id, seq)
);
CREATE INDEX prompts_started ON prompts(started_at);

-- Evidence: every agent log record verbatim, hash-chained.
CREATE TABLE raw_log_records (
	id          INTEGER PRIMARY KEY,
	session_id  INTEGER REFERENCES sessions(id),
	ts          TEXT,
	source_file TEXT NOT NULL,
	type        TEXT NOT NULL,
	unknown     INTEGER NOT NULL DEFAULT 0, -- 1 = unknown record type or unparsable line
	record      TEXT NOT NULL,              -- JSON as written by the agent
	prev_hash   TEXT NOT NULL,
	hash        TEXT NOT NULL
);
CREATE INDEX raw_log_records_ts ON raw_log_records(ts);

-- Normalized rows every view shows.
CREATE TABLE actions (
	id            INTEGER PRIMARY KEY,
	session_id    INTEGER REFERENCES sessions(id),
	prompt_id     INTEGER REFERENCES prompts(id),
	exec_id       TEXT,
	ts            TEXT NOT NULL,
	resource      TEXT NOT NULL,
	action        TEXT NOT NULL,
	path          TEXT,
	summary       TEXT,
	evidence      TEXT NOT NULL,            -- kernel | log | both
	raw_record_id INTEGER REFERENCES raw_log_records(id),
	tool_use_id   TEXT,                     -- links a log tool call to its result
	failed        INTEGER NOT NULL DEFAULT 0,
	lines_added   INTEGER NOT NULL DEFAULT 0,
	lines_removed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX actions_ts ON actions(ts);
CREATE UNIQUE INDEX actions_tool_use ON actions(tool_use_id) WHERE tool_use_id IS NOT NULL;

CREATE TABLE alerts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	action_id   INTEGER REFERENCES actions(id),
	ts          TEXT NOT NULL,
	severity    TEXT NOT NULL,
	rule        TEXT NOT NULL,
	status      TEXT NOT NULL DEFAULT 'open', -- open | reviewed
	reviewed_at TEXT
);

-- Ingester bookkeeping: how far each transcript file has been read, so a
-- restart resumes instead of re-ingesting.
CREATE TABLE ingest_files (
	path   TEXT PRIMARY KEY,
	offset INTEGER NOT NULL
);
`,
	// 2: one command execute per simple command (design.md "Counting
	// commands"): a Bash tool call becomes several actions, numbered by
	// part. Rows ingested before this keep part 0 (one per tool call).
	`
ALTER TABLE actions ADD COLUMN part INTEGER NOT NULL DEFAULT 0;
DROP INDEX actions_tool_use;
CREATE UNIQUE INDEX actions_tool_use ON actions(tool_use_id, part) WHERE tool_use_id IS NOT NULL;
CREATE INDEX actions_prompt ON actions(prompt_id);
`,
	// 3: health page. The agent's version as seen in its records, and a
	// partial index so counting unknown records doesn't scan the table.
	`
ALTER TABLE sessions ADD COLUMN agent_version TEXT;
CREATE INDEX raw_log_records_unknown ON raw_log_records(unknown) WHERE unknown = 1;
CREATE INDEX sessions_ended ON sessions(ended_at);
`,
	// 4: per-prompt values the lists sort and show, kept as columns instead
	// of derived on every read: the time of the prompt's last log record
	// (duration = this − started_at) and its count of actions on the system
	// (the systemAction rule in internal/query). Maintained by AppendRaw,
	// AddPrompt and AddAction; backfilled here.
	`
ALTER TABLE prompts ADD COLUMN last_activity_at TEXT;
ALTER TABLE prompts ADD COLUMN action_count INTEGER NOT NULL DEFAULT 0;
UPDATE prompts SET
	last_activity_at = COALESCE((SELECT MAX(r.ts) FROM raw_log_records r
		WHERE r.session_id = prompts.session_id AND r.ts >= prompts.started_at AND r.ts < COALESCE(prompts.ended_at, '9999')), prompts.started_at),
	action_count = (SELECT COUNT(*) FROM actions a WHERE a.prompt_id = prompts.id
		AND (a.resource IN ('file', 'dir', 'command', 'process', 'network', 'git', 'launch item') OR (a.resource = 'web' AND a.action = 'fetch')));
`,
	// 5: kernel evidence (docs/tetragon-integration.md). processes are the
	// agent's process tree as a collector saw it (Tetragon on Linux, eslogger
	// on macOS); kernel_events are what those processes did, verbatim and
	// hash-chained like raw_log_records; monitor_gaps are the times a
	// collector wasn't watching.
	`
CREATE TABLE processes (
	exec_id        TEXT PRIMARY KEY,                  -- unique per exec, from the collector
	collector      TEXT NOT NULL,                     -- tetragon | eslogger
	session_id     INTEGER REFERENCES sessions(id),   -- the agent session it belongs to, once attributed
	prompt_id      INTEGER REFERENCES prompts(id),
	pid            INTEGER NOT NULL,
	parent_exec_id TEXT,
	binary         TEXT NOT NULL,
	argv           TEXT,                              -- JSON array
	cwd            TEXT,
	uid            INTEGER,
	started_at     TEXT NOT NULL,
	exited_at      TEXT,
	exit_code      INTEGER
);
CREATE INDEX processes_session ON processes(session_id);
CREATE INDEX processes_parent ON processes(parent_exec_id);
CREATE INDEX processes_started ON processes(started_at);

CREATE TABLE kernel_events (
	id        INTEGER PRIMARY KEY,
	exec_id   TEXT,                                   -- the process that did it
	ts        TEXT NOT NULL,
	type      TEXT NOT NULL,                          -- exec | exit | file_create | file_write | file_unlink | file_rename | file_setmode | net_connect | kill | ...
	path      TEXT,
	path2     TEXT,                                   -- rename target
	payload   TEXT NOT NULL,                          -- the collector's record, verbatim JSON
	prev_hash TEXT NOT NULL,
	hash      TEXT NOT NULL
);
CREATE INDEX kernel_events_ts ON kernel_events(ts);
CREATE INDEX kernel_events_exec ON kernel_events(exec_id);

CREATE TABLE monitor_gaps (
	id         INTEGER PRIMARY KEY,
	collector  TEXT NOT NULL,
	started_at TEXT NOT NULL,
	ended_at   TEXT,                                  -- NULL while the gap is still open
	reason     TEXT NOT NULL
);
`,
	// 6: how sure the session of a process is. exact: the only session in that
	// directory active while the agent ran; ambiguous: several were, and the
	// one that became active first was taken. Children inherit their parent's.
	`
ALTER TABLE processes ADD COLUMN attribution TEXT;
`,
		// 7: evidence is immutable (docs/storage-design.md). The links between a
		// process and a session or prompt move out of processes into their own
		// table, so processes is only an index of exec and exit events and the
		// links can be dropped and recomputed. kernel_events and raw_log_records
		// reject UPDATE and DELETE; exits are appended to the kernel chain.
		`
CREATE TABLE process_attribution (
	exec_id     TEXT PRIMARY KEY,
	session_id  INTEGER NOT NULL REFERENCES sessions(id),
	prompt_id   INTEGER REFERENCES prompts(id),
	attribution TEXT                                  -- exact | ambiguous
);
CREATE INDEX process_attribution_session ON process_attribution(session_id);
INSERT INTO process_attribution (exec_id, session_id, prompt_id, attribution)
	SELECT exec_id, session_id, prompt_id, attribution FROM processes WHERE session_id IS NOT NULL;

CREATE TABLE processes_new (
	exec_id        TEXT PRIMARY KEY,
	collector      TEXT NOT NULL,
	pid            INTEGER NOT NULL,
	parent_exec_id TEXT,
	binary         TEXT NOT NULL,
	argv           TEXT,
	cwd            TEXT,
	uid            INTEGER,
	started_at     TEXT NOT NULL,
	exited_at      TEXT,
	exit_code      INTEGER
);
INSERT INTO processes_new (exec_id, collector, pid, parent_exec_id, binary, argv, cwd, uid, started_at, exited_at, exit_code)
	SELECT exec_id, collector, pid, parent_exec_id, binary, argv, cwd, uid, started_at, exited_at, exit_code FROM processes;
DROP TABLE processes;
ALTER TABLE processes_new RENAME TO processes;
CREATE INDEX processes_parent ON processes(parent_exec_id);
CREATE INDEX processes_started ON processes(started_at);

CREATE TRIGGER raw_log_records_no_update BEFORE UPDATE ON raw_log_records
	BEGIN SELECT RAISE(ABORT, 'raw_log_records is append-only'); END;
CREATE TRIGGER raw_log_records_no_delete BEFORE DELETE ON raw_log_records
	BEGIN SELECT RAISE(ABORT, 'raw_log_records is append-only'); END;
CREATE TRIGGER kernel_events_no_update BEFORE UPDATE ON kernel_events
	BEGIN SELECT RAISE(ABORT, 'kernel_events is append-only'); END;
CREATE TRIGGER kernel_events_no_delete BEFORE DELETE ON kernel_events
	BEGIN SELECT RAISE(ABORT, 'kernel_events is append-only'); END;
`,
}

// SchemaVersion is the version the code expects.
func SchemaVersion() int { return len(migrations) }

// Migrate brings the schema up to date, one transaction per migration.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	cur, err := s.Version(ctx)
	if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this sentientd (%d)", cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		if err := s.apply(ctx, v+1, migrations[v]); err != nil {
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
	}
	return nil
}

func (s *Store) apply(ctx context.Context, version int, ddl string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// Version returns the current schema version (0 for a new database).
func (s *Store) Version(ctx context.Context) (int, error) {
	var v int
	err := s.DB.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	return v, nil
}
