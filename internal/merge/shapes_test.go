package merge

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/background"
	"github.com/nosaka4i/sentient/internal/dbtime"
	"github.com/nosaka4i/sentient/internal/store"
)

// These fixtures copy the shapes seen in real Claude Code + Tetragon data, which
// the plain fixtures did not have: a process first appears as a stub exec of its
// parent's binary before the program it runs (same pid, same parent), a pipeline
// forks a subshell per stage, and the shell's grep and find run as the agent's
// own binary with no parent recorded.

// atMs is a moment t0 plus ms milliseconds.
func atMs(ms int) string { return dbtime.Format(t0.Add(time.Duration(ms) * time.Millisecond)) }

// spawn records a process with an explicit pid, directory and start, attributed
// to the fixture's session unless it has no parent (an orphan the collector
// could not link to anything).
func (f *fixture) spawn(execID, parent string, pid int64, binary string, argv []string, startMs int) {
	f.t.Helper()
	j, _ := json.Marshal(argv)
	f.tx(func(tx *store.Tx) error {
		p := store.Process{ExecID: execID, Collector: "tetragon", PID: pid, ParentExecID: parent, Binary: binary, Argv: string(j), Cwd: "/w", StartedAt: atMs(startMs)}
		if err := tx.AddProcess(f.ctx, p); err != nil {
			return err
		}
		if parent == "" {
			return nil
		}
		return tx.AttributeProcess(f.ctx, execID, f.sid, f.pid)
	})
}

// shell is a wrapper shell with its cwd set, started at startMs.
func (f *fixture) shell(execID string, pid int64, command string, startMs int) {
	f.t.Helper()
	f.spawn(execID, "root", pid, "/bin/bash", []string{"-c", "source /snap.sh 2>/dev/null || true && eval '" + command + "' < /dev/null && pwd -P >| /tmp/cwd"}, startMs)
}

func (f *fixture) background(exec string) (key string, checked bool) {
	f.t.Helper()
	var k *string
	if err := f.st.DB.QueryRow(`SELECT background FROM process_attribution WHERE exec_id = ?`, exec).Scan(&k); err != nil {
		f.t.Fatal(err)
	}
	if k == nil {
		return "", false
	}
	return *k, true
}

const claudeBin = "/home/u/.local/share/claude/versions/2.1.9"

var grepFlags = []string{"-G", "--ignore-files", "--hidden", "-I", "--exclude-dir=.git", "--exclude-dir=.svn", "--exclude-dir=.hg", "--exclude-dir=.bzr", "--exclude-dir=.jj", "--exclude-dir=.sl"}
var findFlags = []string{"-S", "dfs", "-regextype", "findutils-default"}

func TestAStubIsNotACommand(t *testing.T) {
	f := newFixture(t)
	// the agent forks a helper (reported as an exec of its own binary, with its own
	// arguments) and the helper execs git: one pid, one parent, two execs
	f.spawn("stub", "root", 500, claudeBin, []string{"-c"}, 10_000)
	f.spawn("git", "root", 500, "/usr/bin/git", []string{"-c", "core.askPass=", "status"}, 10_001)
	f.spawn("stub2", "root", 501, claudeBin, []string{"-c"}, 20_000)
	f.spawn("id", "root", 501, "/usr/bin/id", []string{"-u"}, 20_002)
	f.seen(60)
	f.merge()

	if k, ok := f.background("git"); !ok || k != "git status" {
		t.Errorf("git = %q (checked %v), want the background command", k, ok)
	}
	for _, stub := range []string{"stub", "stub2"} {
		if k, ok := f.background(stub); !ok || k != "" {
			t.Errorf("%s = %q (checked %v): a stub is checked and is nothing", stub, k, ok)
		}
	}
	// the stub of a command nothing explains does not count either: only id does
	if n := f.count(`SELECT COUNT(*) FROM actions WHERE evidence = 'kernel'`); n != 1 {
		t.Errorf("%d kernel actions, want 1 (id)", n)
	}
	if ev, x, ok := f.action("id -u"); !ok || ev != "kernel" || x != "id" {
		t.Errorf("id = %q %q %v", ev, x, ok)
	}
}

