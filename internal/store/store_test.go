package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.Version(ctx)
	if err != nil || v != SchemaVersion() || v < 1 {
		t.Fatalf("Version = %d, %v; want %d", v, err, SchemaVersion())
	}
	var mode string
	if err := st.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v; want wal", mode, err)
	}
	for _, table := range []string{"sessions", "prompts", "raw_log_records", "actions", "alerts", "ingest_files"} {
		var n int
		if err := st.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	st.Close()

	// Reopening is idempotent.
	st, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Version(ctx); v != SchemaVersion() {
		t.Errorf("after reopen Version = %d", v)
	}
	var rows int
	st.DB.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&rows)
	if rows != 1 {
		t.Errorf("schema_version rows = %d, want 1", rows)
	}

	// A database from a newer sentientd is refused.
	if _, err := st.DB.Exec(`UPDATE schema_version SET version = 999`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(ctx, dir); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("Open newer schema: err = %v", err)
	}
}

func TestUpgradeFromVersion1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	all := migrations
	migrations = all[:1] // a database written by the step-1 sentientd
	st, err := Open(ctx, dir)
	migrations = all
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO actions (ts, resource, action, evidence, tool_use_id) VALUES ('t', 'command', 'execute', 'log', 'tu1')`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, _ := st.Version(ctx); v != 2 {
		t.Fatalf("version after upgrade = %d", v)
	}
	var part int
	if err := st.DB.QueryRow(`SELECT part FROM actions WHERE tool_use_id = 'tu1'`).Scan(&part); err != nil || part != 0 {
		t.Errorf("old row part = %d, %v", part, err)
	}
	// The same tool call may now have several parts.
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	if err := tx.AddAction(ctx, Action{TS: "t", Resource: "command", Action: "execute", Evidence: "log", ToolUseID: "tu1", Part: 1}); err != nil {
		t.Fatal(err)
	}
	var n int
	tx.tx.QueryRow(`SELECT COUNT(*) FROM actions WHERE tool_use_id = 'tu1'`).Scan(&n)
	if n != 2 {
		t.Errorf("parts = %d, want 2", n)
	}
}

func TestOpenBadDir(t *testing.T) {
	if _, err := Open(context.Background(), "/proc/sentient-nope"); err == nil {
		t.Error("want error creating data dir under /proc")
	}
}

func TestWritesAndChain(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	tx, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	sid, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s1", TS: "2026-10-17T09:00:00.000000000Z"})
	if err != nil {
		t.Fatal(err)
	}
	// cwd fills in later; started/ended widen.
	sid2, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s1", Cwd: "/w", RepoRoot: "/w", TS: "2026-10-17T08:00:00.000000000Z"})
	if err != nil || sid2 != sid {
		t.Fatalf("upsert again: %d %v", sid2, err)
	}
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s1", Cwd: "/other", TS: "2026-10-17T10:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s1"}); err != nil {
		t.Fatal(err)
	}
	rr, cwd, err := tx.SessionRepoRoot(ctx, sid)
	if err != nil || rr != "/w" || cwd != "/w" {
		t.Errorf("SessionRepoRoot = %q %q %v", rr, cwd, err)
	}

	p1, _ := tx.AddPrompt(ctx, sid, "one", "2026-10-17T09:00:00.000000000Z")
	p2, _ := tx.AddPrompt(ctx, sid, "two", "2026-10-17T09:30:00.000000000Z")
	if id, _ := tx.PromptAt(ctx, sid, "2026-10-17T09:10:00.000000000Z"); id != p1 {
		t.Errorf("PromptAt 09:10 = %d, want %d", id, p1)
	}
	if id, _ := tx.PromptAt(ctx, sid, "2026-10-17T11:00:00.000000000Z"); id != p2 {
		t.Errorf("PromptAt 11:00 = %d, want %d", id, p2)
	}
	if id, _ := tx.PromptAt(ctx, sid, "2026-10-17T07:00:00.000000000Z"); id != 0 {
		t.Errorf("PromptAt before any prompt = %d", id)
	}

	var rawIDs []int64
	for i, rec := range []string{`{"a":1}`, `{"b":2}`, `{"c":3}`} {
		id, err := tx.AppendRaw(ctx, RawRecord{SessionID: sid, TS: "2026-10-17T09:00:0" + string(rune('0'+i)) + ".000000000Z", SourceFile: "f", Type: "user", Record: rec})
		if err != nil {
			t.Fatal(err)
		}
		rawIDs = append(rawIDs, id)
	}
	if _, err := tx.AppendRaw(ctx, RawRecord{SourceFile: "f", Unknown: true, Record: "{bad"}); err != nil {
		t.Fatal(err)
	}

	a := Action{SessionID: sid, PromptID: p1, TS: "2026-10-17T09:00:01.000000000Z", Resource: "file", Action: "update",
		Path: "/w/x", Evidence: "log", RawRecordID: rawIDs[0], ToolUseID: "tu1"}
	if err := tx.AddAction(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := tx.AddAction(ctx, a); err != nil { // duplicate tool call is ignored
		t.Fatal(err)
	}
	ok, err := tx.ApplyToolResult(ctx, ToolResult{ToolUseID: "tu1", Action: "create", LinesAdded: 3})
	if err != nil || !ok {
		t.Fatalf("ApplyToolResult = %v %v", ok, err)
	}
	if ok, _ := tx.ApplyToolResult(ctx, ToolResult{ToolUseID: "nope"}); ok {
		t.Error("ApplyToolResult for unknown tool_use_id reported a match")
	}
	if err := tx.SetOffset(ctx, "f", 42); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetOffset(ctx, "f", 84); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if off, _ := st.Offset(ctx, "f"); off != 84 {
		t.Errorf("Offset = %d, want 84", off)
	}
	if off, _ := st.Offset(ctx, "g"); off != 0 {
		t.Errorf("Offset unknown file = %d", off)
	}
	var n int
	var verb string
	var added int
	st.DB.QueryRow(`SELECT COUNT(*), MAX(action), MAX(lines_added) FROM actions`).Scan(&n, &verb, &added)
	if n != 1 || verb != "create" || added != 3 {
		t.Errorf("actions: n=%d verb=%q added=%d", n, verb, added)
	}
	var started, ended string
	st.DB.QueryRow(`SELECT started_at, ended_at FROM sessions`).Scan(&started, &ended)
	if started != "2026-10-17T08:00:00.000000000Z" || ended != "2026-10-17T10:00:00.000000000Z" {
		t.Errorf("session window = %s..%s", started, ended)
	}

	if bad, err := st.VerifyChain(ctx); err != nil || bad != 0 {
		t.Fatalf("VerifyChain intact = %d %v", bad, err)
	}
	// Editing a record breaks the chain at that record.
	if _, err := st.DB.Exec(`UPDATE raw_log_records SET record = '{"b":3}' WHERE id = ?`, rawIDs[1]); err != nil {
		t.Fatal(err)
	}
	if bad, _ := st.VerifyChain(ctx); bad != rawIDs[1] {
		t.Errorf("VerifyChain after edit = %d, want %d", bad, rawIDs[1])
	}
}

func TestChainHashDistinguishesFields(t *testing.T) {
	if ChainHash("", "ab", "c", "") == ChainHash("", "a", "bc", "") {
		t.Error("field boundaries must be part of the hash")
	}
}
