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

func promptIDs(rows []PromptRow) []int64 {
	out := make([]int64, 0, len(rows))
	for _, p := range rows {
		out = append(out, p.ID)
	}
	return out
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestGetPromptWindow(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	d := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	get := func(req WindowRequest, now time.Time) *PromptWindow {
		t.Helper()
		w, err := GetPromptWindow(ctx, st.DB, req, now)
		if err != nil {
			t.Fatalf("%+v: %v", req, err)
		}
		return w
	}

	// A window that holds everything: newest first, sessions ordered by their newest prompt.
	w := get(WindowRequest{Days: 3, MinPrompts: 1}, d(17, 12))
	if want := []int64{ids["p4"], ids["p3"], ids["p2"], ids["p1"]}; !sameIDs(promptIDs(w.Prompts), want) {
		t.Fatalf("prompts = %v, want %v", promptIDs(w.Prompts), want)
	}
	if w.Matching != 4 || w.More || w.Widened || !w.Oldest.Equal(d(17, 9)) || w.HistoryBegins.IsZero() {
		t.Errorf("matching %d more %v widened %v oldest %v", w.Matching, w.More, w.Widened, w.Oldest)
	}
	if len(w.Sessions) != 2 || w.Sessions[0].ID != "9c1e0000" || w.Sessions[1].ID != "404dabcd" ||
		w.Sessions[1].Prompts != 2 || w.Sessions[1].Flags != 1 || w.Sessions[1].Repo != "nosaka" {
		t.Errorf("sessions = %+v", w.Sessions)
	}
	if len(w.Agents) != 2 || len(w.Repos) != 2 || len(w.Menu) != 2 || w.OlderSessions != 0 {
		t.Errorf("options: %v %v %v older %d", w.Agents, w.Repos, w.Menu, w.OlderSessions)
	}
	// The row values come from the stored columns.
	byID := map[int64]PromptRow{}
	for _, p := range w.Prompts {
		byID[p.ID] = p
	}
	if p := byID[ids["p2"]]; p.Duration != 20*time.Minute || p.Actions != 1 || p.Running || p.Repo != "nosaka" || p.SessionID != "404dabcd" || p.Agent != "claude" {
		t.Errorf("p2 = %+v", p)
	}
	if p := byID[ids["p1"]]; p.Flags != 1 || p.Actions != 1 {
		t.Errorf("p1 = %+v", p)
	}

	// Older than the window, but fewer prompts than MinPrompts: widened to the newest few.
	w = get(WindowRequest{Days: 3, MinPrompts: 2}, d(20, 12))
	if want := []int64{ids["p4"], ids["p3"]}; !sameIDs(promptIDs(w.Prompts), want) || !w.Widened || w.Matching != 2 {
		t.Errorf("widened to 2: %v widened %v matching %d", promptIDs(w.Prompts), w.Widened, w.Matching)
	}
	// ... and when fewer than that exist at all, everything.
	w = get(WindowRequest{Days: 3}, d(20, 12))
	if len(w.Prompts) != 4 || !w.Widened {
		t.Errorf("widened to all: %d prompts, widened %v", len(w.Prompts), w.Widened)
	}
	// Even an empty window yields the newest prompt when the minimum is 1.
	w = get(WindowRequest{Days: 3, MinPrompts: 1}, d(20, 12))
	if want := []int64{ids["p4"]}; !sameIDs(promptIDs(w.Prompts), want) || !w.Widened {
		t.Errorf("empty window with min 1: %v widened %v", promptIDs(w.Prompts), w.Widened)
	}

	// All time, and paging older with Before.
	if w = get(WindowRequest{Days: 0}, d(20, 12)); len(w.Prompts) != 4 || w.Widened {
		t.Errorf("all time: %d prompts widened %v", len(w.Prompts), w.Widened)
	}
	w = get(WindowRequest{Days: 0, Before: d(17, 11)}, d(20, 12))
	if want := []int64{ids["p2"], ids["p1"]}; !sameIDs(promptIDs(w.Prompts), want) || w.Matching != 2 {
		t.Errorf("before 11:00: %v matching %d", promptIDs(w.Prompts), w.Matching)
	}

	// The limit cuts the list, not the count; More says so.
	w = get(WindowRequest{Days: 0, Limit: 2}, d(20, 12))
	if len(w.Prompts) != 2 || w.Matching != 4 || !w.More || !w.Oldest.Equal(d(17, 11)) {
		t.Errorf("limit 2: %d prompts matching %d more %v oldest %v", len(w.Prompts), w.Matching, w.More, w.Oldest)
	}

	// One session: all its prompts, whatever the window says.
	w = get(WindowRequest{Days: 3, SessionID: "404dabcd"}, d(25, 12))
	if want := []int64{ids["p2"], ids["p1"]}; !sameIDs(promptIDs(w.Prompts), want) || w.Widened || len(w.Sessions) != 1 || w.Sessions[0].ID != "404dabcd" {
		t.Errorf("session: %v widened %v sessions %+v", promptIDs(w.Prompts), w.Widened, w.Sessions)
	}
	if w = get(WindowRequest{SessionID: "nope"}, d(17, 12)); len(w.Prompts) != 0 || w.Matching != 0 {
		t.Errorf("unknown session: %+v", w)
	}
}

func TestGetSessionSummary(t *testing.T) {
	ctx := context.Background()
	st, _ := seedPrompts(t)
	s, err := GetSessionSummary(ctx, st.DB, "404dabcd")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "404dabcd" || s.Agent != "claude" || s.Repo != "nosaka" || s.Prompts != 2 || s.Actions != 2 || s.Flags != 1 ||
		s.Started.Hour() != 9 || s.LastActivity.IsZero() {
		t.Errorf("summary = %+v", s)
	}
	if _, err := GetSessionSummary(ctx, st.DB, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSessionDetailLoadsOnlyTheNewestPrompts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "long", Cwd: "/w/r"})
	n := SessionPromptPreview + 3
	for i := 1; i <= n; i++ {
		ts := time.Date(2026, 10, 17, 9, i, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000000000Z")
		pid, _ := tx.AddPrompt(ctx, sid, "p", ts)
		tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: pid, TS: ts, Resource: "file", Action: "read", Path: "/w/r/a", Evidence: "log", ToolUseID: "t" + ts})
		if i == n {
			tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: pid, TS: ts, Resource: "subagent", Action: "start", Path: "Explore", Summary: "look", Evidence: "log", ToolUseID: "sub"})
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	d, err := GetSessionDetail(ctx, st.DB, "long", time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Prompts) != SessionPromptPreview || d.TotalPrompts != int64(n) || d.Actions != int64(n) {
		t.Errorf("loaded %d of %d prompts, %d actions", len(d.Prompts), d.TotalPrompts, d.Actions)
	}
	if d.Prompts[0].Started.Minute() != n || d.Prompts[SessionPromptPreview-1].Started.Minute() != n-SessionPromptPreview+1 {
		t.Errorf("not the newest prompts, newest first: %v … %v", d.Prompts[0].Started, d.Prompts[len(d.Prompts)-1].Started)
	}
	if d.Activity[0].Count != int64(n) {
		t.Errorf("prompt tile counts %d, want the session's %d", d.Activity[0].Count, n)
	}
	if len(d.Subagents) != 1 || d.Subagents[0].PromptNumber != int64(n) {
		t.Errorf("subagent = %+v, want prompt number %d", d.Subagents, n)
	}
}

