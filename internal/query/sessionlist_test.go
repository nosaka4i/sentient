package query

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
)

func TestListSessions(t *testing.T) {
	st, ids := seedPrompts(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	l, err := ListSessions(ctx, st.DB, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Total != 2 || len(l.Sessions) != 2 {
		t.Fatalf("total %d, %d sessions", l.Total, len(l.Sessions))
	}
	// the codex session started later, so it is the most recently active
	b, a := l.Sessions[0], l.Sessions[1]
	if b.ID != "9c1e0000" || b.Agent != "codex" || b.Repo != "a_b" || b.Prompts != 2 || b.Actions != 0 || b.Flags != 0 || b.FirstPrompt != "add a --json flag" {
		t.Errorf("codex session = %+v", b)
	}
	// 2 system actions (the subagent start doesn't count), 1 open alert
	if a.ID != "404dabcd" || a.Repo != "nosaka" || a.Prompts != 2 || a.Actions != 2 || a.Flags != 1 || a.FirstPrompt != "Fix the Snooze crash" {
		t.Errorf("claude session = %+v", a)
	}
	if a.Started.IsZero() || a.LastActivity.IsZero() || a.Running || b.Running {
		t.Errorf("times/running: %+v %+v", a, b)
	}
	if len(l.Agents) != 2 || len(l.Repos) != 2 || l.HistoryBegins.IsZero() {
		t.Errorf("agents %v repos %v begins %v", l.Agents, l.Repos, l.HistoryBegins)
	}

	// a window keeps only sessions active inside it, but Total still counts all
	late, err := ListSessions(ctx, st.DB, 1, now.Add(24*time.Hour+30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(late.Sessions) != 0 || late.Total != 2 {
		t.Errorf("a day later: %d sessions, total %d", len(late.Sessions), late.Total)
	}
	both, _ := ListSessions(ctx, st.DB, 1, now)
	if len(both.Sessions) != 2 {
		t.Errorf("1-day window = %d sessions", len(both.Sessions))
	}

	// a prompt still working (not ended, recent activity) makes its session running
	if _, err := st.DB.Exec(`UPDATE prompts SET ended_at = NULL, last_activity_at = ? WHERE id = ?`, dbtime.Format(now.Add(-time.Minute)), ids["p4"]); err != nil {
		t.Fatal(err)
	}
	run, _ := ListSessions(ctx, st.DB, 0, now)
	if !run.Sessions[0].Running || run.Sessions[1].Running {
		t.Errorf("running = %v %v", run.Sessions[0].Running, run.Sessions[1].Running)
	}
	stale, _ := ListSessions(ctx, st.DB, 0, now.Add(time.Hour))
	if stale.Sessions[0].Running {
		t.Error("an hour of silence is not running")
	}
}

func TestListSessionsEmptyAndErrors(t *testing.T) {
	st, _ := seedPrompts(t)
	if _, err := st.DB.Exec(`DELETE FROM alerts; DELETE FROM actions; DELETE FROM prompts`); err != nil {
		t.Fatal(err)
	}
	l, err := ListSessions(context.Background(), st.DB, 0, time.Now())
	if err != nil || len(l.Sessions) != 0 || l.Total != 0 {
		t.Errorf("no prompts: %v %+v", err, l)
	}
	st.DB.Close()
	if _, err := ListSessions(context.Background(), st.DB, 0, time.Now()); err == nil {
		t.Error("a closed database should be an error")
	}
}
