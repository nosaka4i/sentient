package es

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// recSink records what the normalizer reports.
type recSink struct {
	procs  []store.Process
	exits  []exitRec
	events []store.KernelEvent
	fail   error
}

type exitRec struct {
	id   string
	code int
}

func (s *recSink) Process(_ context.Context, p store.Process) error {
	s.procs = append(s.procs, p)
	return s.fail
}
func (s *recSink) Exit(_ context.Context, id string, _ time.Time, code int) error {
	s.exits = append(s.exits, exitRec{id, code})
	return s.fail
}
func (s *recSink) Event(_ context.Context, e store.KernelEvent) error {
	s.events = append(s.events, e)
	return s.fail
}
func (s *recSink) GapOpened(context.Context, time.Time, string) (int64, error) { return 1, nil }
func (s *recSink) GapClosed(context.Context, int64, time.Time) error           { return nil }

// replay feeds testdata/session.jsonl, a sanitized sequence shaped like the
// real captures, through a normalizer.
func replay(t *testing.T) (*recSink, *Normalizer) {
	t.Helper()
	f, err := os.Open("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sink := &recSink{}
	n := NewNormalizer(sink)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m, err := Decode(sc.Bytes())
		if err != nil {
			t.Fatalf("decode %q: %v", sc.Text(), err)
		}
		if err := n.Handle(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	return sink, n
}

func TestSessionProcesses(t *testing.T) {
	sink, n := replay(t)
	if len(sink.procs) != 3 {
		t.Fatalf("%d processes, want 3 (claude, zsh, python): %+v", len(sink.procs), sink.procs)
	}
	claude, zsh, py := sink.procs[0], sink.procs[1], sink.procs[2]
	if claude.ExecID != "es:6258:1:1794640830" || claude.Collector != Name || claude.ParentExecID != "" {
		t.Errorf("claude: %+v", claude)
	}
	if zsh.ParentExecID != claude.ExecID || zsh.Binary != "/bin/zsh" || zsh.UID != 501 {
		t.Errorf("zsh: %+v", zsh)
	}
	// python was forked by zsh and exec'd; its parent is zsh's exec
	if py.ParentExecID != zsh.ExecID || !strings.HasSuffix(py.Binary, "/Python") {
		t.Errorf("python: %+v", py)
	}
	if !strings.Contains(zsh.Argv, "eval 'python3 write_data.py && rm data.txt'") {
		t.Errorf("argv not kept verbatim: %s", zsh.Argv)
	}
	// claude is still running; the python and zsh images ended
	if n.Known() != 1 {
		t.Errorf("%d processes still tracked, want 1 (claude)", n.Known())
	}
}

func TestSessionExits(t *testing.T) {
	sink, _ := replay(t)
	want := []exitRec{{"es:6310:2:1794640832", 0}, {"es:6301:2:1794640831", 1}}
	if len(sink.exits) != len(want) {
		t.Fatalf("exits %+v, want %+v", sink.exits, want)
	}
	for i := range want {
		if sink.exits[i] != want[i] {
			t.Errorf("exit %d = %+v, want %+v", i, sink.exits[i], want[i])
		}
	}
}

func TestSessionEvents(t *testing.T) {
	sink, _ := replay(t)
	byType := map[string][]store.KernelEvent{}
	for _, e := range sink.events {
		byType[e.Type] = append(byType[e.Type], e)
	}
	if len(byType["exec"]) != 3 {
		t.Errorf("%d exec events, want 3", len(byType["exec"]))
	}
	// the write by the forked child, before its exec, belongs to the parent's exec_id
	w := byType["file_write"]
	if len(w) != 1 || w[0].ExecID != "es:6301:2:1794640831" || w[0].Path != "/Users/dev/cctest/data.txt" {
		t.Errorf("file_write: %+v", w)
	}
	// the agent's own edit is a temp file plus a rename; the destination is Path2
	r := byType["file_rename"]
	if len(r) != 1 || r[0].ExecID != "es:6258:1:1794640830" || r[0].Path2 != "/private/tmp/outside-test.txt" ||
		r[0].Path != "/private/tmp/outside-test.txt.tmp.6258.ab12" {
		t.Errorf("file_rename: %+v", r)
	}
	if len(byType["file_create"]) != 2 || len(byType["file_unlink"]) != 1 {
		t.Errorf("create %d unlink %d", len(byType["file_create"]), len(byType["file_unlink"]))
	}
	o := byType["file_open"]
	if len(o) != 2 || !strings.Contains(o[0].Payload, `"perm":"r"`) || !strings.Contains(o[1].Payload, `"perm":"rw"`) {
		t.Errorf("file_open: %+v", o)
	}
	// nothing from a process that is not in the tree
	for _, e := range sink.events {
		if strings.Contains(e.Path, "not-in-the-tree") {
			t.Errorf("event for an untracked process: %+v", e)
		}
	}
}

func TestPerm(t *testing.T) {
	for flags, want := range map[int64]string{0: "r", 1: "r", 2: "w", 3: "rw", 16908293: "r", 16777218: "w", 160 | 3: "rw"} {
		if got := perm(flags); got != want {
			t.Errorf("perm(%d) = %q, want %q", flags, got, want)
		}
	}
}

func TestFileEventOfAProcessStartedBeforeTheDaemonAdoptsIt(t *testing.T) {
	sink := &recSink{}
	n := NewNormalizer(sink)
	ctx := context.Background()
	m := Message{T: KindFile, TSSec: 50, PID: 8, PIDVer: 3, Start: 40, UID: 501, Exe: "/bin/zsh", Op: OpWrite, Path: "/Users/dev/x"}
	for i := 0; i < 2; i++ { // the second event must not report the process again
		if err := n.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.procs) != 1 || sink.procs[0].ExecID != "es:8:3:40" || sink.procs[0].Binary != "/bin/zsh" || sink.procs[0].Argv != "[]" {
		t.Errorf("adopted process: %+v", sink.procs)
	}
	if len(sink.events) != 2 || sink.events[0].ExecID != "es:8:3:40" {
		t.Errorf("events: %+v", sink.events)
	}
	// a message with no executable cannot be adopted
	if err := n.Handle(ctx, Message{T: KindFile, PID: 9, PIDVer: 1, Op: OpWrite, Path: "/y"}); err != nil || len(sink.events) != 2 {
		t.Errorf("%v %+v", err, sink.events)
	}
	// the adopted process links to a parent the normalizer knows
	n.Handle(ctx, Message{T: KindFile, PID: 10, PIDVer: 1, PPID: 8, PPIDVer: 3, Exe: "/bin/ls", Op: OpWrite, Path: "/z"})
	if got := sink.procs[len(sink.procs)-1].ParentExecID; got != "es:8:3:40" {
		t.Errorf("parent of the adopted child = %q", got)
	}
	n.Reset()
	if n.Known() != 0 {
		t.Errorf("%d tracked after Reset", n.Known())
	}
	adoptErr := errors.New("disk full")
	sink.fail = adoptErr
	if err := n.Handle(ctx, m); !errors.Is(err, adoptErr) {
		t.Errorf("adopt with a failing sink: %v", err)
	}
}

func TestForkOfUnknownParentIsIgnored(t *testing.T) {
	sink := &recSink{}
	n := NewNormalizer(sink)
	ctx := context.Background()
	n.Handle(ctx, Message{V: 1, T: KindFork, PID: 5, PIDVer: 1, PPID: 4, PPIDVer: 1})
	n.Handle(ctx, Message{V: 1, T: KindFile, PID: 5, PIDVer: 1, Op: OpWrite, Path: "/x"})
	if n.Known() != 0 || len(sink.events) != 0 {
		t.Errorf("an unknown parent's child was tracked: %d, %+v", n.Known(), sink.events)
	}
}

func TestIncompleteFileMessagesAreDropped(t *testing.T) {
	sink := &recSink{}
	n := NewNormalizer(sink)
	ctx := context.Background()
	n.Handle(ctx, Message{V: 1, T: KindExec, PID: 5, PIDVer: 1, Start: 10, Exe: "/bin/x"})
	sink.events = nil
	for _, m := range []Message{
		// no path
		{T: KindFile, PID: 5, PIDVer: 1, Op: OpWrite},
		// a rename with no destination
		{T: KindFile, PID: 5, PIDVer: 1, Op: OpRename, Path: "/a"},
		// an op we do not record
		{T: KindFile, PID: 5, PIDVer: 1, Op: "chmod", Path: "/a"},
		// an exit of a process never seen
		{T: KindExit, PID: 77, PIDVer: 1},
		// kinds that are not about a process
		{T: KindHello}, {T: KindPing}, {T: KindStats}, {T: "something"},
	} {
		if err := n.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.events) != 0 || len(sink.exits) != 0 {
		t.Errorf("unexpected output: %+v %+v", sink.events, sink.exits)
	}
}

func TestExecWithoutArgsAndStart(t *testing.T) {
	sink := &recSink{}
	n := NewNormalizer(sink)
	if err := n.Handle(context.Background(), Message{V: 1, T: KindExec, TSSec: 100, PID: 5, PIDVer: 1, Exe: "/bin/x"}); err != nil {
		t.Fatal(err)
	}
	p := sink.procs[0]
	if p.Argv != "[]" || p.StartedAt == "" {
		t.Errorf("%+v", p)
	}
}

func TestPidReuseDropsTheOldImage(t *testing.T) {
	sink := &recSink{}
	n := NewNormalizer(sink)
	ctx := context.Background()
	n.Handle(ctx, Message{T: KindExec, PID: 5, PIDVer: 1, Start: 10, Exe: "/bin/a"})
	n.Handle(ctx, Message{T: KindExec, PID: 5, PIDVer: 4, Start: 20, Exe: "/bin/b"}) // the pid came back
	if n.Known() != 1 {
		t.Errorf("%d tracked, want 1", n.Known())
	}
}

func TestSinkErrorsStopTheNormalizer(t *testing.T) {
	boom := errors.New("disk full")
	for _, m := range []Message{
		{T: KindExec, PID: 5, PIDVer: 1, Start: 10, Exe: "/bin/x"},
	} {
		n := NewNormalizer(&recSink{fail: boom})
		if err := n.Handle(context.Background(), m); !errors.Is(err, boom) {
			t.Errorf("exec: %v", err)
		}
	}
	sink := &recSink{}
	n := NewNormalizer(sink)
	ctx := context.Background()
	n.Handle(ctx, Message{T: KindExec, PID: 5, PIDVer: 1, Start: 10, Exe: "/bin/x"})
	sink.fail = boom
	if err := n.Handle(ctx, Message{T: KindFile, PID: 5, PIDVer: 1, Op: OpWrite, Path: "/a"}); !errors.Is(err, boom) {
		t.Errorf("file: %v", err)
	}
	if err := n.Handle(ctx, Message{T: KindExit, PID: 5, PIDVer: 1}); !errors.Is(err, boom) {
		t.Errorf("exit: %v", err)
	}
}

func TestDecode(t *testing.T) {
	m, err := Decode([]byte(`{"v":1,"t":"file","op":"open","path":"/a","flags":3,"future_field":true}`))
	if err != nil || m.Op != OpOpen || m.Flags != 3 {
		t.Errorf("%+v %v", m, err)
	}
	for _, bad := range []string{`not json`, `{"t":"exec"}`, `{"v":1}`} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("Decode(%q) succeeded", bad)
		}
	}
	if _, err := Decode([]byte(`{"v":2,"t":"exec"}`)); !errors.Is(err, ErrVersion) {
		t.Errorf("a newer schema: %v", err)
	}
}
