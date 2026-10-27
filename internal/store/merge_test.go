package store

import (
	"context"
	"testing"
)

// mergeFixture: a session, a prompt, an attributed agent root "root", a wrapper
// shell "sh" under it with one child "ls", and a logged Bash call for it.
func mergeFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T09:00:00.000000000Z"})
	pid, _ := tx.AddPrompt(ctx, sid, "p", "2026-10-17T09:00:00.000000000Z")
	for _, p := range []Process{
		{ExecID: "root", Collector: "tetragon", PID: 1, Binary: "/opt/claude", StartedAt: "2026-10-17T09:00:00.000000000Z"},
		{ExecID: "sh", Collector: "tetragon", PID: 2, ParentExecID: "root", Binary: "/bin/bash", Argv: `["-c","eval 'ls' < /dev/null"]`, StartedAt: "2026-10-17T09:00:02.000000000Z"},
		{ExecID: "ls", Collector: "tetragon", PID: 3, ParentExecID: "sh", Binary: "/bin/ls", StartedAt: "2026-10-17T09:00:03.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, pid); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.AddAction(ctx, Action{SessionID: sid, PromptID: pid, TS: "2026-10-17T09:00:01.000000000Z", Resource: "command", Action: "execute",
		Path: "ls", Evidence: "log", ToolUseID: "t1", CallCommand: "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, sid
}

func TestMergeQueries(t *testing.T) {
	st, sid := mergeFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()

	calls, err := tx.OpenCalls(ctx)
	if err != nil || len(calls) != 1 || calls[0].ToolUseID != "t1" || calls[0].SessionID != sid || len(calls[0].Parts) != 1 || calls[0].Parts[0].Text != "ls" {
		t.Fatalf("OpenCalls = %+v, %v", calls, err)
	}
	shells, err := tx.FreeShells(ctx)
	if err != nil || len(shells) != 1 || shells[0].ExecID != "sh" || shells[0].SessionID != sid {
		t.Fatalf("FreeShells = %+v, %v", shells, err)
	}
	kids, err := tx.Children(ctx, "sh")
	if err != nil || len(kids) != 1 || kids[0].ExecID != "ls" {
		t.Fatalf("Children = %+v, %v", kids, err)
	}

	// the shell and the root's other children are what a kernel action may come from
	if first, last, err := tx.KernelRange(ctx); err != nil || first != "" || last != "" {
		t.Errorf("KernelRange with no events = %q %q %v", first, last, err)
	}
	if _, err := tx.AppendKernel(ctx, KernelEvent{ExecID: "root", TS: "2026-10-17T09:00:05.000000000Z", Type: "exec", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	if first, last, err := tx.KernelRange(ctx); err != nil || first != "2026-10-17T09:00:05.000000000Z" || last != first {
		t.Errorf("KernelRange = %q %q %v", first, last, err)
	}

	if err := tx.LinkCall(ctx, "t1", "sh"); err != nil {
		t.Fatal(err)
	}
	if err := tx.LinkCall(ctx, "t1", "sh"); err != nil { // linking again is fine
		t.Fatal(err)
	}
	if shells, _ := tx.FreeShells(ctx); len(shells) != 0 {
		t.Errorf("a linked shell is still free: %+v", shells)
	}
	if calls, _ := tx.OpenCalls(ctx); len(calls) != 1 || calls[0].Shell != "sh" {
		t.Errorf("open linked call = %+v", calls)
	}
	un, err := tx.Unexplained(ctx)
	if err != nil || len(un) != 0 { // the session's log ends at 09:00:00, before the processes
		t.Errorf("Unexplained before the log caught up = %+v, %v", un, err)
	}

	if err := tx.LinkPart(ctx, calls[0].Parts[0].ActionID, "ls"); err != nil {
		t.Fatal(err)
	}
	var ev, exec string
	if err := tx.tx.QueryRowContext(ctx, `SELECT evidence, exec_id FROM actions WHERE tool_use_id = 't1'`).Scan(&ev, &exec); err != nil || ev != "both" || exec != "ls" {
		t.Errorf("linked part = %q %q, %v", ev, exec, err)
	}
	if kids, _ := tx.Children(ctx, "sh"); len(kids) != 0 {
		t.Errorf("a child that has an action is still offered: %+v", kids)
	}

	if err := tx.SettleCall(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if calls, _ := tx.OpenCalls(ctx); len(calls) != 0 {
		t.Errorf("a settled call is still open: %+v", calls)
	}
	if err := tx.SettleCall(ctx, "other"); err != nil { // a call never linked
		t.Fatal(err)
	}
}

func TestUnexplainedAndKernelActions(t *testing.T) {
	st, sid := mergeFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	defer tx.Rollback()
	// the log has reached the processes
	if _, err := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s", TS: "2026-10-17T10:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	un, err := tx.Unexplained(ctx)
	if err != nil || len(un) != 1 || un[0].ExecID != "sh" { // an unmatched wrapper is a child of the root; its child is not offered
		t.Fatalf("Unexplained = %+v, %v", un, err)
	}
	if err := tx.AddKernelAction(ctx, Action{SessionID: sid, ExecID: "sh", TS: un[0].StartedAt, Resource: "command", Action: "execute", Path: "ls"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.AddKernelAction(ctx, Action{SessionID: sid, ExecID: "sh", TS: un[0].StartedAt, Resource: "command", Action: "execute", Path: "ls"}); err != nil {
		t.Fatal(err) // the same exec twice is ignored
	}
	var n int
	tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE exec_id = 'sh' AND evidence = 'kernel'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d kernel actions for one exec", n)
	}
	if un, _ := tx.Unexplained(ctx); len(un) != 0 {
		t.Errorf("an exec with an action is still unexplained: %+v", un)
	}
}

func TestMergeQueriesReportStoreErrors(t *testing.T) {
	st, _ := mergeFixture(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	tx.tx.ExecContext(ctx, `DROP TABLE call_links`)
	if _, err := tx.OpenCalls(ctx); err == nil {
		t.Error("OpenCalls")
	}
	if _, err := tx.FreeShells(ctx); err == nil {
		t.Error("FreeShells")
	}
	if err := tx.LinkCall(ctx, "t", "s"); err == nil {
		t.Error("LinkCall")
	}
	if err := tx.SettleCall(ctx, "t"); err == nil {
		t.Error("SettleCall")
	}
	if _, err := tx.Unexplained(ctx); err == nil {
		t.Error("Unexplained")
	}
	tx.tx.ExecContext(ctx, `DROP TABLE actions`)
	if err := tx.LinkPart(ctx, 1, "e"); err == nil {
		t.Error("LinkPart")
	}
	if err := tx.AddKernelAction(ctx, Action{}); err == nil {
		t.Error("AddKernelAction")
	}
	if _, err := tx.Children(ctx, "sh"); err == nil {
		t.Error("Children")
	}
	tx.tx.ExecContext(ctx, `DROP TABLE kernel_events`)
	if _, _, err := tx.KernelRange(ctx); err == nil {
		t.Error("KernelRange")
	}
	tx.Rollback()
}
