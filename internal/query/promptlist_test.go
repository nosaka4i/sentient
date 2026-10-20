package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// seedPrompts builds two sessions (claude in /w/nosaka, codex in /w/a_b) with
// four prompts, a subagent start, and one open alert on the first prompt.
func seedPrompts(t *testing.T) (*store.Store, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := func(s string) string { return "2026-10-17T" + s + ".000000000Z" }
	tx, _ := st.Begin(ctx)
	s1, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "404dabcd", Cwd: "/w/nosaka/ios", RepoRoot: "/w/nosaka", TS: ts("09:00:00"), AgentVersion: "2.1.3"})
	s2, _ := tx.UpsertSession(ctx, store.Session{Agent: "codex", AgentSessionID: "9c1e0000", Cwd: "/w/a_b", TS: ts("11:00:00")})
	ids := map[string]int64{"s1": s1, "s2": s2}
	add := func(name string, sid int64, text, at string) {
		id, err := tx.AddPrompt(ctx, sid, text, ts(at))
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	add("p1", s1, "Fix the Snooze crash", "09:00:00")
	add("p2", s1, "run the tests", "10:00:00")
	add("p3", s2, "add a --json flag", "11:00:00")
	add("p4", s2, "100% done_ish?", "11:30:00")
	tx.AppendRaw(ctx, store.RawRecord{SessionID: s1, TS: ts("10:20:00"), SourceFile: "f", Type: "assistant", Record: "{}"})
	tx.AddAction(ctx, store.Action{SessionID: s1, PromptID: ids["p1"], TS: ts("09:05:00"), Resource: "file", Action: "update", Path: "/w/nosaka/a.go", Evidence: "log", ToolUseID: "t1"})
	tx.AddAction(ctx, store.Action{SessionID: s1, PromptID: ids["p1"], TS: ts("09:06:00"), Resource: "subagent", Action: "start", Path: "Explore", Summary: "find the crash", Evidence: "log", ToolUseID: "t2"})
	tx.AddAction(ctx, store.Action{SessionID: s1, PromptID: ids["p2"], TS: ts("10:05:00"), Resource: "command", Action: "execute", Path: "go test", Evidence: "log", ToolUseID: "t3"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var actionID int64
	st.DB.QueryRow(`SELECT id FROM actions WHERE tool_use_id = 't1'`).Scan(&actionID)
	st.DB.Exec(`INSERT INTO alerts (action_id, ts, severity, rule) VALUES (?, ?, 'high', 'test')`, actionID, ts("09:05:00"))
	return st, ids
}

func TestListPrompts(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	all, err := ListPrompts(ctx, st.DB, PromptFilter{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 4 || len(all.Prompts) != 4 || all.Prompts[0].ID != ids["p4"] || all.Prompts[3].ID != ids["p1"] {
		t.Fatalf("all: total %d, %d rows, first %d", all.Total, len(all.Prompts), all.Prompts[0].ID)
	}
	if len(all.Agents) != 2 || all.Agents[0] != "claude" || len(all.Repos) != 2 || all.Repos[0] != "a_b" || all.Repos[1] != "nosaka" {
		t.Errorf("options: agents %v repos %v", all.Agents, all.Repos)
	}
	if all.HistoryBegins.IsZero() {
		t.Error("HistoryBegins not set")
	}

	for _, tc := range []struct {
		name string
		f    PromptFilter
		want []int64
	}{
		{"text, case-insensitive", PromptFilter{Text: "snooze"}, []int64{ids["p1"]}},
		{"text with a percent is literal", PromptFilter{Text: "100%"}, []int64{ids["p4"]}},
		{"agent", PromptFilter{Agent: "codex"}, []int64{ids["p4"], ids["p3"]}},
		{"repo", PromptFilter{Repo: "nosaka"}, []int64{ids["p2"], ids["p1"]}},
		{"repo with an underscore is literal", PromptFilter{Repo: "a_b"}, []int64{ids["p4"], ids["p3"]}},
		{"repo that is only a suffix doesn't match", PromptFilter{Repo: "saka"}, nil},
		{"flagged", PromptFilter{Flagged: true}, []int64{ids["p1"]}},
		{"combined", PromptFilter{Agent: "claude", Text: "tests"}, []int64{ids["p2"]}},
		{"no match", PromptFilter{Text: "zzz"}, nil},
	} {
		l, err := ListPrompts(ctx, st.DB, tc.f, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got []int64
		for _, p := range l.Prompts {
			got = append(got, p.ID)
		}
		if len(got) != len(tc.want) || int(l.Total) != len(tc.want) {
			t.Errorf("%s: got %v (total %d), want %v", tc.name, got, l.Total, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}

	// The limit cuts the rows but not the total.
	l, _ := ListPrompts(ctx, st.DB, PromptFilter{Limit: 2}, now)
	if l.Total != 4 || len(l.Prompts) != 2 {
		t.Errorf("limit: total %d rows %d", l.Total, len(l.Prompts))
	}
}

func TestListPromptsOneSession(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	l, err := ListPrompts(ctx, st.DB, PromptFilter{SessionID: "404dabcd"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 2 || len(l.Prompts) != 2 || l.Prompts[0].ID != ids["p2"] {
		t.Errorf("session prompts: total %d rows %+v", l.Total, l.Prompts)
	}
	s := l.Session
	if s == nil || s.ID != "404dabcd" || s.Agent != "claude" || s.Repo != "nosaka" || s.Prompts != 2 || s.Actions != 2 || s.Flags != 1 ||
		s.Started.Hour() != 9 || s.LastActivity.IsZero() {
		t.Errorf("summary = %+v", s)
	}
	if len(l.Sessions) != 2 || l.Sessions[0].ID != "9c1e0000" || l.Sessions[0].Repo != "a_b" || l.OlderSessions != 0 {
		t.Errorf("menu = %+v older %d", l.Sessions, l.OlderSessions)
	}

	// An unknown session matches nothing and has no summary.
	l, err = ListPrompts(ctx, st.DB, PromptFilter{SessionID: "nope"}, now)
	if err != nil || l.Total != 0 || l.Session != nil {
		t.Errorf("unknown session: %+v, %v", l, err)
	}
	if _, err := GetSessionSummary(ctx, st.DB, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSessionSummary: err = %v, want ErrNotFound", err)
	}
}

func TestListSessionGroups(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	g, err := ListSessionGroups(ctx, st.DB, PromptFilter{}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if g.TotalPrompts != 4 || g.TotalSessions != 2 || len(g.Groups) != 2 || g.HistoryBegins.IsZero() {
		t.Fatalf("groups: %d prompts %d sessions %d groups", g.TotalPrompts, g.TotalSessions, len(g.Groups))
	}
	// Newest session first, newest prompt first within it.
	a, b := g.Groups[0], g.Groups[1]
	if a.Session.ID != "9c1e0000" || a.Matching != 2 || a.Session.Prompts != 2 || len(a.Prompts) != 2 || a.Prompts[0].ID != ids["p4"] {
		t.Errorf("first group = %+v", a)
	}
	if b.Session.ID != "404dabcd" || b.Session.Repo != "nosaka" || b.Session.Flags != 1 || b.Prompts[0].ID != ids["p2"] {
		t.Errorf("second group = %+v", b)
	}
	if len(g.Agents) != 2 || len(g.Repos) != 2 || len(g.Sessions) != 2 {
		t.Errorf("options: %v %v %v", g.Agents, g.Repos, g.Sessions)
	}

	// Each session shows only its newest prompt(s); the match count stays whole.
	g, _ = ListSessionGroups(ctx, st.DB, PromptFilter{}, 1, now)
	if len(g.Groups[0].Prompts) != 1 || g.Groups[0].Matching != 2 || g.Groups[0].Prompts[0].ID != ids["p4"] {
		t.Errorf("per-session cap: %+v", g.Groups[0])
	}

	for _, tc := range []struct {
		name          string
		f             PromptFilter
		sessions      int
		prompts       int64
		firstSession  string
		firstMatching int64
	}{
		{"text", PromptFilter{Text: "snooze"}, 1, 1, "404dabcd", 1},
		{"agent", PromptFilter{Agent: "codex"}, 1, 2, "9c1e0000", 2},
		{"repo", PromptFilter{Repo: "nosaka"}, 1, 2, "404dabcd", 2},
		{"flagged", PromptFilter{Flagged: true}, 1, 1, "404dabcd", 1},
		{"session id is ignored", PromptFilter{SessionID: "404dabcd"}, 2, 4, "9c1e0000", 2},
	} {
		g, err := ListSessionGroups(ctx, st.DB, tc.f, 0, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(g.Groups) != tc.sessions || g.TotalPrompts != tc.prompts || g.Groups[0].Session.ID != tc.firstSession || g.Groups[0].Matching != tc.firstMatching {
			t.Errorf("%s: %d groups, %d prompts, first %s (%d)", tc.name, len(g.Groups), g.TotalPrompts, g.Groups[0].Session.ID, g.Groups[0].Matching)
		}
	}

	// The session limit cuts the groups, not the totals.
	g, _ = ListSessionGroups(ctx, st.DB, PromptFilter{Limit: 1}, 0, now)
	if len(g.Groups) != 1 || g.TotalSessions != 2 {
		t.Errorf("limit: %d groups of %d", len(g.Groups), g.TotalSessions)
	}
	g, _ = ListSessionGroups(ctx, st.DB, PromptFilter{Text: "zzz"}, 0, now)
	if len(g.Groups) != 0 || g.TotalPrompts != 0 {
		t.Errorf("no match: %+v", g)
	}
}

func TestSessionMenuIsCapped(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tx, _ := st.Begin(ctx)
	for i := 0; i < SessionMenuSize+2; i++ {
		ts := time.Date(2026, 10, 17, 9, i, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000000000Z")
		sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s" + time.Duration(i).String(), Cwd: "/w/r", TS: ts})
		tx.AddPrompt(ctx, sid, "p", ts)
	}
	tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "no-prompts"}) // metadata only: not offered
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	opts, older, err := sessionOptions(ctx, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != SessionMenuSize || older != 2 {
		t.Errorf("menu %d older %d, want %d and 2", len(opts), older, SessionMenuSize)
	}
}

func TestGetSessionDetail(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	d, err := GetSessionDetail(ctx, st.DB, "404dabcd", now)
	if err != nil {
		t.Fatal(err)
	}
	if d.Agent != "claude" || d.Repo != "nosaka" || d.Cwd != "/w/nosaka/ios" || d.AgentVersion != "2.1.3" || d.ID != "404dabcd" {
		t.Errorf("session = %+v", d)
	}
	if len(d.Prompts) != 2 || d.Prompts[0].ID != ids["p1"] || d.Prompts[1].ID != ids["p2"] {
		t.Fatalf("prompts (oldest first) = %+v", d.Prompts)
	}
	if d.Actions != 2 || d.Flags != 1 || d.Running {
		t.Errorf("actions %d flags %d running %v", d.Actions, d.Flags, d.Running)
	}
	if d.Started.Hour() != 9 || d.LastActivity.IsZero() {
		t.Errorf("started %v last %v", d.Started, d.LastActivity)
	}
	byKey := map[string]ActivityRow{}
	for _, r := range d.Activity {
		byKey[r.Resource+" "+r.Action] = r
	}
	if len(d.Activity) != len(StatusRows) || byKey["prompt request"].Count != 2 || byKey["file update"].Count != 1 ||
		byKey["command execute"].Count != 1 || byKey["subagent start"].Count != 1 {
		t.Errorf("activity = %+v", d.Activity)
	}
	if len(d.Subagents) != 1 || d.Subagents[0].Kind != "Explore" || d.Subagents[0].Task != "find the crash" || d.Subagents[0].PromptNumber != 1 {
		t.Errorf("subagents = %+v", d.Subagents)
	}

	// A session without a repo root falls back to its working directory.
	d2, err := GetSessionDetail(ctx, st.DB, "9c1e0000", now)
	if err != nil || d2.Repo != "a_b" || len(d2.Subagents) != 0 {
		t.Errorf("second session = %+v, %v", d2, err)
	}

	if _, err := GetSessionDetail(ctx, st.DB, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session: err = %v, want ErrNotFound", err)
	}
}
