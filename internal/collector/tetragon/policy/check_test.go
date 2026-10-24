package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const boot = 1_000_000 // btime of the fake machine, unix seconds

// fakeProc writes a /proc entry. start is seconds after boot.
func fakeProc(t *testing.T, proc string, pid, ppid int, comm, exe string, start int) {
	t.Helper()
	d := filepath.Join(proc, fmt.Sprint(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// pid (comm) state ppid, 17 fields we do not read, then the start time in ticks
	stat := fmt.Sprintf("%d (%s) S %d %s %d 0 0\n", pid, comm, ppid, strings.Repeat("0 ", 17), start*clockTicks)
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if exe != "" {
		if err := os.Symlink(exe, filepath.Join(d, "exe")); err != nil {
			t.Fatal(err)
		}
	}
}

type env struct {
	home, proc, policy string
	bin                string // the agent binary installed now
}

func newEnv(t *testing.T) env {
	t.Helper()
	home := t.TempDir()
	versions := filepath.Join(home, ".local/share/claude/versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(versions, "2.1.307")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	proc := t.TempDir()
	if err := os.WriteFile(filepath.Join(proc, "stat"), []byte(fmt.Sprintf("cpu 1 2 3\nbtime %d\n", boot)), 0o644); err != nil {
		t.Fatal(err)
	}
	return env{home: home, proc: proc, policy: filepath.Join(t.TempDir(), "sentient.yaml"), bin: bin}
}

// install writes the policy for bins, with the given modification time.
func (e env) install(t *testing.T, bins []string, mtime time.Time) {
	t.Helper()
	text, err := Render(bins)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.policy, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(e.policy, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func (e env) check() Report {
	return Check(CheckOptions{PolicyFile: e.policy, Homes: []string{e.home}, Proc: e.proc})
}

func at(sec int) time.Time { return time.Unix(boot+int64(sec), 0) }

func lineFor(r Report, label string) []Line {
	var out []Line
	for _, l := range r.Lines {
		if l.Label == label {
			out = append(out, l)
		}
	}
	return out
}

func TestCheckPassesWhenPolicyMatchesAndAgentsStartedAfterTetragon(t *testing.T) {
	e := newEnv(t)
	e.install(t, Discover([]string{e.home}), at(50))
	fakeProc(t, e.proc, 10, 1, "tetragon", "/usr/local/bin/tetragon", 100)
	fakeProc(t, e.proc, 20, 1, "claude", e.bin, 200)
	r := e.check()
	if !r.OK() {
		t.Fatalf("report = %+v", r.Lines)
	}
	if l := lineFor(r, "agents"); len(l) != 1 || !strings.Contains(l[0].Text, "1 running") {
		t.Errorf("agents = %+v", l)
	}
}

func TestCheckFlagsAnAgentThatStartedBeforeTetragon(t *testing.T) {
	e := newEnv(t)
	e.install(t, Discover([]string{e.home}), at(50))
	fakeProc(t, e.proc, 10, 1, "tetragon", "/usr/local/bin/tetragon", 100)
	fakeProc(t, e.proc, 20, 1, "claude", e.bin, 60)
	fakeProc(t, e.proc, 21, 20, "claude", e.bin, 70) // its helper copy is not a second session
	r := e.check()
	l := lineFor(r, "agents")
	if r.OK() || len(l) != 1 || l[0].OK || !strings.Contains(l[0].Text, "pid 20") || !strings.Contains(l[0].Text, "Restart it") {
		t.Fatalf("report = %+v", r.Lines)
	}
}

func TestCheckFlagsAPolicyThatDoesNotListTheInstalledAgent(t *testing.T) {
	e := newEnv(t)
	e.install(t, []string{"/old/claude/versions/2.1.305"}, at(50))
	fakeProc(t, e.proc, 10, 1, "tetragon", "/usr/local/bin/tetragon", 100)
	r := e.check()
	l := lineFor(r, "policy")
	if r.OK() || len(l) != 1 || l[0].OK || !strings.Contains(l[0].Text, e.bin) {
		t.Fatalf("report = %+v", r.Lines)
	}
}

func TestCheckFlagsAMissingPolicyFile(t *testing.T) {
	e := newEnv(t)
	fakeProc(t, e.proc, 10, 1, "tetragon", "/usr/local/bin/tetragon", 100)
	l := lineFor(e.check(), "policy")
	if len(l) != 1 || l[0].OK || !strings.Contains(l[0].Text, "not installed") {
		t.Fatalf("policy = %+v", l)
	}
}

func TestCheckFlagsAPolicyChangedAfterTetragonStarted(t *testing.T) {
	e := newEnv(t)
	e.install(t, Discover([]string{e.home}), at(500))
	fakeProc(t, e.proc, 10, 1, "tetragon", "/usr/local/bin/tetragon", 100)
	l := lineFor(e.check(), "tetragon")
	if len(l) != 1 || l[0].OK || !strings.Contains(l[0].Text, "Restart Tetragon") {
		t.Fatalf("tetragon = %+v", l)
	}
}

func TestCheckFlagsTetragonNotRunning(t *testing.T) {
	e := newEnv(t)
	e.install(t, Discover([]string{e.home}), at(50))
	l := lineFor(e.check(), "tetragon")
	if len(l) != 1 || l[0].OK || !strings.Contains(l[0].Text, "not running") {
		t.Fatalf("tetragon = %+v", l)
	}
}

func TestCheckWithNoAgentInstalled(t *testing.T) {
	r := Check(CheckOptions{PolicyFile: "x", Homes: []string{t.TempDir()}, Proc: t.TempDir()})
	if r.OK() || len(r.Lines) != 1 || r.Lines[0].Label != "agent" {
		t.Errorf("report = %+v", r.Lines)
	}
}

func TestCheckWithoutProc(t *testing.T) {
	e := newEnv(t)
	e.install(t, Discover([]string{e.home}), at(50))
	r := Check(CheckOptions{PolicyFile: e.policy, Homes: []string{e.home}, Proc: filepath.Join(t.TempDir(), "nope")})
	if l := lineFor(r, "tetragon"); r.OK() || len(l) != 1 || !strings.Contains(l[0].Text, "cannot read") {
		t.Errorf("report = %+v", r.Lines)
	}
}

func TestPolicyBinariesReadsWhatRenderWrote(t *testing.T) {
	want := []string{"/a/claude", "/b/versions/1"}
	text, err := Render(want)
	if err != nil {
		t.Fatal(err)
	}
	got := PolicyBinaries(text)
	// the template names the agents in several selectors; each lists them all
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if len(seen) != 2 || !seen["/a/claude"] || !seen["/b/versions/1"] {
		t.Errorf("binaries = %v", got)
	}
}

func TestBootTimeNeedsBtime(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte("cpu 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bootTime(d); err == nil {
		t.Error("no error without btime")
	}
}
