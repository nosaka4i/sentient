package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

func TestGetStatusEmpty(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := GetStatus(context.Background(), st.DB, time.Unix(0, 0), time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !s.HistoryBegins.IsZero() || s.UnknownRecords != 0 {
		t.Errorf("empty status = %+v", s)
	}
	// Exactly the 11 summary rows, even at zero.
	if len(s.Activity) != 11 || len(StatusRows) != 11 {
		t.Fatalf("rows = %d, want 11", len(s.Activity))
	}
	for i, r := range s.Activity {
		if r.Resource != StatusRows[i][0] || r.Action != StatusRows[i][1] || r.Count != 0 || r.Summary != "" {
			t.Errorf("row %d = %+v", i, r)
		}
	}
}

func TestGetStatus(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tx, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	s1, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "a", Cwd: "/w/proj/sub", RepoRoot: "/w/proj", TS: ts("09:00:00")})
	s2, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "b", Cwd: "/w/other", TS: ts("09:00:00")})
	s3, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "c", TS: ts("09:00:00")})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: "2026-10-16T08:00:00.000000000Z", SourceFile: "f", Type: "user", Record: "{}"})
	tx.AppendRaw(ctx, store.RawRecord{SourceFile: "f", Type: "", Unknown: true, Record: "{bad"})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, SourceFile: "f", Type: "mystery", Unknown: true, Record: "{}"})

	tx.AddPrompt(ctx, s1, "p1", ts("09:00:00"))
	tx.AddPrompt(ctx, s1, "p2", ts("10:00:00"))
	tx.AddPrompt(ctx, s2, "p3", ts("11:00:00"))
	tx.AddPrompt(ctx, s3, "p4", ts("11:30:00"))
	tx.AddPrompt(ctx, s1, "yesterday", "2026-10-16T09:00:00.000000000Z")

	n := 0
	add := func(sid int64, at, res, act, path string) string {
		n++
		id := "tu" + string(rune('a'+n))
		if err := tx.AddAction(ctx, store.Action{SessionID: sid, TS: ts(at), Resource: res, Action: act, Path: path, Evidence: "log", ToolUseID: id}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	add(s1, "09:01:00", "file", "read", "/w/proj/a.go")
	add(s1, "09:02:00", "file", "read", "/etc/hosts")
	add(s1, "09:02:30", "file", "read", "/w/project2/x") // sibling with shared prefix is outside
	add(s2, "09:03:00", "file", "read", "/anything")     // no repo root: not counted as outside
	upd := add(s1, "09:04:00", "file", "update", "/w/proj/a.go")
	tx.ApplyToolResult(ctx, store.ToolResult{ToolUseID: upd, LinesAdded: 248, LinesRemoved: 61})
	add(s1, "09:05:00", "file", "update", "/w/proj/b.go") // no line counts
	bad := add(s1, "09:06:00", "command", "execute", "false")
	tx.ApplyToolResult(ctx, store.ToolResult{ToolUseID: bad, Failed: true})
	add(s1, "09:06:30", "command", "execute", "true")
	for _, u := range []string{"https://github.com/x", "https://github.com/y", "https://pkg.go.dev/z", "https://a.dev/", "https://b.dev/", "not a url"} {
		add(s1, "09:07:00", "web", "fetch", u)
	}
	add(s1, "09:08:00", "web", "search", "q")
	add(s1, "09:09:00", "subagent", "start", "Explore")
	// Folded into summary rows.
	add(s1, "09:09:10", "file", "rename", "/w/proj/c.go") // -> file update
	add(s1, "09:09:20", "dir", "create", "/w/proj/d")     // -> file create
	add(s1, "09:09:30", "dir", "delete", "/w/proj/e")     // -> file delete
	add(s1, "09:09:40", "git", "commit", "")
	add(s1, "09:09:50", "git", "push", "") // -> git commit
	add(s1, "09:09:55", "process", "inspect", "ps")
	add(s1, "09:09:56", "network", "connect", "github.com")
	// Not rows: risky actions alert instead; off-vocabulary is dropped.
	for _, ra := range [][2]string{{"git", "force-push"}, {"git", "reset"}, {"file", "chmod"},
		{"process", "kill"}, {"launch item", "add"}, {"monitor", "gap"}, {"network", "listen"}, {"zeta", "poke"}} {
		add(s1, "09:10:00", ra[0], ra[1], "")
	}
	add(s1, "09:11:00", "file", "delete", "/x")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Move the delete to yesterday so it falls outside the window.
	st.DB.Exec(`UPDATE actions SET ts = '2026-10-16T09:00:00.000000000Z' WHERE resource = 'file' AND action = 'delete'`)

	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	s, err := GetStatus(ctx, st.DB, from, to, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.PromptsTotal != 4 || len(s.Prompts) != 4 || s.Prompts[0].Text != "p4" {
		t.Errorf("prompts = %d %+v", s.PromptsTotal, s.Prompts)
	}
	if want := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC); !s.HistoryBegins.Equal(want) {
		t.Errorf("HistoryBegins = %v, want %v", s.HistoryBegins, want)
	}
	if s.UnknownRecords != 2 {
		t.Errorf("UnknownRecords = %d, want 2", s.UnknownRecords)
	}
	r := func(res, act string, n int64, sum string) ActivityRow {
		return ActivityRow{Resource: res, Action: act, Count: n, Summary: sum}
	}
	prompt := r("prompt", "request", 4, "2 repos")
	prompt.Repos = 2
	read := r("file", "read", 4, "2 outside repo")
	read.Outside = 2
	update := r("file", "update", 3, "+248 −61")
	update.Added, update.Removed = 248, 61
	cmd := r("command", "execute", 2, "1 failed")
	cmd.Failed = 1
	web := r("web", "search", 7, "a.dev, b.dev, github.com +1 more") // 1 search + 6 fetches
	web.Hosts = web.Summary
	want := []ActivityRow{
		prompt,
		read,
		r("file", "create", 1, ""),
		update,
		r("file", "delete", 1, ""), // the dir delete; the file delete is yesterday's
		cmd,
		r("process", "inspect", 1, ""),
		r("network", "connect", 1, ""),
		r("git", "commit", 2, ""),
		web,
		r("subagent", "start", 1, ""),
	}
	if len(s.Activity) != len(want) {
		t.Fatalf("Activity = %+v\nwant %+v", s.Activity, want)
	}
	for i := range want {
		if s.Activity[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, s.Activity[i], want[i])
		}
	}
}

