package store

import (
	"context"
	"testing"
)

// fileFixture: a session and prompt, an agent process "root" (pid 1) attributed
// to it, another process "other" (pid 2) under it, and logged file actions.
func fileFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T09:00:00.000000000Z"})
	pid, _ := tx.AddPrompt(ctx, sid, "p", "2026-10-17T09:00:00.000000000Z")
	for _, p := range []Process{
		{ExecID: "root", Collector: "tetragon", PID: 1, Binary: "/opt/claude", Cwd: "/w", StartedAt: "2026-10-17T08:00:00.000000000Z"},
		{ExecID: "other", Collector: "tetragon", PID: 2, ParentExecID: "root", Binary: "/bin/cat", Cwd: "/w", StartedAt: "2026-10-17T09:00:01.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, pid); err != nil {
			t.Fatal(err)
		}
	}
	for i, a := range []Action{
		{Resource: "file", Action: "read", Path: "/w/a.go", ToolUseID: "r1"},
		{Resource: "file", Action: "update", Path: "/w/a.go", ToolUseID: "e1"},
		{Resource: "file", Action: "create", Path: "/w/new.go", ToolUseID: "w1"},
		{Resource: "file", Action: "delete", Path: "/w/old.go", ToolUseID: "d1"}, // not matchable
		{Resource: "command", Action: "execute", Path: "ls", ToolUseID: "c1"},    // not a file action
	} {
		a.SessionID, a.PromptID, a.Evidence = sid, pid, "log"
		a.TS = "2026-10-17T09:00:1" + string(rune('0'+i)) + ".000000000Z"
		if err := tx.AddAction(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, sid
}

func TestOpenFileActionsAndAgentPIDs(t *testing.T) {
	st, sid := fileFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()

	refs, err := tx.OpenFileActions(ctx, 1)
	if err != nil || len(refs) != 3 { // read, update, create: not the delete or the command
		t.Fatalf("OpenFileActions = %+v, %v", refs, err)
	}
	if refs[0].Action != "read" || refs[0].Path != "/w/a.go" || refs[0].SessionID != sid {
		t.Errorf("first = %+v", refs[0])
	}

	pids, err := tx.AgentPIDs(ctx, sid)
	if err != nil || len(pids) != 1 || pids[0] != 1 { // the root, not its child
		t.Errorf("AgentPIDs = %v, %v", pids, err)
	}
	if none, _ := tx.AgentPIDs(ctx, sid+99); len(none) != 0 {
		t.Errorf("AgentPIDs of another session = %v", none)
	}

	// a call settled under this matcher is not offered again; an older matcher's is
	if err := tx.SettleCall(ctx, "r1", 1); err != nil {
		t.Fatal(err)
	}
	if refs, _ := tx.OpenFileActions(ctx, 1); len(refs) != 2 {
		t.Errorf("settled call offered: %+v", refs)
	}
	if refs, _ := tx.OpenFileActions(ctx, 2); len(refs) != 3 {
		t.Errorf("a newer matcher should look again: %+v", refs)
	}
}

func TestFileEvent(t *testing.T) {
	st, sid := fileFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	add := func(exec, typ, path, at string) int64 {
		id, err := tx.AppendKernel(ctx, KernelEvent{ExecID: exec, TS: "2026-10-17T09:00:" + at + ".000000000Z", Type: typ, Path: path, Payload: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	open := add("root", "file_open", "/w/a.go", "10")
	add("other", "file_open", "/w/a.go", "10") // another process
	rename := add("root", "file_rename", "/w/a.go.tmp.1.abcd", "12")
	create := add("root", "file_create", "/w/new.go", "13")
	add("root", "file_rename", "/w/a.gox.tmp.1.abcd", "11") // another file that starts the same
	add("root", "file_rename", "/w/a.go.bak", "11")         // not a temp file
	from, to := "2026-10-17T09:00:00.000000000Z", "2026-10-17T09:01:00.000000000Z"
	pids, _ := tx.AgentPIDs(ctx, sid)

	for _, tc := range []struct {
		action, path string
		want         int64
	}{
		{"read", "/w/a.go", open},
		{"update", "/w/a.go", rename},
		{"create", "/w/new.go", create},
		{"read", "/w/missing.go", 0},
		{"update", "/w/missing.go", 0},
	} {
		got, err := tx.FileEvent(ctx, pids, tc.action, tc.path, from, to)
		if err != nil || got != tc.want {
			t.Errorf("FileEvent(%s %s) = %d, %v; want %d", tc.action, tc.path, got, err, tc.want)
		}
	}
	// outside the window, and with no agent process
	if got, _ := tx.FileEvent(ctx, pids, "read", "/w/a.go", "2026-10-17T09:00:11.000000000Z", to); got != 0 {
		t.Errorf("event before the window = %d", got)
	}
	if got, err := tx.FileEvent(ctx, nil, "read", "/w/a.go", from, to); got != 0 || err != nil {
		t.Errorf("no pids = %d, %v", got, err)
	}

	// an event confirms one action: once linked, it is not offered again
	var action int64
	if err := tx.tx.QueryRowContext(ctx, `SELECT id FROM actions WHERE tool_use_id = 'r1'`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if err := tx.LinkFile(ctx, action, open); err != nil {
		t.Fatal(err)
	}
	var ev string
	var linked int64
	tx.tx.QueryRowContext(ctx, `SELECT evidence, kernel_event_id FROM actions WHERE id = ?`, action).Scan(&ev, &linked)
	if ev != "both" || linked != open {
		t.Errorf("linked action = %q %d", ev, linked)
	}
	if got, _ := tx.FileEvent(ctx, pids, "read", "/w/a.go", from, to); got != 0 {
		t.Errorf("a claimed event offered again: %d", got)
	}
}

func TestFileQueriesReportStoreErrors(t *testing.T) {
	st, sid := fileFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	tx.tx.ExecContext(ctx, `DROP TABLE kernel_events`)
	if _, err := tx.FileEvent(ctx, []int64{1}, "read", "/w/a.go", "a", "b"); err == nil {
		t.Error("FileEvent")
	}
	tx.tx.ExecContext(ctx, `DROP TABLE process_attribution`)
	if _, err := tx.AgentPIDs(ctx, sid); err == nil {
		t.Error("AgentPIDs")
	}
	tx.tx.ExecContext(ctx, `DROP TABLE actions`)
	if _, err := tx.OpenFileActions(ctx, 1); err == nil {
		t.Error("OpenFileActions")
	}
	if err := tx.LinkFile(ctx, 1, 1); err == nil {
		t.Error("LinkFile")
	}
	tx.Rollback()
}

func TestToolRunsFitInsideAShell(t *testing.T) {
	st, _ := agentFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	// a wrapper shell, and the shell's grep: the agent's binary, injected flags, no parent
	if err := tx.AddProcess(ctx, Process{ExecID: "sh", Collector: "tetragon", PID: 70, ParentExecID: "root", Binary: "/bin/bash", Cwd: "/w", StartedAt: "2026-10-17T09:20:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	grep := `["-G","--ignore-files","--hidden","-I","--exclude-dir=.git","-n","x"]`
	for _, p := range []Process{
		{ExecID: "ug", Collector: "tetragon", PID: 71, Binary: "/opt/claude", Argv: grep, Cwd: "/w", StartedAt: "2026-10-17T09:20:01.000000000Z"},
		{ExecID: "late", Collector: "tetragon", PID: 72, Binary: "/opt/claude", Argv: grep, Cwd: "/w", StartedAt: "2026-10-17T09:25:00.000000000Z"},
		{ExecID: "away", Collector: "tetragon", PID: 73, Binary: "/opt/claude", Argv: grep, Cwd: "/elsewhere", StartedAt: "2026-10-17T09:20:01.000000000Z"},
		{ExecID: "plain", Collector: "tetragon", PID: 74, Binary: "/opt/claude", Argv: `["-c"]`, Cwd: "/w", StartedAt: "2026-10-17T09:20:01.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExitProcess(ctx, "sh", "2026-10-17T09:20:05.000000000Z", 0); err != nil {
		t.Fatal(err)
	}
	runs, err := tx.ToolRuns(ctx, "sh")
	if err != nil || len(runs) != 1 || runs[0].ExecID != "ug" {
		t.Fatalf("ToolRuns = %+v, %v; want only the grep inside the shell's lifetime and directory", runs, err)
	}
	// one already explained by an action (not a kernel-only one) is not offered
	if err := tx.AddAction(ctx, Action{SessionID: 1, TS: "2026-10-17T09:20:01.000000000Z", Resource: "command", Action: "execute", Path: "grep", Evidence: "log", ToolUseID: "c9"}); err != nil {
		t.Fatal(err)
	}
	var logged int64
	if err := tx.tx.QueryRowContext(ctx, `SELECT id FROM actions WHERE tool_use_id = 'c9'`).Scan(&logged); err != nil {
		t.Fatal(err)
	}
	if err := tx.LinkPart(ctx, logged, "ug"); err != nil {
		t.Fatal(err)
	}
	if runs, _ := tx.ToolRuns(ctx, "sh"); len(runs) != 0 {
		t.Errorf("a claimed run is offered again: %+v", runs)
	}
	tx.tx.ExecContext(ctx, `DROP TABLE processes`)
	if _, err := tx.ToolRuns(ctx, "sh"); err == nil {
		t.Error("ToolRuns store error")
	}
}
