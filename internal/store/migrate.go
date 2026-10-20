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