func TestPromptActionCounts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s"})
	p1, _ := tx.AddPrompt(ctx, sid, "one", "2026-10-17T09:00:00.000000000Z")
	p2, _ := tx.AddPrompt(ctx, sid, "two", "2026-10-17T10:00:00.000000000Z")
	old, _ := tx.AddPrompt(ctx, sid, "old", "2026-10-15T10:00:00.000000000Z")
	// p1: a 3-part command line plus a file read = 4 actions.
	for part, path := range []string{"cd x", "ls", "sort"} {
		tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: p1, TS: "2026-10-17T09:01:00.000000000Z",
			Resource: "command", Action: "execute", Path: path, Evidence: "log", ToolUseID: "t1", Part: part})
	}
	tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: p1, TS: "2026-10-17T09:02:00.000000000Z",
		Resource: "file", Action: "read", Path: "/a", Evidence: "log", ToolUseID: "t2"})
	tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: old, TS: "2026-10-15T10:01:00.000000000Z",
		Resource: "file", Action: "read", Path: "/b", Evidence: "log", ToolUseID: "t3"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	got, err := PromptActionCounts(ctx, st.DB, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[p1] != 4 || got[p2] != 0 {
		t.Errorf("PromptActionCounts = %v, want {%d:4 %d:0}", got, p1, p2)
	}

	// The chained command also counts 3 in LAST 24H's command execute.
	s, err := GetStatus(ctx, st.DB, from, from.Add(24*time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Activity[5].Resource != "command" || s.Activity[5].Count != 3 {
		t.Errorf("command execute row = %+v", s.Activity[5])
	}

	st.Close()
	if _, err := PromptActionCounts(ctx, st.DB, from, from); err == nil {
		t.Error("want error on closed db")
	}
}

func TestHelpers(t *testing.T) {
	if plural(1, "repo") != "1 repo" || plural(3, "repo") != "3 repos" {
		t.Error("plural")
	}
	if joinLimited([]string{"a", "b"}, 3) != "a, b" {
		t.Error("joinLimited short")
	}
	if !within("/a/b/c", "/a/b") || within("/a/bc", "/a/b") || within("/x", "/a") || !within("/a/b", "/a/b") {
		t.Error("within")
	}
}