// The store counts a prompt's system actions as it ingests (IsSystemAction);
// the queries describe the same rule in SQL (systemAction). They must agree.
func TestStoredActionCountMatchesTheSQLRule(t *testing.T) {
	ctx := context.Background()
	st, ids := seedPrompts(t)
	tx, _ := st.Begin(ctx)
	sid := ids["s1"]
	for i, ra := range [][2]string{
		{"file", "update"}, {"file", "delete"}, {"dir", "create"}, {"command", "execute"}, {"process", "kill"},
		{"network", "connect"}, {"git", "force-push"}, {"launch item", "add"}, {"web", "fetch"}, {"web", "search"},
		{"subagent", "start"}, {"prompt", "request"}, {"monitor", "gap"},
	} {
		if err := tx.AddAction(ctx, store.Action{SessionID: sid, PromptID: ids["p1"], TS: "2026-10-17T09:30:00.000000000Z",
			Resource: ra[0], Action: ra[1], Evidence: "log", ToolUseID: "rule" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := st.DB.Query(`SELECT p.id, p.action_count, (SELECT COUNT(*) FROM actions a WHERE a.prompt_id = p.id AND ` + systemAction + `) FROM prompts p`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, stored, derived int64
		if err := rows.Scan(&id, &stored, &derived); err != nil {
			t.Fatal(err)
		}
		if stored != derived {
			t.Errorf("prompt %d: stored action_count %d, SQL rule counts %d", id, stored, derived)
		}
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
	if len(d.Prompts) != 2 || d.TotalPrompts != 2 || d.Prompts[0].ID != ids["p2"] || d.Prompts[1].ID != ids["p1"] {
		t.Fatalf("prompts (newest first) = %+v, total %d", d.Prompts, d.TotalPrompts)
	}
	if d.FirstPrompt != "Fix the Snooze crash" {
		t.Errorf("FirstPrompt = %q", d.FirstPrompt)
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
