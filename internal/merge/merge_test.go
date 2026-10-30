package merge

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

var t0 = time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)

func ts(sec int) string { return dbtime.Format(t0.Add(time.Duration(sec) * time.Second)) }

type fixture struct {
	t   *testing.T
	st  *store.Store
	ctx context.Context
	sid int64
	pid int64
}

var pidSeq int64

// nextPID gives each test process its own pid: a child with its parent's pid is
// the same program run again (see store.Unexplained).
func nextPID() int64 {
	pidSeq++
	return pidSeq
}

// newFixture has one session with a prompt at t0 and an agent root process
// "root" attributed to it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{t: t, st: st, ctx: context.Background()}
	f.tx(func(tx *store.Tx) error {
		var err error
		f.sid, err = tx.UpsertSession(f.ctx, store.Session{Agent: "claude", AgentSessionID: "s", Cwd: "/w", TS: ts(0)})
		if err != nil {
			return err
		}
		f.pid, err = tx.AddPrompt(f.ctx, f.sid, "do it", ts(0))
		return err
	})
	f.proc("root", "", "/opt/claude", nil, 0)
	return f
}

func (f *fixture) tx(fn func(*store.Tx) error) {
	f.t.Helper()
	tx, err := f.st.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) proc(execID, parent, binary string, argv []string, start int) {
	f.t.Helper()
	j, _ := json.Marshal(argv)
	f.tx(func(tx *store.Tx) error {
		if err := tx.AddProcess(f.ctx, store.Process{ExecID: execID, Collector: "tetragon", PID: nextPID(), ParentExecID: parent, Binary: binary, Argv: string(j), StartedAt: ts(start)}); err != nil {
			return err
		}
		return tx.AttributeProcess(f.ctx, execID, f.sid, f.pid)
	})
}

// wrapper is a Claude Code Bash shell for command, started at start.
func (f *fixture) wrapper(execID, command string, start int) {
	f.t.Helper()
	f.proc(execID, "root", "/bin/bash", []string{"-c", "source /snap.sh 2>/dev/null || true && eval '" + command + "' < /dev/null && pwd -P >| /tmp/cwd"}, start)
}

