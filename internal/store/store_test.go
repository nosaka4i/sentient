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
	if v, _ := st.Version(ctx); v != SchemaVersion() {
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

func TestUpgradeBackfillsPromptColumns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	all := migrations
	migrations = all[:3] // a database from before last_activity_at / action_count
	st, err := Open(ctx, dir)
	migrations = all
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sessions (id, agent, agent_session_id) VALUES (1, 'claude', 's')`,
		`INSERT INTO prompts (id, session_id, seq, text, started_at, ended_at) VALUES (1, 1, 1, 'a', '2026-10-17T09:00:00Z', '2026-10-17T10:00:00Z')`,
		`INSERT INTO prompts (id, session_id, seq, text, started_at) VALUES (2, 1, 2, 'b', '2026-10-17T10:00:00Z')`,
		`INSERT INTO prompts (id, session_id, seq, text, started_at) VALUES (3, 1, 3, 'quiet', '2026-10-17T11:00:00Z')`,
		// activity of prompt 1 up to 09:20; the 10:00 record opens prompt 2; prompt 2 runs to 10:07
		`INSERT INTO raw_log_records (session_id, ts, source_file, type, record, prev_hash, hash) VALUES (1, '2026-10-17T09:05:00Z', 'f', 'assistant', '{}', '', 'h1')`,
		`INSERT INTO raw_log_records (session_id, ts, source_file, type, record, prev_hash, hash) VALUES (1, '2026-10-17T09:20:00Z', 'f', 'assistant', '{}', 'h1', 'h2')`,
		`INSERT INTO raw_log_records (session_id, ts, source_file, type, record, prev_hash, hash) VALUES (1, '2026-10-17T10:00:00Z', 'f', 'user', '{}', 'h2', 'h3')`,
		`INSERT INTO raw_log_records (session_id, ts, source_file, type, record, prev_hash, hash) VALUES (1, '2026-10-17T10:07:00Z', 'f', 'assistant', '{}', 'h3', 'h4')`,
		// prompt 1: 2 system actions + a subagent start + a web search (not counted) + a web fetch (counted)
		`INSERT INTO actions (prompt_id, ts, resource, action, evidence, tool_use_id) VALUES (1, 't', 'file', 'read', 'log', 'a')`,
		`INSERT INTO actions (prompt_id, ts, resource, action, evidence, tool_use_id) VALUES (1, 't', 'command', 'execute', 'log', 'b')`,
		`INSERT INTO actions (prompt_id, ts, resource, action, evidence, tool_use_id) VALUES (1, 't', 'subagent', 'start', 'log', 'c')`,
		`INSERT INTO actions (prompt_id, ts, resource, action, evidence, tool_use_id) VALUES (1, 't', 'web', 'search', 'log', 'd')`,
		`INSERT INTO actions (prompt_id, ts, resource, action, evidence, tool_use_id) VALUES (1, 't', 'web', 'fetch', 'log', 'e')`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	st, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := map[int64]struct {
		last    string
		actions int
	}{
		1: {"2026-10-17T09:20:00Z", 3},
		2: {"2026-10-17T10:07:00Z", 0},
		3: {"2026-11-01T00:00:00Z", 0}, // placeholder, replaced below
	}
	for id, w := range want {
		var last string
		var n int
		if err := st.DB.QueryRow(`SELECT last_activity_at, action_count FROM prompts WHERE id = ?`, id).Scan(&last, &n); err != nil {
			t.Fatal(err)
		}
		if id == 3 {
			w.last = "2026-10-17T11:00:00Z" // no records of its own: its own start
		}
		if last != w.last || n != w.actions {
			t.Errorf("prompt %d: last_activity_at %q action_count %d, want %q and %d", id, last, n, w.last, w.actions)
		}
	}
}

func TestPromptActivityAndActionCountAreMaintained(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s"})
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	raw := func(at string) {
		t.Helper()
		if _, err := tx.AppendRaw(ctx, RawRecord{SessionID: sid, TS: ts(at), SourceFile: "f", Type: "x", Record: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	prompt := func(at string) int64 {
		t.Helper()
		// like the ingester: the prompt's own record is appended first
		raw(at)
		id, err := tx.AddPrompt(ctx, sid, "p", ts(at))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	raw("08:55:00") // before any prompt: nobody's activity
	p1 := prompt("09:00:00")
	raw("09:05:00")
	raw("09:20:00")
	p2 := prompt("10:00:00") // the 10:00 record must not extend prompt 1
	raw("10:07:00")
	raw("10:03:00") // out of order: never moves the last activity back

	for _, a := range []Action{
		{PromptID: p1, Resource: "file", Action: "read", ToolUseID: "t1"},
		{PromptID: p1, Resource: "command", Action: "execute", ToolUseID: "t2", Part: 0},
		{PromptID: p1, Resource: "command", Action: "execute", ToolUseID: "t2", Part: 1},
		{PromptID: p1, Resource: "command", Action: "execute", ToolUseID: "t2", Part: 1}, // duplicate: not counted
		{PromptID: p1, Resource: "subagent", Action: "start", ToolUseID: "t3"},
		{PromptID: p1, Resource: "web", Action: "search", ToolUseID: "t4"},
		{PromptID: p1, Resource: "web", Action: "fetch", ToolUseID: "t5"},
		{PromptID: 0, Resource: "file", Action: "read", ToolUseID: "t6"}, // no prompt: nothing to count
		{PromptID: p2, Resource: "git", Action: "commit", ToolUseID: "t7"},
	} {
		a.SessionID, a.TS, a.Evidence = sid, ts("09:30:00"), "log"
		if err := tx.AddAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, w := range []struct {
		id      int64
		last    string
		actions int
	}{
		{p1, ts("09:20:00"), 4},
		{p2, ts("10:07:00"), 1},
	} {
		var last string
		var n int
		if err := st.DB.QueryRow(`SELECT last_activity_at, action_count FROM prompts WHERE id = ?`, w.id).Scan(&last, &n); err != nil {
			t.Fatal(err)
		}
		if last != w.last || n != w.actions {
			t.Errorf("prompt %d: last_activity_at %q action_count %d, want %q and %d", w.id, last, n, w.last, w.actions)
		}
	}
}

func TestIsSystemAction(t *testing.T) {
	for _, tc := range []struct {
		res, act string
		want     bool
	}{
		{"file", "update", true}, {"dir", "create", true}, {"command", "execute", true}, {"process", "kill", true},
		{"network", "connect", true}, {"git", "push", true}, {"launch item", "add", true},
		{"web", "fetch", true}, {"web", "search", false}, {"prompt", "request", false}, {"subagent", "start", false}, {"monitor", "gap", false},
	} {
		if got := IsSystemAction(tc.res, tc.act); got != tc.want {
			t.Errorf("IsSystemAction(%q, %q) = %v", tc.res, tc.act, got)
		}
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
	dropGuards(t, st)
	if _, err := st.DB.Exec(`UPDATE raw_log_records SET record = '{"b":3}' WHERE id = ?`, rawIDs[1]); err != nil {
		t.Fatal(err)
	}
	if bad, _ := st.VerifyChain(ctx); bad != rawIDs[1] {
		t.Errorf("VerifyChain after edit = %d, want %d", bad, rawIDs[1])
	}
}

func TestVerifyChainIncremental(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	appendN := func(n int) {
		tx, _ := st.Begin(ctx)
		for i := 0; i < n; i++ {
			if _, err := tx.AppendRaw(ctx, RawRecord{SourceFile: "f", Type: "user", Record: `{"i":1}`}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendN(3)
	full, err := st.VerifyChainFrom(ctx, ChainPoint{})
	if err != nil || full.Checked != 3 || full.BadID != 0 || full.Last.ID != 3 {
		t.Fatalf("full = %+v, %v", full, err)
	}
	appendN(2)
	inc, err := st.VerifyChainFrom(ctx, full.Last)
	if err != nil || inc.Checked != 2 || inc.BadID != 0 || inc.Last.ID != 5 {
		t.Fatalf("incremental = %+v, %v", inc, err)
	}
	// Nothing new: nothing checked, position kept.
	if again, _ := st.VerifyChainFrom(ctx, inc.Last); again.Checked != 0 || again.Last != inc.Last {
		t.Errorf("no-op = %+v", again)
	}
	// A record whose prev_hash doesn't continue the chain is caught.
	appendN(1)
	dropGuards(t, st)
	st.DB.Exec(`UPDATE raw_log_records SET prev_hash = 'x' WHERE id = 6`)
	if bad, _ := st.VerifyChainFrom(ctx, inc.Last); bad.BadID != 6 {
		t.Errorf("tampered = %+v", bad)
	}
	st.Close()
	if _, err := st.VerifyChainFrom(ctx, ChainPoint{}); err == nil {
		t.Error("want error on closed db")
	}
}

func TestChainHashDistinguishesFields(t *testing.T) {
	if ChainHash("", "ab", "c", "") == ChainHash("", "a", "bc", "") {
		t.Error("field boundaries must be part of the hash")
	}
}
