package store

import (
	"context"
	"testing"
)

func TestProcessesAreRecordedAttributedAndExited(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, Session{Agent: "claude", AgentSessionID: "s1", TS: "2026-10-17T09:00:00.000000000Z"})
	pid, _ := tx.AddPrompt(ctx, sid, "hi", "2026-10-17T09:00:00.000000000Z")

	root := Process{ExecID: "e1", Collector: "eslogger", PID: 100, Binary: "/x/claude", Argv: `["claude"]`, Cwd: "/w", UID: 501, StartedAt: "2026-10-17T09:00:01.000000000Z"}
	if err := tx.AddProcess(ctx, root); err != nil {
		t.Fatal(err)
	}
	child := Process{ExecID: "e2", Collector: "eslogger", PID: 101, ParentExecID: "e1", Binary: "/bin/zsh", StartedAt: "2026-10-17T09:00:02.000000000Z"}
	if err := tx.AddProcess(ctx, child); err != nil {
		t.Fatal(err)
	}
	// the same exec reported again (the /proc or startup scan, then live) changes nothing
	dup := root
	dup.Binary = "/other"
	if err := tx.AddProcess(ctx, dup); err != nil {
		t.Fatal(err)
	}
	if err := tx.AttributeProcess(ctx, "e1", sid, pid); err != nil {
		t.Fatal(err)
	}
	// attributing with zeros leaves what is there
	if err := tx.AttributeProcess(ctx, "e1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if known, err := tx.ExitProcess(ctx, "e2", "2026-10-17T09:00:03.000000000Z", 1); err != nil || !known {
		t.Fatalf("exit known process: %v %v", known, err)
	}
	if known, err := tx.ExitProcess(ctx, "nope", "2026-10-17T09:00:03.000000000Z", 0); err != nil || known {
		t.Fatalf("exit unknown process: %v %v", known, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var bin, argv, cwd, parent string
	var sess, prompt, uid int64
	if err := st.DB.QueryRow(`SELECT binary, COALESCE(argv,''), COALESCE(cwd,''), COALESCE(a.session_id,0), COALESCE(a.prompt_id,0), uid FROM processes p LEFT JOIN process_attribution a ON a.exec_id = p.exec_id WHERE p.exec_id = 'e1'`).
		Scan(&bin, &argv, &cwd, &sess, &prompt, &uid); err != nil {
		t.Fatal(err)
	}
	if bin != "/x/claude" || argv != `["claude"]` || cwd != "/w" || sess != sid || prompt != pid || uid != 501 {
		t.Errorf("root = %q %q %q session %d prompt %d uid %d", bin, argv, cwd, sess, prompt, uid)
	}
	var exited string
	var code int
	if err := st.DB.QueryRow(`SELECT COALESCE(parent_exec_id,''), exited_at, exit_code FROM processes WHERE exec_id = 'e2'`).Scan(&parent, &exited, &code); err != nil {
		t.Fatal(err)
	}
	if parent != "e1" || exited != "2026-10-17T09:00:03.000000000Z" || code != 1 {
		t.Errorf("child = parent %q exited %q code %d", parent, exited, code)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM processes`).Scan(&n)
	if n != 2 {
		t.Errorf("%d processes, want 2", n)
	}
}

func appendKernel(t *testing.T, st *Store, events ...KernelEvent) []int64 {
	t.Helper()
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	var ids []int64
	for _, e := range events {
		id, err := tx.AppendKernel(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// seedKernelChain is a store with four chained kernel events.
func seedKernelChain(t *testing.T) (*Store, []int64) {
	st := openTest(t)
	ids := appendKernel(t, st,
		KernelEvent{ExecID: "e1", TS: "2026-10-17T09:00:01.000000000Z", Type: "exec", Payload: `{"a":1}`},
		KernelEvent{ExecID: "e1", TS: "2026-10-17T09:00:02.000000000Z", Type: "file_write", Path: "/w/a.go", Payload: `{"b":2}`},
		KernelEvent{ExecID: "e2", TS: "2026-10-17T09:00:03.000000000Z", Type: "file_rename", Path: "/w/a.tmp", Path2: "/w/a.go", Payload: `{"c":3}`},
		KernelEvent{TS: "2026-10-17T09:00:04.000000000Z", Type: "net_connect", Payload: `{"d":4}`}, // no process known
	)
	return st, ids
}

func TestKernelChainDetectsEditing(t *testing.T) {
	st, ids := seedKernelChain(t)
	ctx := context.Background()
	if len(ids) != 4 {
		t.Fatal("ids")
	}
	res, err := st.VerifyKernelChainFrom(ctx, ChainPoint{})
	if err != nil || res.BadID != 0 || res.Checked != 4 || res.Last.ID != ids[3] {
		t.Fatalf("intact chain = %+v, %v", res, err)
	}
	// checking from a verified point only looks at what came after
	part, _ := st.VerifyKernelChainFrom(ctx, ChainPoint{ID: ids[1], Hash: hashOf(t, st, ids[1])})
	if part.Checked != 2 || part.BadID != 0 {
		t.Errorf("incremental = %+v", part)
	}

	// every covered field is protected: payload, path, type, exec, time
	for name, stmt := range map[string]string{
		"payload": `UPDATE kernel_events SET payload = '{"b":999}' WHERE id = ?`,
		"path":    `UPDATE kernel_events SET path = '/etc/passwd' WHERE id = ?`,
		"path2":   `UPDATE kernel_events SET path2 = '/etc/passwd' WHERE id = ?`,
		"type":    `UPDATE kernel_events SET type = 'file_unlink' WHERE id = ?`,
		"exec":    `UPDATE kernel_events SET exec_id = 'other' WHERE id = ?`,
		"time":    `UPDATE kernel_events SET ts = '2026-10-17T09:00:09.000000000Z' WHERE id = ?`,
	} {
		fresh, fids := seedKernelChain(t) // each edit on its own copy
		dropGuards(t, fresh)
		if _, err := fresh.DB.Exec(stmt, fids[2]); err != nil {
			t.Fatal(err)
		}
		res, _ := fresh.VerifyKernelChainFrom(ctx, ChainPoint{})
		if res.BadID != fids[2] {
			t.Errorf("editing %s: bad id = %d, want %d", name, res.BadID, fids[2])
		}
	}
	// deleting an event breaks the chain at the next one
	fresh, fids := seedKernelChain(t)
	dropGuards(t, fresh)
	if _, err := fresh.DB.Exec(`DELETE FROM kernel_events WHERE id = ?`, fids[1]); err != nil {
		t.Fatal(err)
	}
	if res, _ := fresh.VerifyKernelChainFrom(ctx, ChainPoint{}); res.BadID != fids[2] {
		t.Errorf("deleting an event: bad id = %d, want %d", res.BadID, fids[2])
	}
	// the chain of kernel events is separate from the raw-record chain
	if bad, err := st.VerifyChain(ctx); err != nil || bad != 0 {
		t.Errorf("raw chain = %d, %v", bad, err)
	}
}

// dropGuards removes the append-only triggers so a test can tamper with
// evidence the way a person with the file could, and check it is detected.
func dropGuards(t *testing.T, st *Store) {
	t.Helper()
	for _, g := range []string{"raw_log_records_no_update", "raw_log_records_no_delete", "kernel_events_no_update", "kernel_events_no_delete"} {
		if _, err := st.DB.Exec(`DROP TRIGGER ` + g); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEvidenceRejectsUpdateAndDelete(t *testing.T) {
	st, ids := seedKernelChain(t)
	for _, stmt := range []string{
		`UPDATE kernel_events SET payload = '{}' WHERE id = ?`,
		`DELETE FROM kernel_events WHERE id = ?`,
	} {
		if _, err := st.DB.Exec(stmt, ids[0]); err == nil {
			t.Errorf("%s was allowed", stmt)
		}
	}
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	if _, err := tx.AppendRaw(ctx, RawRecord{TS: "2026-10-17T09:00:00.000000000Z", SourceFile: "f", Type: "user", Record: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`UPDATE raw_log_records SET record = '{}'`, `DELETE FROM raw_log_records`} {
		if _, err := st.DB.Exec(stmt); err == nil {
			t.Errorf("%s was allowed", stmt)
		}
	}
}

func TestExitIsAChainedEvent(t *testing.T) {
	st, _ := seedKernelChain(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	if err := tx.AddProcess(ctx, Process{ExecID: "x", Collector: "tetragon", PID: 9, Binary: "/bin/true", StartedAt: "2026-10-17T09:00:00.000000000Z"}); err != nil {
		t.Fatal(err)
	}
	if known, err := tx.ExitProcess(ctx, "x", "2026-10-17T09:00:05.000000000Z", 2); err != nil || !known {
		t.Fatalf("exit = %v, %v", known, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := st.DB.QueryRow(`SELECT payload FROM kernel_events WHERE exec_id = 'x' AND type = 'exit'`).Scan(&payload); err != nil || payload != `{"exit_code":2}` {
		t.Errorf("exit event = %q, %v", payload, err)
	}
	if res, err := st.VerifyKernelChainFrom(ctx, ChainPoint{}); err != nil || res.BadID != 0 {
		t.Errorf("chain with exit = %+v, %v", res, err)
	}
}

func hashOf(t *testing.T, st *Store, id int64) string {
	t.Helper()
	var h string
	if err := st.DB.QueryRow(`SELECT hash FROM kernel_events WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestGapsOpenAndClose(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	id, err := tx.OpenGap(ctx, Gap{Collector: "eslogger", StartedAt: "2026-10-17T03:10:00.000000000Z", Reason: "eslogger exited"})
	if err != nil || id == 0 {
		t.Fatalf("open: %d %v", id, err)
	}
	if ok, err := tx.CloseGap(ctx, id, "2026-10-17T03:42:00.000000000Z"); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	// closing again, or closing an unknown gap, changes nothing
	if ok, _ := tx.CloseGap(ctx, id, "2026-10-17T05:00:00.000000000Z"); ok {
		t.Error("a closed gap closed again")
	}
	if ok, _ := tx.CloseGap(ctx, 999, "2026-10-17T05:00:00.000000000Z"); ok {
		t.Error("an unknown gap closed")
	}
	open, _ := tx.OpenGap(ctx, Gap{Collector: "tetragon", StartedAt: "2026-10-17T06:00:00.000000000Z", Reason: "events lost"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var ended string
	st.DB.QueryRow(`SELECT ended_at FROM monitor_gaps WHERE id = ?`, id).Scan(&ended)
	if ended != "2026-10-17T03:42:00.000000000Z" {
		t.Errorf("ended_at = %q", ended)
	}
	var still int
	st.DB.QueryRow(`SELECT COUNT(*) FROM monitor_gaps WHERE id = ? AND ended_at IS NULL`, open).Scan(&still)
	if still != 1 {
		t.Error("the second gap should still be open")
	}
}

func TestKernelWritesFailOnAFinishedTransactionOrClosedDatabase(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	tx.Rollback() // every write below now hits a finished transaction
	defer st.DB.Close()
	st.DB.Close()
	if err := tx.AddProcess(ctx, Process{ExecID: "e", Binary: "b", StartedAt: "t"}); err == nil {
		t.Error("AddProcess")
	}
	if _, err := tx.ExitProcess(ctx, "e", "t", 0); err == nil {
		t.Error("ExitProcess")
	}
	if err := tx.AttributeProcess(ctx, "e", 1, 1); err == nil {
		t.Error("AttributeProcess")
	}
	if _, err := tx.AppendKernel(ctx, KernelEvent{TS: "t", Type: "x"}); err == nil {
		t.Error("AppendKernel")
	}
	if _, err := tx.OpenGap(ctx, Gap{}); err == nil {
		t.Error("OpenGap")
	}
	if _, err := tx.CloseGap(ctx, 1, "t"); err == nil {
		t.Error("CloseGap")
	}
	if _, err := st.VerifyKernelChainFrom(ctx, ChainPoint{}); err == nil {
		t.Error("VerifyKernelChainFrom")
	}
}
