package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// agentsFixture: a running agent "root" (started 08:00) that ran git status at
// 09:00, 09:01, 09:02, claude -c at 09:00:30 and git config --get at 09:03,
// plus one command nothing classified; a stopped agent "old"; and a brief run
// of the binary that is not an agent.
func agentsFixture(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }

	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s1", Cwd: "/w/app", RepoRoot: "/w/app", TS: ts("08:00:00")})
	oid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s0", Cwd: "/w/old", TS: ts("06:00:00")})
	add := func(exec, parent string, sess int64, bin, argv, at string) {
		if err := tx.AddProcess(ctx, store.Process{ExecID: exec, Collector: "tetragon", PID: int64(len(exec)) + 10, ParentExecID: parent, Binary: bin, Argv: argv, Cwd: "/w/app", StartedAt: ts(at)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, exec, sess, 0); err != nil {
			t.Fatal(err)
		}
	}
	add("root", "", sid, "/x/claude/versions/2.1.9", `["-c"]`, "08:00:00")
	for exec, c := range map[string]struct{ argv, at, key string }{
		"g1": {`["status"]`, "09:00:00", "git status"},
		"c1": {`["-c"]`, "09:00:30", "claude -c"},
		"g2": {`["status"]`, "09:01:00", "git status"},
		"g3": {`["status"]`, "09:02:00", "git status"},
		"k1": {`["config","--get","remote.origin.url"]`, "09:03:00", "git config --get"},
		"u1": {`["-u"]`, "09:04:00", ""},
	} {
		bin := "/usr/bin/git"
		if c.key == "claude -c" {
			bin = "/x/claude/versions/2.1.9"
		}
		if exec == "u1" {
			bin = "/usr/bin/id"
		}
		add(exec, "root", sid, bin, c.argv, c.at)
		if err := tx.SetBackground(ctx, exec, c.key, 1); err != nil {
			t.Fatal(err)
		}
	}
	add("old", "", oid, "/x/claude/versions/2.1.8", `[]`, "06:00:00")
	add("brief", "", oid, "/x/claude/versions/2.1.9", `["--version"]`, "07:00:00")                                             // exits at once
	add("rg", "", oid, "/x/claude/versions/2.1.9", `["-G","--ignore-files","--hidden","-I","--exclude-dir=.git"]`, "07:20:00") // the Grep tool, still running
	if _, err := tx.ExitProcess(ctx, "old", ts("07:30:00"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExitProcess(ctx, "brief", ts("07:00:00"), 0); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestListAgents(t *testing.T) {
	st := agentsFixture(t)
	rows, err := ListAgents(context.Background(), st.DB)
	if err != nil {
		t.Fatal(err)
	}
	// newest first; the brief run of the binary is not an agent
	if len(rows) != 2 || rows[0].ID != "root" || rows[1].ID != "old" {
		t.Fatalf("agents = %+v", rows)
	}
	a := rows[0]
	if a.Agent != "claude" || a.Repo != "app" || a.Version != "2.1.9" || !a.Running || !a.Stopped.IsZero() || a.Background != 5 {
		t.Errorf("running agent = %+v", a)
	}
	o := rows[1]
	if o.Running || o.Stopped.IsZero() || o.Background != 0 || o.Repo != "old" {
		t.Errorf("stopped agent = %+v", o)
	}
}

func TestGetAgent(t *testing.T) {
	st := agentsFixture(t)
	d, err := GetAgent(context.Background(), st.DB, "root")
	if err != nil {
		t.Fatal(err)
	}
	if d.Dir != "/w/app" || len(d.Sessions) != 1 || d.Sessions[0].ID != "s1" || d.Background != 5 {
		t.Errorf("detail = %+v", d)
	}
	// groups: most runs first, then by name
	if len(d.Groups) != 3 || d.Groups[0].Key != "git status" || d.Groups[0].Count != 3 || d.Groups[1].Key != "claude -c" || d.Groups[2].Key != "git config --get" {
		t.Fatalf("groups = %+v", d.Groups)
	}
	g := d.Groups[0]
	if !g.First.Before(g.Last) || len(g.Latest) != 3 || !g.Latest[0].At.After(g.Latest[1].At) || g.Latest[0].Command != "git status" {
		t.Errorf("git status group = %+v", g)
	}
	if got := d.Groups[1].Latest[0].Command; got != "2.1.9 -c" {
		t.Errorf("claude -c command = %q", got)
	}

	if _, err := GetAgent(context.Background(), st.DB, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: %v", err)
	}
}

func TestAgentTimeline(t *testing.T) {
	st := agentsFixture(t)
	ctx := context.Background()
	at := func(s string) time.Time { return time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC).Add(parse(t, s)) }

	// no window: newest first
	tl, err := AgentTimeline(ctx, st.DB, "root", TimelineFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Total != 5 || len(tl.Runs) != 5 || tl.Runs[0].Key != "git config --get" || tl.Runs[4].Key != "git status" {
		t.Fatalf("timeline = %+v", tl)
	}
	// a text filter is case-insensitive and matches the whole command line
	tl, _ = AgentTimeline(ctx, st.DB, "root", TimelineFilter{Text: "CONFIG --get"})
	if tl.Total != 1 || tl.Runs[0].Key != "git config --get" {
		t.Errorf("filtered = %+v", tl)
	}
	// a window reads oldest first, and pages
	f := TimelineFilter{From: at("0s"), To: at("2m"), Limit: 2}
	tl, _ = AgentTimeline(ctx, st.DB, "root", f)
	if tl.Total != 4 || len(tl.Runs) != 2 || !tl.Runs[0].At.Before(tl.Runs[1].At) || tl.Runs[0].Key != "git status" {
		t.Errorf("window page 1 = %+v", tl)
	}
	f.Offset = 2
	tl, _ = AgentTimeline(ctx, st.DB, "root", f)
	if len(tl.Runs) != 2 || tl.Runs[1].Key != "git status" || tl.Runs[0].Key != "claude -c" && tl.Runs[0].Key != "git status" {
		t.Errorf("window page 2 = %+v", tl)
	}
	// nothing matches
	tl, _ = AgentTimeline(ctx, st.DB, "root", TimelineFilter{Text: "zzz"})
	if tl.Total != 0 || len(tl.Runs) != 0 {
		t.Errorf("no match = %+v", tl)
	}

	if _, err := AgentTimeline(ctx, st.DB, "nope", TimelineFilter{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown agent: %v", err)
	}
}

func parse(t *testing.T, s string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAgentQueriesReportStoreErrors(t *testing.T) {
	st := agentsFixture(t)
	ctx := context.Background()
	st.DB.Exec(`DROP TABLE process_attribution`)
	if _, err := ListAgents(ctx, st.DB); err == nil {
		t.Error("ListAgents")
	}
	if _, err := GetAgent(ctx, st.DB, "root"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetAgent: %v", err)
	}
	if _, err := AgentTimeline(ctx, st.DB, "root", TimelineFilter{}); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("AgentTimeline: %v", err)
	}
}
