package store

import (
	"context"
	"strings"
	"testing"
)

// agentFixture: a session, an agent root "root" with children "g" (git) and
// "id", and a grandchild "gg" under "g".
func agentFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T09:00:00.000000000Z"})
	pid, _ := tx.AddPrompt(ctx, sid, "p", "2026-10-17T09:00:00.000000000Z")
	for _, p := range []Process{
		{ExecID: "root", Collector: "tetragon", PID: 1, Binary: "/opt/claude", StartedAt: "2026-10-17T09:00:00.000000000Z"},
		{ExecID: "g", Collector: "tetragon", PID: 2, ParentExecID: "root", Binary: "/usr/bin/git", Argv: `["status"]`, StartedAt: "2026-10-17T09:00:02.000000000Z"},
		{ExecID: "id", Collector: "tetragon", PID: 3, ParentExecID: "root", Binary: "/usr/bin/id", StartedAt: "2026-10-17T09:00:03.000000000Z"},
		{ExecID: "gg", Collector: "tetragon", PID: 4, ParentExecID: "g", Binary: "/usr/bin/ssh", StartedAt: "2026-10-17T09:00:04.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, pid); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, sid
}

func TestBackgroundClassification(t *testing.T) {
	st, sid := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()

	// only the agent root's direct children are candidates
	un, err := tx.UncheckedBackground(ctx, 1)
	if err != nil || len(un) != 2 || un[0].ExecID != "g" || un[1].ExecID != "id" {
		t.Fatalf("UncheckedBackground = %+v, %v", un, err)
	}
	if err := tx.SetBackground(ctx, "g", "git status", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetBackground(ctx, "id", "", 1); err != nil { // checked: not background
		t.Fatal(err)
	}
	if un, _ := tx.UncheckedBackground(ctx, 1); len(un) != 0 {
		t.Errorf("checked processes are offered again: %+v", un)
	}

	// a kernel-only action for a background command is dropped, one for any other is kept,
	// and so is one an alert points at
	for _, a := range []Action{
		{SessionID: sid, ExecID: "g", TS: "2026-10-17T09:00:02.000000000Z", Resource: "command", Action: "execute", Path: "git status"},
		{SessionID: sid, ExecID: "id", TS: "2026-10-17T09:00:03.000000000Z", Resource: "command", Action: "execute", Path: "id"},
	} {
		if err := tx.AddKernelAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := tx.DropBackgroundActions(ctx); err != nil || n != 1 {
		t.Fatalf("DropBackgroundActions = %d, %v; want 1", n, err)
	}
	var left int
	tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE evidence = 'kernel' AND exec_id = 'id'`).Scan(&left)
	if left != 1 {
		t.Error("an action for a command that is not background was dropped")
	}

	if err := tx.AddKernelAction(ctx, Action{SessionID: sid, ExecID: "gg", TS: "2026-10-17T09:00:04.000000000Z", Resource: "command", Action: "execute", Path: "ssh"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetBackground(ctx, "gg", "x", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.tx.ExecContext(ctx, `INSERT INTO alerts (action_id, ts, severity, rule, status) SELECT id, ts, 'medium', 'r', 'open' FROM actions WHERE exec_id = 'gg'`); err != nil {
		t.Fatal(err)
	}
	if n, err := tx.DropBackgroundActions(ctx); err != nil || n != 0 {
		t.Errorf("an action with an alert was dropped: %d, %v", n, err)
	}
}

// An agent that restarts itself in place (an update) shows as a new exec of the
// same pid whose parent is the old exec. Its children are still the agent's.
func TestChildrenOfARestartedAgentAreTheAgentsToo(t *testing.T) {
	st, sid := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	for _, p := range []Process{
		{ExecID: "img2", Collector: "tetragon", PID: 1, ParentExecID: "root", Binary: "/opt/claude2", StartedAt: "2026-10-17T09:30:00.000000000Z"},
		{ExecID: "c2", Collector: "tetragon", PID: 50, ParentExecID: "img2", Binary: "/usr/bin/git", Argv: `["status"]`, StartedAt: "2026-10-17T09:31:00.000000000Z"},
		{ExecID: "deep", Collector: "tetragon", PID: 51, ParentExecID: "c2", Binary: "/usr/bin/ssh", StartedAt: "2026-10-17T09:31:01.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, 0); err != nil {
			t.Fatal(err)
		}
	}
	var root string
	tx.tx.QueryRowContext(ctx, `SELECT root_exec_id FROM process_attribution WHERE exec_id = 'c2'`).Scan(&root)
	if root != "root" {
		t.Fatalf("c2 descends from %q, want root", root)
	}
	un, err := tx.UncheckedBackground(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range un {
		got[p.ExecID] = true
	}
	if !got["c2"] || got["deep"] {
		t.Errorf("candidates = %v: the restarted image's child is one, its grandchild is not", got)
	}
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T10:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	ex, err := tx.Unexplained(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ex {
		found = found || p.ExecID == "c2"
	}
	if !found {
		t.Error("a child of the restarted image is not offered as a kernel action")
	}
}

// A shell forks subshells for pipelines; Tetragon reports each as an exec with
// the parent's own binary and arguments. They are the shell running on, never
// a command, so no kernel action comes of them (and an old one is dropped).
func TestForksOfAShellAreNotCommands(t *testing.T) {
	st, sid := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	for _, p := range []Process{
		{ExecID: "sh", Collector: "tetragon", PID: 60, ParentExecID: "root", Binary: "/bin/bash", Argv: `["-c","eval 'a | b'"]`, StartedAt: "2026-10-17T09:10:00.000000000Z"},
		{ExecID: "fork", Collector: "tetragon", PID: 61, ParentExecID: "sh", Binary: "/bin/bash", Argv: `["-c","eval 'a | b'"]`, StartedAt: "2026-10-17T09:10:00.100000000Z"},
		{ExecID: "real", Collector: "tetragon", PID: 62, ParentExecID: "sh", Binary: "/usr/bin/a", Argv: `[]`, StartedAt: "2026-10-17T09:10:00.200000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, 0); err != nil {
			t.Fatal(err)
		}
	}
	kids, err := tx.Children(ctx, "sh")
	if err != nil || len(kids) != 1 || kids[0].ExecID != "real" {
		t.Errorf("Children = %+v, %v; the fork is not a command", kids, err)
	}
	if err := tx.LinkCall(ctx, "t", "sh"); err != nil { // the shell ran a call
		t.Fatal(err)
	}
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T11:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	un, err := tx.Unexplained(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range un {
		if p.ExecID == "fork" {
			t.Error("a fork of the wrapper shell is offered as a kernel action")
		}
	}

	// one recorded before the fork rule existed is dropped
	if err := tx.AddKernelAction(ctx, Action{SessionID: sid, ExecID: "fork", TS: "2026-10-17T09:10:00.100000000Z", Resource: "command", Action: "execute", Path: "a | b"}); err != nil {
		t.Fatal(err)
	}
	if n, err := tx.DropBackgroundActions(ctx); err != nil || n != 1 {
		t.Errorf("DropBackgroundActions = %d, %v; want the fork's action", n, err)
	}
}

// A process is checked once per version of the rules, so a rule added later
// reaches what was recorded before it.
func TestChangedRulesCheckProcessesAgain(t *testing.T) {
	st, _ := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	for _, id := range []string{"g", "id"} {
		if err := tx.SetBackground(ctx, id, "", 1); err != nil {
			t.Fatal(err)
		}
	}
	if un, _ := tx.UncheckedBackground(ctx, 1); len(un) != 0 {
		t.Errorf("checked under the same rules, still offered: %+v", un)
	}
	un, err := tx.UncheckedBackground(ctx, 2)
	if err != nil || len(un) != 2 {
		t.Errorf("under new rules = %+v, %v; want both again", un, err)
	}
}

func TestBackgroundCommandsAreNotExplainedAway(t *testing.T) {
	st, _ := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	// the log has reached the processes
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T10:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetBackground(ctx, "g", "git status", 1); err != nil {
		t.Fatal(err)
	}
	un, err := tx.Unexplained(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range un {
		if p.ExecID == "g" {
			t.Error("a background command is offered as a kernel action")
		}
	}
	if len(un) != 1 || un[0].ExecID != "id" {
		t.Errorf("Unexplained = %+v, want just the id command", un)
	}
}

func TestMigration9BackfillsTheAgentProcess(t *testing.T) {
	st, _ := agentFixture(t)
	ctx := context.Background()
	m := migrations[8]
	backfill := m[strings.Index(m, "WITH RECURSIVE"):strings.Index(m, "CREATE INDEX")]
	if _, err := st.DB.ExecContext(ctx, `UPDATE process_attribution SET root_exec_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, backfill); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for _, id := range []string{"root", "g", "id", "gg"} {
		var root string
		if err := st.DB.QueryRowContext(ctx, `SELECT root_exec_id FROM process_attribution WHERE exec_id = ?`, id).Scan(&root); err != nil {
			t.Fatal(err)
		}
		if root != "root" {
			t.Errorf("%s: root = %q, want root", id, root)
		}
	}
}

func TestBackgroundQueriesReportStoreErrors(t *testing.T) {
	st, _ := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	tx.tx.ExecContext(ctx, `DROP TABLE process_attribution`)
	if _, err := tx.UncheckedBackground(ctx, 1); err == nil {
		t.Error("UncheckedBackground")
	}
	if err := tx.SetBackground(ctx, "g", "k", 1); err == nil {
		t.Error("SetBackground")
	}
	if _, err := tx.DropBackgroundActions(ctx); err == nil {
		t.Error("DropBackgroundActions")
	}
	tx.Rollback()
}
