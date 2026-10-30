package merge

import (
	"testing"

	"github.com/nosaka4i/sentient/internal/store"
)

// A file tool call (Read, Edit, Write) runs inside the agent process, so the
// kernel reports no exec for it: a Read is the agent process opening the file,
// and a write is it creating `<path>.tmp.<pid>.<hash>` and renaming it over the
// path (seen in real Claude Code + Tetragon data).

// fileAction logs a file tool call at sec with its result at resSec.
func (f *fixture) fileAction(tool, action, path string, sec, resSec int) int64 {
	f.t.Helper()
	var id int64
	f.tx(func(tx *store.Tx) error {
		if err := tx.AddAction(f.ctx, store.Action{SessionID: f.sid, PromptID: f.pid, TS: ts(sec), Resource: "file", Action: action,
			Path: path, Evidence: "log", ToolUseID: tool}); err != nil {
			return err
		}
		_, err := tx.ApplyToolResult(f.ctx, store.ToolResult{ToolUseID: tool, TS: ts(resSec)})
		return err
	})
	if err := f.st.DB.QueryRow(`SELECT id FROM actions WHERE tool_use_id = ?`, tool).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// kernelFile records a file event by the process with the given exec id.
func (f *fixture) kernelFile(exec, typ, path string, sec int) {
	f.t.Helper()
	f.tx(func(tx *store.Tx) error {
		_, err := tx.AppendKernel(f.ctx, store.KernelEvent{ExecID: exec, TS: ts(sec), Type: typ, Path: path, Payload: "{}"})
		return err
	})
}

func (f *fixture) evidenceOf(id int64) (ev string, event *int64) {
	f.t.Helper()
	if err := f.st.DB.QueryRow(`SELECT evidence, kernel_event_id FROM actions WHERE id = ?`, id).Scan(&ev, &event); err != nil {
		f.t.Fatal(err)
	}
	return ev, event
}

func TestAReadIsConfirmedByTheAgentOpeningTheFile(t *testing.T) {
	f := newFixture(t) // "root" is the agent process
	rd := f.fileAction("r1", "read", "/w/a.go", 10, 11)
	f.kernelFile("root", "file_open", "/w/a.go", 10)
	f.seen(5)
	f.seen(60)
	f.merge()
	if ev, e := f.evidenceOf(rd); ev != "both" || e == nil {
		t.Errorf("read = %q %v, want confirmed by a kernel event", ev, e)
	}
}

func TestAWriteIsConfirmedByTheAtomicRename(t *testing.T) {
	f := newFixture(t)
	ed := f.fileAction("e1", "update", "/w/a.go", 20, 21)
	// the edit: read the file, write a temp file beside it, rename it over the file
	f.kernelFile("root", "file_open", "/w/a.go", 20)
	f.kernelFile("root", "file_create", "/w/a.go.tmp.118708.3c8d04c10799", 20)
	f.kernelFile("root", "file_rename", "/w/a.go.tmp.118708.3c8d04c10799", 20)
	f.seen(5)
	f.seen(60)
	f.merge()
	if ev, e := f.evidenceOf(ed); ev != "both" || e == nil {
		t.Errorf("edit = %q %v, want confirmed", ev, e)
	}
}

func TestOneKernelEventConfirmsOneAction(t *testing.T) {
	f := newFixture(t)
	a := f.fileAction("e1", "update", "/w/a.go", 20, 21)
	b := f.fileAction("e2", "update", "/w/a.go", 22, 23)          // a second edit of the same file
	f.kernelFile("root", "file_rename", "/w/a.go.tmp.1.aaaa", 20) // only one rename happened
	f.seen(5)
	f.seen(60)
	f.merge()
	ea, _ := f.evidenceOf(a)
	eb, _ := f.evidenceOf(b)
	if ea != "both" || eb != "log" {
		t.Errorf("edits = %q, %q: one rename can confirm only the first edit", ea, eb)
	}
	// the second edit has no event of its own, and the kernel stream is past its
	// end: it is settled as logged-only (a finding), not kept open for ever
	if n := f.count(`SELECT settled FROM call_links WHERE tool_use_id = 'e2'`); n != 1 {
		t.Error("an unconfirmed edit stayed open after the kernel moved past it")
	}
}

func TestTwoEditsAreConfirmedByTwoRenamesInOrder(t *testing.T) {
	f := newFixture(t)
	a := f.fileAction("e1", "update", "/w/a.go", 20, 21)
	b := f.fileAction("e2", "update", "/w/a.go", 22, 23)
	f.kernelFile("root", "file_rename", "/w/a.go.tmp.1.aaaa", 20)
	f.kernelFile("root", "file_rename", "/w/a.go.tmp.1.bbbb", 22)
	f.seen(5)
	f.seen(60)
	f.merge()
	_, ea := f.evidenceOf(a)
	_, eb := f.evidenceOf(b)
	if ea == nil || eb == nil || *ea == *eb {
		t.Errorf("edits claimed events %v and %v: each edit takes its own rename", ea, eb)
	}
}

func TestAFileEventOfAnotherProcessOrPathOrTimeDoesNotConfirm(t *testing.T) {
	f := newFixture(t)
	f.spawn("other", "root", 900, "/bin/cat", []string{"/w/a.go"}, 5_000) // a command, not the agent process
	rd := f.fileAction("r1", "read", "/w/a.go", 10, 11)
	f.kernelFile("other", "file_open", "/w/a.go", 10)    // another process opened it
	f.kernelFile("root", "file_open", "/w/b.go", 10)     // another file
	f.kernelFile("root", "file_open", "/w/a.go", 40)     // long after the call ended
	f.kernelFile("root", "file_rename", "/w/a.go.x", 10) // not a temp-file rename, and not a read
	f.seen(5)
	f.seen(60)
	f.merge()
	if ev, _ := f.evidenceOf(rd); ev != "log" {
		t.Errorf("read = %q: nothing the agent process did to that file in the call's window", ev)
	}
}

func TestAPendingFileCallIsNotSettled(t *testing.T) {
	f := newFixture(t)
	f.tx(func(tx *store.Tx) error { // awaiting approval: no result yet
		return tx.AddAction(f.ctx, store.Action{SessionID: f.sid, PromptID: f.pid, TS: ts(10), Resource: "file", Action: "update",
			Path: "/w/a.go", Evidence: "log", ToolUseID: "e1"})
	})
	f.seen(5)
	f.seen(60)
	f.merge()
	if n := f.count(`SELECT COUNT(*) FROM call_links WHERE tool_use_id = 'e1' AND settled = 1`); n != 0 {
		t.Error("a call waiting for approval was settled")
	}
	// the edit happens later, after the approval, and is confirmed then
	f.kernelFile("root", "file_rename", "/w/a.go.tmp.1.aaaa", 3000)
	f.seen(3100)
	f.merge()
	var ev string
	f.st.DB.QueryRow(`SELECT evidence FROM actions WHERE tool_use_id = 'e1'`).Scan(&ev)
	if ev != "both" {
		t.Errorf("approved edit = %q", ev)
	}
}

func TestAFileCallOverBeforeTheKernelWatchedIsSettled(t *testing.T) {
	f := newFixture(t)
	rd := f.fileAction("r1", "read", "/w/a.go", 1, 2)
	f.seen(1000)
	f.merge()
	if ev, _ := f.evidenceOf(rd); ev != "log" {
		t.Errorf("read = %q", ev)
	}
	if n := f.count(`SELECT settled FROM call_links WHERE tool_use_id = 'r1'`); n != 1 {
		t.Error("a call over before the kernel was recording stayed open")
	}
}
