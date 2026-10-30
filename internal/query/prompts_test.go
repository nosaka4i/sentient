package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

func TestRecentPrompts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	tx, _ := st.Begin(ctx)
	s1, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "404dabcd", Cwd: "/w/nosaka/ios", RepoRoot: "/w/nosaka"})
	s2, _ := tx.UpsertSession(ctx, store.Session{Agent: "codex", AgentSessionID: "9c1e0000", Cwd: "/w/git-backlog"})
	p1, _ := tx.AddPrompt(ctx, s1, "first", ts("09:00:00"))
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts("09:00:00"), SourceFile: "f", Type: "user", Record: "{}"})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts("09:36:00"), SourceFile: "f", Type: "assistant", Record: "{}"})
	p2, _ := tx.AddPrompt(ctx, s1, "second", ts("10:00:00")) // ends p1 at 10:00
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts("10:06:00"), SourceFile: "f", Type: "assistant", Record: "{}"})
	p3, _ := tx.AddPrompt(ctx, s2, "third", ts("11:50:00")) // latest in its session, active recently: running
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s2, TS: ts("11:58:00"), SourceFile: "f", Type: "assistant", Record: "{}"})
	s3, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "old"})
	tx.AddPrompt(ctx, s3, "yesterday", "2026-10-16T09:00:00.000000000Z") // outside the window
	for i, path := range []string{"cd x", "ls", "sort"} {
		tx.AddAction(ctx, store.Action{SessionID: s1, PromptID: p1, TS: ts("09:01:00"), Resource: "command", Action: "execute", Path: path, Evidence: "log", ToolUseID: "t1", Part: i})
	}
	tx.AddAction(ctx, store.Action{SessionID: s2, PromptID: p3, TS: ts("11:51:00"), Resource: "file", Action: "read", Path: "/a", Evidence: "log", ToolUseID: "t2"})
	// Not actions on the system: not in ACTIONS.
	tx.AddAction(ctx, store.Action{SessionID: s2, PromptID: p3, TS: ts("11:52:00"), Resource: "subagent", Action: "start", Evidence: "log", ToolUseID: "t4"})
	tx.AddAction(ctx, store.Action{SessionID: s2, PromptID: p3, TS: ts("11:52:00"), Resource: "web", Action: "search", Evidence: "log", ToolUseID: "t5"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var actionID int64
	st.DB.QueryRow(`SELECT id FROM actions WHERE tool_use_id = 't2'`).Scan(&actionID)
	st.DB.Exec(`INSERT INTO alerts (action_id, ts, severity, rule) VALUES (?, ?, 'high', 'test')`, actionID, ts("11:51:00"))
	st.DB.Exec(`INSERT INTO alerts (action_id, ts, severity, rule, status) VALUES (?, ?, 'low', 'test', 'reviewed')`, actionID, ts("11:51:00"))

	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	total, rows, err := RecentPrompts(ctx, st.DB, from, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(rows) != 2 {
		t.Fatalf("total %d rows %d, want 3 and 2 (limit)", total, len(rows))
	}
	r0, r1 := rows[0], rows[1]
	if r0.ID != p3 || !r0.Running || r0.Duration != 8*time.Minute || r0.Agent != "codex" || r0.Repo != "git-backlog" ||
		r0.SessionID != "9c1e0000" || r0.Actions != 1 || r0.Flags != 1 {
		t.Errorf("row 0 = %+v", r0)
	}
	// p2 is its session's latest prompt but went quiet at 10:06: not running.
	if r1.ID != p2 || r1.Running || r1.Duration != 6*time.Minute || r1.Repo != "nosaka" || r1.Actions != 0 {
		t.Errorf("row 1 = %+v", r1)
	}

	_, rows, err = RecentPrompts(ctx, st.DB, from, now, 5)
	if err != nil {
		t.Fatal(err)
	}
	if p := rows[2]; p.ID != p1 || p.Running || p.Duration != 36*time.Minute || p.Actions != 3 {
		t.Errorf("first prompt = %+v (activity after the next prompt starts must not count)", p)
	}

	st.Close()
	if _, _, err := RecentPrompts(ctx, st.DB, from, now, 5); err == nil {
		t.Error("want error on closed db")
	}
}

func TestPromptLine(t *testing.T) {
	cases := []struct {
		in     string
		pasted int
		line   string
	}{
		{"add a --json flag to list", 0, "add a --json flag to list"},
		{"  run   the tests\n\nand commit", 0, "run the tests"},
		{"<pasted_content>\na\nb\nc\n</pasted_content>\nthis crash log is from the reminder screen", 3, "this crash log is from the reminder screen"},
		{"tunnel is spewing:\n\n<pasted_content id=\"057e\">x\ny</pasted_content>", 2, "tunnel is spewing:"},
		{"<pasted_content>only</pasted_content>", 1, ""},
		{"<pasted_content id=\"057e\">a\nb</pasted_content id=\"057e\">\nwhy", 2, "why"}, // the id repeats on the closing tag
		{"<pasted_content></pasted_content>hi", 0, "hi"},
		{"", 0, ""},
	}
	for _, c := range cases {
		p, l := PromptLine(c.in)
		if p != c.pasted || l != c.line {
			t.Errorf("PromptLine(%q) = %d %q, want %d %q", c.in, p, l, c.pasted, c.line)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := Ellipsis("make the iOS build use the staging server", 20); got != "make the iOS build…" {
		t.Errorf("Ellipsis = %q", got)
	}
	if got := Ellipsis("short", 20); got != "short" {
		t.Errorf("Ellipsis short = %q", got)
	}
	if got := Ellipsis("ééééé", 3); got != "éé…" {
		t.Errorf("Ellipsis runes = %q", got)
	}
	for d, want := range map[time.Duration]string{
		20 * time.Second: "<1m", 12 * time.Minute: "12m", 2 * time.Hour: "2h", 65 * time.Minute: "1h 5m",
	} {
		if got := ShortDuration(d); got != want {
			t.Errorf("ShortDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if ShortSession("404dabcd") != "404d" || ShortSession("ab") != "ab" {
		t.Error("ShortSession")
	}
}