func TestAPipelinesForkedSubshellsAreNotCommands(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `ls | head`, []string{"ls", "head"}, 10)
	f.shell("sh", 600, `ls | head`, 11_000)
	// one subshell per stage: an exec of bash with the wrapper's arguments, then the stage's program
	f.spawn("fork1", "sh", 601, "/bin/bash", []string{"-c", "same"}, 11_010)
	f.spawn("ls", "sh", 601, "/bin/ls", nil, 11_011)
	f.spawn("fork2", "sh", 602, "/bin/bash", []string{"-c", "same"}, 11_012)
	f.spawn("hd", "sh", 602, "/usr/bin/head", nil, 11_013)
	f.seen(60)
	f.merge()

	for text, exec := range map[string]string{"ls": "ls", "head": "hd"} {
		if ev, x, ok := f.action(text); !ok || ev != "both" || x != exec {
			t.Errorf("%q = %q %q %v, want both %s", text, ev, x, ok, exec)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM actions WHERE evidence = 'kernel'`); n != 0 {
		t.Errorf("%d kernel actions: the forks are not commands", n)
	}
}

func TestTheShellsGrepAndFindRunAsTheAgentsBinary(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `grep -n foo a.go b.go | head`, []string{"grep -n foo a.go b.go", "head"}, 10)
	f.call("t2", `find . -name x`, []string{"find . -name x"}, 20)
	f.shell("sh1", 700, `grep -n foo a.go b.go | head`, 11_000)
	f.spawn("fork", "sh1", 701, "/bin/bash", []string{"-c", "same"}, 11_010)
	f.spawn("hd", "sh1", 701, "/usr/bin/head", nil, 11_011)
	f.shell("sh2", 710, `find . -name x`, 21_000)
	// the shell's grep and find: the agent's binary, injected flags, no parent recorded
	f.spawn("ug", "", 702, claudeBin, append(append([]string{}, grepFlags...), "-n", "foo", "a.go", "b.go"), 11_020)
	f.spawn("bfs", "", 711, claudeBin, append(append([]string{}, findFlags...), ".", "-name", "x"), 21_020)
	f.tx(func(tx *store.Tx) error {
		if _, err := tx.ExitProcess(f.ctx, "sh1", atMs(11_100), 0); err != nil {
			return err
		}
		_, err := tx.ExitProcess(f.ctx, "sh2", atMs(21_100), 0)
		return err
	})
	f.seen(60)
	f.merge()

	for text, exec := range map[string]string{"grep -n foo a.go b.go": "ug", "head": "hd", "find . -name x": "bfs"} {
		if ev, x, ok := f.action(text); !ok || ev != "both" || x != exec {
			t.Errorf("%q = %q %q %v, want both %s", text, ev, x, ok, exec)
		}
	}
	if n := f.count(`SELECT COUNT(*) FROM actions WHERE evidence = 'kernel'`); n != 0 {
		t.Errorf("%d kernel actions", n)
	}
}

func TestAGrepOutsideTheShellsLifetimeOrDirectoryIsNotItsOwn(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `grep -n foo a.go`, []string{"grep -n foo a.go"}, 10)
	f.shell("sh1", 800, `grep -n foo a.go`, 11_000)
	f.tx(func(tx *store.Tx) error {
		_, err := tx.ExitProcess(f.ctx, "sh1", atMs(11_100), 0)
		return err
	})
	// the same grep, but long after the shell ended, and another in another directory
	f.spawn("late", "", 801, claudeBin, append(append([]string{}, grepFlags...), "-n", "foo", "a.go"), 30_000)
	f.tx(func(tx *store.Tx) error {
		return tx.AddProcess(f.ctx, store.Process{ExecID: "elsewhere", Collector: "tetragon", PID: 802, Binary: claudeBin,
			Argv: mustJSON(append(append([]string{}, grepFlags...), "-n", "foo", "a.go")), Cwd: "/other", StartedAt: atMs(11_050)})
	})
	f.seen(60)
	f.merge()
	if ev, x, _ := f.action("grep -n foo a.go"); ev != "log" || x != "" {
		t.Errorf("grep = %q %q: neither candidate belongs to this shell", ev, x)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAKernelOnlyActionIsReclaimedByTheLoggedPart(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `ls`, []string{"ls"}, 10)
	f.shell("sh", 900, `ls`, 11_000)
	f.spawn("ls", "sh", 901, "/bin/ls", nil, 11_010)
	// an older build recorded the ls as unexplained
	f.tx(func(tx *store.Tx) error {
		return tx.AddKernelAction(f.ctx, store.Action{SessionID: f.sid, PromptID: f.pid, ExecID: "ls", TS: atMs(11_010), Resource: "command", Action: "execute", Path: "ls"})
	})
	f.seen(60)
	f.merge()
	if ev, x, ok := f.action("ls"); !ok || ev != "both" || x != "ls" {
		t.Errorf("ls = %q %q %v", ev, x, ok)
	}
	if n := f.count(`SELECT COUNT(*) FROM actions WHERE exec_id = 'ls'`); n != 1 {
		t.Errorf("%d actions for the exec, want 1: the logged command accounts for it", n)
	}
}

func TestAnImprovedMatcherLooksAgainAtSettledCalls(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `grep -n foo a.go`, []string{"grep -n foo a.go"}, 10)
	f.shell("sh1", 1000, `grep -n foo a.go`, 11_000)
	f.spawn("ug", "", 1001, claudeBin, append(append([]string{}, grepFlags...), "-n", "foo", "a.go"), 11_020)
	f.result("t1", 12) // the call is over, so the matcher can settle it
	f.seen(5)          // the kernel was already watching when it ran
	f.seen(60)
	// an older matcher settled the call without finding the grep
	f.tx(func(tx *store.Tx) error {
		if err := tx.LinkCall(f.ctx, "t1", "sh1"); err != nil {
			return err
		}
		return tx.SettleCall(f.ctx, "t1", matcherVersion-1)
	})
	f.merge()
	if ev, x, ok := f.action("grep -n foo a.go"); !ok || ev != "both" || x != "ug" {
		t.Errorf("grep = %q %q %v: the new matcher should have paired it", ev, x, ok)
	}
	var m int
	f.st.DB.QueryRow(`SELECT matcher FROM call_links WHERE tool_use_id = 't1'`).Scan(&m)
	if m != matcherVersion {
		t.Errorf("settled under matcher %d, want %d", m, matcherVersion)
	}
	// settled under the current matcher: not examined again
	f.tx(func(tx *store.Tx) error {
		calls, err := tx.OpenCalls(f.ctx, matcherVersion)
		if len(calls) != 0 {
			t.Errorf("a call settled by the current matcher is open again: %+v", calls)
		}
		return err
	})
}

func TestACommandTheShellExpandedStillPairs(t *testing.T) {
	f := newFixture(t)
	f.call("t1", `ls internal/*.go && echo $HOME`, []string{"ls internal/*.go", "echo $HOME"}, 10)
	f.shell("sh", 1100, `ls internal/*.go && echo $HOME`, 11_000)
	f.spawn("ls", "sh", 1101, "/bin/ls", []string{"internal/a.go", "internal/b.go"}, 11_010)
	f.spawn("echo", "sh", 1102, "/bin/echo", []string{"/home/u"}, 11_020)
	f.seen(60)
	f.merge()
	for text, exec := range map[string]string{"ls internal/*.go": "ls", "echo $HOME": "echo"} {
		if ev, x, ok := f.action(text); !ok || ev != "both" || x != exec {
			t.Errorf("%q = %q %q %v, want both %s", text, ev, x, ok, exec)
		}
	}
}

func TestExpansionMatchers(t *testing.T) {
	for _, tc := range []struct {
		text, got string
		want      bool
	}{
		{"ls internal/*.go", "lsinternal/a.gointernal/b.go", true},
		{"ls internal/*.go", "lsinternal/a.txt", false},
		{"cat a?.txt", "cata1.txt", true},
		{"echo $HOME", "echo/home/u", true},
		{"echo $HOME", "printf/home/u", false},
	} {
		m := expansion(tc.text)
		if m == nil {
			t.Errorf("%q: no matcher", tc.text)
			continue
		}
		if got := m(norm(tc.text), tc.got); got != tc.want {
			t.Errorf("%q vs %q = %v, want %v", tc.text, tc.got, got, tc.want)
		}
	}
	if expansion("ls -la") != nil || expansion("") != nil {
		t.Error("a plain command needs no expansion matcher")
	}
	if expansion("$X y") != nil || expansion("{a,b} y") != nil || expansion("~/bin/tool x") != nil {
		t.Error("an expansion as the program cannot be matched by program")
	}
	if !background.IsToolRun(mustJSON(grepFlags)) || !background.IsToolRun(mustJSON(findFlags)) {
		t.Error("the redirected grep and find are tool runs")
	}
}