// call is a logged Bash call split into parts, issued at `at`.
func (f *fixture) call(tool, command string, parts []string, at int) {
	f.t.Helper()
	f.tx(func(tx *store.Tx) error {
		for i, p := range parts {
			a := store.Action{SessionID: f.sid, PromptID: f.pid, TS: ts(at), Resource: "command", Action: "execute", Path: p, Evidence: "log", ToolUseID: tool, Part: i}
			if i == 0 {
				a.CallCommand = command
			}
			if err := tx.AddAction(f.ctx, a); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *fixture) result(tool string, at int) {
	f.t.Helper()
	f.tx(func(tx *store.Tx) error {
		_, err := tx.ApplyToolResult(f.ctx, store.ToolResult{ToolUseID: tool, TS: ts(at)})
		return err
	})
}

// seen says the kernel has been recording up to sec and the log up to sec.
func (f *fixture) seen(sec int) {
	f.t.Helper()
	f.tx(func(tx *store.Tx) error {
		if _, err := tx.AppendKernel(f.ctx, store.KernelEvent{ExecID: "root", TS: ts(sec), Type: "file_open", Path: "/x", Payload: "{}"}); err != nil {
			return err
		}
		_, err := tx.UpsertSession(f.ctx, store.Session{Agent: "claude", AgentSessionID: "s", TS: ts(sec)})
		return err
	})
}

func (f *fixture) merge() int {
	f.t.Helper()
	n, err := (&Merger{St: f.st}).Once(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

// action returns the evidence and exec id of the action whose path is text.
func (f *fixture) action(text string) (evidence, exec string, ok bool) {
	f.t.Helper()
	var e, x *string
	err := f.st.DB.QueryRow(`SELECT evidence, exec_id FROM actions WHERE path = ? ORDER BY id LIMIT 1`, text).Scan(&e, &x)
	if err != nil {
		return "", "", false
	}
	if x != nil {
		exec = *x
	}
	return *e, exec, true
}

func (f *fixture) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.st.DB.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestACallPairsWithItsShellAndItsPartsWithTheShellsChildren(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `gofmt -l . ; GOFLAGS=-p=1 go test ./...`, []string{"gofmt -l .", "GOFLAGS=-p=1 go test ./..."}, 10)
	f.wrapper("sh1", `gofmt -l . ; GOFLAGS=-p=1 go test ./...`, 12)
	f.proc("fmt", "sh1", "/usr/bin/gofmt", []string{"-l", "."}, 12)
	f.proc("go", "sh1", "/usr/local/go/bin/go", []string{"test", "./..."}, 13)
	f.proc("vet", "go", "/usr/local/go/pkg/vet", []string{"-all"}, 14) // a grandchild: no action of its own
	f.seen(30)

	f.merge()
	for text, exec := range map[string]string{"gofmt -l .": "fmt", "GOFLAGS=-p=1 go test ./...": "go"} {
		if ev, x, ok := f.action(text); !ok || ev != "both" || x != exec {
			t.Errorf("%q = %q %q %v, want both %s", text, ev, x, ok, exec)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM actions`); n != 2 {
		t.Errorf("%d actions, want 2 (no kernel-only row for the shell or its children)", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM call_links WHERE tool_use_id = 't1' AND shell_exec_id = 'sh1'`); n != 1 {
		t.Error("call not linked to its shell")
	}
	if f.merge() != 0 {
		t.Error("a second pass changed things")
	}
}

func TestACallWaitingForApprovalMatchesWheneverItsShellAppears(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `make build`, []string{"make build"}, 10) // no result yet
	f.seen(20)
	f.merge() // nothing to match: approval pending

	three := 3 * 24 * 3600
	f.wrapper("sh1", `make build`, three)
	f.proc("mk", "sh1", "/usr/bin/make", []string{"build"}, three+1)
	f.seen(three + 5)
	f.merge()
	if ev, x, _ := f.action("make build"); ev != "both" || x != "mk" {
		t.Errorf("make build = %q %q after days of waiting", ev, x)
	}
}

func TestADeniedCallDoesNotClaimALaterIdenticalRun(t *testing.T) {
	f := newFixture(t)
	f.call("denied", `rm -rf build`, []string{"rm -rf build"}, 10)
	f.result("denied", 12) // refused straight away
	f.wrapper("sh1", `rm -rf build`, 3600)
	f.proc("rm", "sh1", "/bin/rm", []string{"-rf", "build"}, 3601)
	f.seen(4000)
	f.merge()

	if ev, x, _ := f.action("rm -rf build"); ev != "log" || x != "" {
		t.Errorf("denied call = %q %q, want log only", ev, x)
	}
	// the run the log doesn't explain is a kernel action showing the command
	var kev, kexec string
	if err := f.st.DB.QueryRow(`SELECT evidence, exec_id FROM actions WHERE evidence = 'kernel'`).Scan(&kev, &kexec); err != nil || kexec != "sh1" {
		t.Errorf("kernel action = %q %q, %v", kev, kexec, err)
	}
}

func TestAnInterruptedCallEndsWithItsPrompt(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `sleep 5`, []string{"sleep 5"}, 10) // never answered
	f.tx(func(tx *store.Tx) error {
		_, err := tx.AddPrompt(f.ctx, f.sid, "never mind", ts(100)) // ends the first prompt
		return err
	})
	f.wrapper("sh1", `sleep 5`, 5000)
	f.proc("sl", "sh1", "/bin/sleep", []string{"5"}, 5001)
	f.seen(6000)
	f.merge()
	if ev, _, _ := f.action("sleep 5"); ev != "log" {
		t.Errorf("sleep 5 = %q: a call from an ended prompt claimed a later shell", ev)
	}
}

func TestEqualCallsPairInOrder(t *testing.T) {
	f := newFixture(t)
	f.seen(5) // the kernel is being watched from the start
	f.call("a", `go test`, []string{"go test"}, 10)
	f.result("a", 20)
	f.call("b", `go test`, []string{"go test"}, 30)
	f.wrapper("sh1", `go test`, 11)
	f.proc("g1", "sh1", "/usr/bin/go", []string{"test"}, 12)
	f.wrapper("sh2", `go test`, 31)
	f.proc("g2", "sh2", "/usr/bin/go", []string{"test"}, 32)
	f.seen(60)
	f.merge()
	for tool, want := range map[string]string{"a": "g1", "b": "g2"} {
		var x string
		f.st.DB.QueryRow(`SELECT exec_id FROM actions WHERE tool_use_id = ?`, tool).Scan(&x)
		if x != want {
			t.Errorf("call %s ran as %q, want %q", tool, x, want)
		}
	}
}

func TestAPartWithNoExecStaysLogOnly(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `cd sub && ls`, []string{"cd sub", "ls"}, 10) // cd is a builtin: no exec
	f.wrapper("sh1", `cd sub && ls`, 11)
	f.proc("ls", "sh1", "/bin/ls", nil, 12)
	f.seen(30)
	f.merge()
	if ev, _, _ := f.action("cd sub"); ev != "log" {
		t.Errorf("cd = %q", ev)
	}
	if ev, x, _ := f.action("ls"); ev != "both" || x != "ls" {
		t.Errorf("ls = %q %q", ev, x)
	}
}

func TestUnexplainedCommandsOfTheAgentAreKernelActions(t *testing.T) {
	f := newFixture(t)
	f.proc("id", "root", "/usr/bin/id", []string{"-u"}, 10)
	f.seen(30)
	f.merge()
	ev, x, ok := f.action("id -u")
	if !ok || ev != "kernel" || x != "id" {
		t.Errorf("unexplained id = %q %q %v", ev, x, ok)
	}
	if n := f.count(`SELECT action_count FROM prompts WHERE id = ?`, f.pid); n != 0 {
		t.Errorf("prompt action count = %d: kernel-only commands are not counted here", n)
	}
	if f.merge() != 0 {
		t.Error("a second pass added the action again")
	}
}

func TestBackgroundCommandsBelongToTheAgentNotAPrompt(t *testing.T) {
	f := newFixture(t)
	f.proc("git", "root", "/usr/bin/git", []string{"-c", "core.askPass=", "status", "--short"}, 10)
	f.proc("cfg", "root", "/usr/bin/git", []string{"config", "--get", "remote.origin.url"}, 11)
	f.proc("push", "root", "/usr/bin/git", []string{"push"}, 12) // not housekeeping
	f.seen(30)
	f.merge()

	for exec, key := range map[string]string{"git": "git status", "cfg": "git config --get", "push": ""} {
		var got *string
		if err := f.st.DB.QueryRow(`SELECT background FROM process_attribution WHERE exec_id = ?`, exec).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != key {
			t.Errorf("%s: background = %v, want %q", exec, got, key)
		}
	}
	if _, _, ok := f.action("git -c core.askPass= status --short"); ok {
		t.Error("a background command became an action")
	}
	if ev, _, ok := f.action("git push"); !ok || ev != "kernel" {
		t.Errorf("an unknown git command = %q %v, want a kernel action", ev, ok)
	}
	var root string
	f.st.DB.QueryRow(`SELECT root_exec_id FROM process_attribution WHERE exec_id = 'git'`).Scan(&root)
	if root != "root" {
		t.Errorf("root_exec_id = %q, want the agent process", root)
	}

	// a kernel action recorded before the rules knew the command is dropped
	f.tx(func(tx *store.Tx) error {
		if err := tx.AddProcess(f.ctx, store.Process{ExecID: "old", Collector: "tetragon", PID: nextPID(), ParentExecID: "root", Binary: "/usr/bin/git", Argv: `["rev-parse","HEAD"]`, StartedAt: ts(13)}); err != nil {
			return err
		}
		if err := tx.AttributeProcess(f.ctx, "old", f.sid, f.pid); err != nil {
			return err
		}
		return tx.AddKernelAction(f.ctx, store.Action{SessionID: f.sid, PromptID: f.pid, ExecID: "old", TS: ts(13), Resource: "command", Action: "execute", Path: "git rev-parse HEAD"})
	})
	f.merge()
	if _, _, ok := f.action("git rev-parse HEAD"); ok {
		t.Error("an action for a now-known background command was kept")
	}
}

func TestAKernelExecWaitsForTheLogToReachIt(t *testing.T) {
	f := newFixture(t)
	f.proc("git", "root", "/usr/bin/id", []string{}, 100)
	f.tx(func(tx *store.Tx) error {
		_, err := tx.AppendKernel(f.ctx, store.KernelEvent{ExecID: "git", TS: ts(101), Type: "exec", Payload: "{}"})
		return err
	}) // the kernel is ahead of the log (session ends at t0)
	f.merge()
	if n := f.count(`SELECT COUNT(*) FROM actions`); n != 0 {
		t.Errorf("%d actions before the log caught up", n)
	}
	f.seen(110) // the log moves on
	f.merge()
	if n := f.count(`SELECT COUNT(*) FROM actions WHERE evidence = 'kernel'`); n != 1 {
		t.Errorf("%d kernel actions after the log caught up, want 1", n)
	}
}

func TestCallsAreSettledOnceNothingCanChange(t *testing.T) {
	f := newFixture(t)
	f.call("old", `ls`, []string{"ls"}, 1)
	f.result("old", 2)
	f.call("pending", `ls /tmp`, []string{"ls /tmp"}, 3)
	f.seen(1000) // the kernel's first event is at 1000: "old" ended before that
	f.merge()
	if n := f.count(`SELECT settled FROM call_links WHERE tool_use_id = 'old'`); n != 1 {
		t.Error("a call over before the kernel was watched stayed open")
	}
	if n := f.count(`SELECT COUNT(*) FROM call_links WHERE tool_use_id = 'pending' AND settled = 1`); n != 0 {
		t.Error("a call still waiting for approval was settled")
	}

	f.result("pending", 1010)
	f.seen(1100)
	f.merge()
	if n := f.count(`SELECT COUNT(*) FROM call_links WHERE tool_use_id = 'pending' AND settled = 1`); n != 1 {
		t.Error("a finished call stayed open after the kernel moved past it")
	}
}

func TestNoKernelDataMeansNothingToMerge(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `ls`, []string{"ls"}, 10)
	if n := f.merge(); n != 0 {
		t.Errorf("merged %d with no kernel events", n)
	}
	if n := f.count(`SELECT COUNT(*) FROM call_links`); n != 0 {
		t.Error("settled calls before any kernel event")
	}
}

func TestRunStopsWithContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	m := &Merger{St: f.st}
	go func() { done <- m.Run(ctx, 5*time.Millisecond) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOnceReportsStoreErrors(t *testing.T) {
	f := newFixture(t)
	f.st.DB.Close()
	if _, err := (&Merger{St: f.st}).Once(f.ctx); err == nil {
		t.Error("closed store gave no error")
	}
}

func TestTextHelpers(t *testing.T) {
	for in, want := range map[string]string{
		`grep -v "^ok\|no test files"`: `grep-v^ok|notestfiles`,
		"a  'b'\n":                     "ab",
	} {
		if got := norm(in); got != want {
			t.Errorf("norm(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"GOFLAGS=-p=1 go test ./...": "go test ./...",
		"/usr/bin/git status":        "git status",
		"A=1 B=2":                    "",
		"1x=2 z":                     "1x=2 z",
	} {
		if got := partCommand(in); got != want {
			t.Errorf("partCommand(%q) = %q, want %q", in, got, want)
		}
	}
	argvJSON := `["-c","source /s && eval 'echo hi' < /dev/null && pwd -P >| /tmp/c"]`
	if got := shellCommand(argvJSON); got != "echo hi" {
		t.Errorf("shellCommand = %q", got)
	}
	// a heredoc keeps its own stdin: the wrapper has no < /dev/null (seen in real data)
	heredoc := `["-c","source /s && eval 'cat > f <<'\"'\"'EOF'\"'\"'\nbody\nEOF\nls | head' && pwd -P >| /tmp/c"]`
	if got := shellCommand(heredoc); !strings.HasSuffix(got, "ls | head") || strings.Contains(got, "pwd -P") {
		t.Errorf("shellCommand(heredoc) = %q", got)
	}
	for _, in := range []string{`["-c","ls"]`, `["ls"]`, `not json`} {
		if got := shellCommand(in); got != "" {
			t.Errorf("shellCommand(%s) = %q, want none", in, got)
		}
	}
	if !strings.Contains(procCommand(store.ProcRef{Binary: "/usr/bin/git", Argv: `["status"]`}), "git status") {
		t.Error("procCommand")
	}
}

func TestSplitArgumentsStillMatchTheLogsText(t *testing.T) {
	// the collector splits a quoted argument at its spaces
	f := newFixture(t)
	f.call("t1", `grep -v "^ok\|no test files" x`, []string{`grep -v "^ok\|no test files" x`}, 10)
	f.wrapper("sh1", `grep -v "^ok\|no test files" x`, 11)
	f.proc("gr", "sh1", "/bin/grep", []string{"-v", `^ok\|no`, "test", "files", "x"}, 12)
	f.seen(30)
	f.merge()
	if ev, x, _ := f.action(`grep -v "^ok\|no test files" x`); ev != "both" || x != "gr" {
		t.Errorf("grep = %q %q", ev, x)
	}
}
