package tetragon

import (
	"bufio"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cilium/tetragon/api/v1/tetragon"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

// recorder is a Sink that keeps what it is told.
type recorder struct {
	mu     sync.Mutex
	procs  []store.Process
	exits  map[string]int
	events []store.KernelEvent
	gaps   []gapRec
	failOn string // "process" | "event" | "exit" | "gap" | "close": fail that call
}

type gapRec struct {
	reason     string
	opened     time.Time
	closed     time.Time
	closedFlag bool
}

func newRecorder() *recorder { return &recorder{exits: map[string]int{}} }

func (r *recorder) Process(_ context.Context, p store.Process) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn == "process" {
		return errFail
	}
	r.procs = append(r.procs, p)
	return nil
}

func (r *recorder) Exit(_ context.Context, id string, _ time.Time, code int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn == "exit" {
		return errFail
	}
	r.exits[id] = code
	return nil
}

func (r *recorder) Event(_ context.Context, e store.KernelEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn == "event" {
		return errFail
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) GapOpened(_ context.Context, at time.Time, reason string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn == "gap" {
		return 0, errFail
	}
	r.gaps = append(r.gaps, gapRec{reason: reason, opened: at})
	return int64(len(r.gaps)), nil
}

func (r *recorder) GapClosed(_ context.Context, id int64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn == "close" {
		return errFail
	}
	r.gaps[id-1].closed, r.gaps[id-1].closedFlag = at, true
	return nil
}

type failure string

func (f failure) Error() string { return string(f) }

const errFail = failure("sink failed")

// fixture reads testdata/run1.jsonl: a trimmed sample of the evaluation's
// run1 capture of a Claude Code session.
func fixture(t *testing.T) []*tetragon.GetEventsResponse {
	t.Helper()
	f, err := os.Open("testdata/run1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*tetragon.GetEventsResponse
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r tetragon.GetEventsResponse
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, &r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

var ctx = context.Background()

func normalizer(rec *recorder) *Normalizer {
	return NewNormalizer(rec, policy.AgentPrefixes([]string{"/home/ubuntu"}))
}

func TestRun1CaptureIsNormalized(t *testing.T) {
	rec := newRecorder()
	n := normalizer(rec)
	for _, r := range fixture(t) {
		if err := n.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// the claude root and the git it started are recorded; the unrelated ls is not
	if len(rec.procs) != 2 {
		t.Fatalf("%d processes, want 2: %+v", len(rec.procs), rec.procs)
	}
	root, git := rec.procs[0], rec.procs[1]
	if root.Binary != "/home/ubuntu/.local/bin/claude" || root.PID != 155005 || root.ParentExecID != "" || root.Collector != "tetragon" || root.UID != 1000 {
		t.Errorf("root = %+v", root)
	}
	if git.Binary != "/usr/bin/git" || git.ParentExecID != root.ExecID {
		t.Errorf("git = %+v", git)
	}
	if want := `["-p","Run ./z.sh`; len(root.Argv) < len(want) || root.Argv[:len(want)] != want {
		t.Errorf("root argv = %.60s", root.Argv)
	}
	if code, ok := rec.exits[git.ExecID]; !ok || code != 0 {
		t.Errorf("git exit = %d, %v", code, ok)
	}

	byType := map[string]store.KernelEvent{}
	for _, e := range rec.events {
		byType[e.Type] = e
	}
	for _, typ := range []string{"exec", "file_open", "file_unlink", "file_rename", "file_setmode", "dir_create", "dir_delete", "file_create", "net_connect", "kill"} {
		if _, ok := byType[typ]; !ok {
			t.Errorf("no %s event", typ)
		}
	}
	if e := byType["file_unlink"]; e.Path != "/home/ubuntu/.claude/backups/.claude.json.backup.1792195452773" {
		t.Errorf("unlink path = %q", e.Path)
	}
	if e := byType["file_rename"]; e.Path == "" || e.Path2 == "" || e.Path == e.Path2 {
		t.Errorf("rename = %q -> %q", e.Path, e.Path2)
	}
	if e := byType["file_setmode"]; e.Path != "/run/user/1000/cc-socks/155005.sock" || e.Payload != `{"mode":"0600"}` {
		t.Errorf("setmode = %+v", e)
	}
	// a mount-relative entry is joined to its directory
	if e := byType["file_create"]; e.Path != "/run/user/1000/cc-socks/155005.sock" {
		t.Errorf("mknod path = %q", e.Path)
	}
	if e := byType["net_connect"]; e.Path != "[2607:6bc0::10]:443" {
		t.Errorf("connect = %+v", e)
	}
	if e := byType["kill"]; e.Payload != `{"signal":0,"target":134330}` {
		t.Errorf("kill = %+v", e)
	}
	for _, e := range rec.events {
		if e.TS == "" || e.ExecID == "" || e.Payload == "" {
			t.Errorf("incomplete event %+v", e)
		}
	}
}

func proc(id, parent, bin, args string) *tetragon.Process {
	return &tetragon.Process{ExecId: id, ParentExecId: parent, Binary: bin, Arguments: args}
}

func execEvent(p *tetragon.Process) *tetragon.GetEventsResponse {
	return &tetragon.GetEventsResponse{Event: &tetragon.GetEventsResponse_ProcessExec{ProcessExec: &tetragon.ProcessExec{Process: p}}}
}

func exitEvent(p *tetragon.Process, status uint32) *tetragon.GetEventsResponse {
	return &tetragon.GetEventsResponse{Event: &tetragon.GetEventsResponse_ProcessExit{ProcessExit: &tetragon.ProcessExit{Process: p, Status: status}}}
}

func kprobeEvent(p *tetragon.Process, fn string, args ...*tetragon.KprobeArgument) *tetragon.GetEventsResponse {
	return &tetragon.GetEventsResponse{Event: &tetragon.GetEventsResponse_ProcessKprobe{ProcessKprobe: &tetragon.ProcessKprobe{Process: p, FunctionName: fn, Args: args}}}
}

func pathArgOf(p string) *tetragon.KprobeArgument {
	return &tetragon.KprobeArgument{Arg: &tetragon.KprobeArgument_PathArg{PathArg: &tetragon.KprobePath{Path: p}}}
}

func TestOnlyTheAgentTreeIsKept(t *testing.T) {
	rec := newRecorder()
	n := normalizer(rec)
	agent := proc("a", "", "/home/ubuntu/.local/share/claude/versions/2.1.399", "-p hi") // a version never seen before
	stranger := proc("s", "", "/usr/bin/vim", "")
	childOfStranger := proc("sc", "s", "/bin/sh", "")
	child := proc("c", "a", "/bin/sh", "-c ls")
	grandchild := proc("g", "c", "/usr/bin/ls", "")
	for _, p := range []*tetragon.Process{agent, stranger, childOfStranger, child, grandchild} {
		if err := n.Handle(ctx, execEvent(p)); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, p := range rec.procs {
		ids = append(ids, p.ExecID)
	}
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "c" || ids[2] != "g" {
		t.Errorf("kept %v, want [a c g]", ids)
	}
	if rec.procs[2].ParentExecID != "c" {
		t.Errorf("grandchild parent = %q", rec.procs[2].ParentExecID)
	}
	// events and exits of processes outside the tree are dropped
	n.Handle(ctx, kprobeEvent(stranger, "security_path_unlink", pathArgOf("/d"), pathArgOf("/d/f")))
	n.Handle(ctx, exitEvent(stranger, 1))
	n.Handle(ctx, exitEvent(grandchild, 3))
	if len(rec.exits) != 1 || rec.exits["g"] != 3 {
		t.Errorf("exits = %v", rec.exits)
	}
	for _, e := range rec.events {
		if e.ExecID == "s" {
			t.Errorf("event from a process outside the tree: %+v", e)
		}
	}
	// exiting forgets the process, so a later exit of it is ignored
	n.Handle(ctx, exitEvent(grandchild, 9))
	if rec.exits["g"] != 3 {
		t.Errorf("a second exit changed the status to %d", rec.exits["g"])
	}
}

func TestAProcessAlreadyRunningIsRegisteredByItsFirstEvent(t *testing.T) {
	rec := newRecorder()
	n := normalizer(rec)
	agent := proc("pre", "", "/home/ubuntu/.local/bin/claude", "")
	if err := n.Handle(ctx, kprobeEvent(agent, "security_path_unlink", pathArgOf("/d"), pathArgOf("/d/f"))); err != nil {
		t.Fatal(err)
	}
	if len(rec.procs) != 1 || rec.procs[0].ExecID != "pre" || len(rec.events) != 1 {
		t.Errorf("procs %+v events %+v", rec.procs, rec.events)
	}
	// a second event does not register it again
	n.Handle(ctx, kprobeEvent(agent, "security_path_unlink", pathArgOf("/d"), pathArgOf("/d/g")))
	if len(rec.procs) != 1 || len(rec.events) != 2 {
		t.Errorf("after second event: %d procs, %d events", len(rec.procs), len(rec.events))
	}
}

func TestHooksAreMapped(t *testing.T) {
	agent := proc("a", "", "/home/ubuntu/.local/bin/claude", "")
	sock := &tetragon.KprobeArgument{Arg: &tetragon.KprobeArgument_SockArg{SockArg: &tetragon.KprobeSock{Family: "AF_INET", Daddr: "10.0.0.1", Dport: 8080}}}
	file := &tetragon.KprobeArgument{Arg: &tetragon.KprobeArgument_FileArg{FileArg: &tetragon.KprobeFile{Path: "/tmp/x (deleted)", Permission: "-rw-r--r--"}}}
	intArg := func(v int32) *tetragon.KprobeArgument {
		return &tetragon.KprobeArgument{Arg: &tetragon.KprobeArgument_IntArg{IntArg: v}}
	}
	cases := []struct {
		name         string
		fn           string
		args         []*tetragon.KprobeArgument
		typ, p1, p2  string
		wantRecorded bool
	}{
		{"open strips (deleted)", "security_file_open", []*tetragon.KprobeArgument{file}, "file_open", "/tmp/x", "", true},
		{"connect v4", "tcp_connect", []*tetragon.KprobeArgument{sock}, "net_connect", "10.0.0.1:8080", "", true},
		{"kill on arm64", "__arm64_sys_kill", []*tetragon.KprobeArgument{intArg(42), intArg(9)}, "kill", "", "", true},
		{"tgkill", "__x64_sys_tgkill", []*tetragon.KprobeArgument{intArg(42), intArg(43), intArg(15)}, "kill", "", "", true},
		{"rename", "security_path_rename", []*tetragon.KprobeArgument{pathArgOf("/a"), pathArgOf("/a/old"), pathArgOf("/b"), pathArgOf("/b/new")}, "file_rename", "/a/old", "/b/new", true},
		{"rename missing target", "security_path_rename", []*tetragon.KprobeArgument{pathArgOf("/a"), pathArgOf("/a/old")}, "", "", "", false},
		{"unlink without args", "security_path_unlink", nil, "", "", "", false},
		{"connect without a socket", "tcp_connect", nil, "", "", "", false},
		{"kill without args", "__x64_sys_kill", []*tetragon.KprobeArgument{intArg(1)}, "", "", "", false},
		{"chmod without a mode", "security_path_chmod", []*tetragon.KprobeArgument{pathArgOf("/f")}, "file_setmode", "/f", "", true},
		{"open of a non-path", "security_file_open", []*tetragon.KprobeArgument{intArg(1)}, "", "", "", false},
		{"bytes sent is not recorded yet", "tcp_sendmsg", []*tetragon.KprobeArgument{sock}, "", "", "", false},
		{"permission checks are not recorded", "security_file_permission", []*tetragon.KprobeArgument{file}, "", "", "", false},
	}
	for _, c := range cases {
		rec := newRecorder()
		n := normalizer(rec)
		if err := n.Handle(ctx, kprobeEvent(agent, c.fn, c.args...)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !c.wantRecorded {
			if len(rec.events) != 0 {
				t.Errorf("%s: recorded %+v", c.name, rec.events)
			}
			continue
		}
		if len(rec.events) != 1 {
			t.Errorf("%s: %d events", c.name, len(rec.events))
			continue
		}
		if e := rec.events[0]; e.Type != c.typ || e.Path != c.p1 || e.Path2 != c.p2 {
			t.Errorf("%s: got %s %q %q, want %s %q %q", c.name, e.Type, e.Path, e.Path2, c.typ, c.p1, c.p2)
		}
	}
}

func TestEventsWeCannotUseAreIgnored(t *testing.T) {
	rec := newRecorder()
	n := normalizer(rec)
	for _, r := range []*tetragon.GetEventsResponse{
		{},                             // no event
		execEvent(nil),                 // no process
		execEvent(&tetragon.Process{}), // no exec id
		exitEvent(nil, 0),
		kprobeEvent(nil, "security_path_unlink"),
		{Event: &tetragon.GetEventsResponse_ProcessLoader{ProcessLoader: &tetragon.ProcessLoader{}}}, // a kind we don't use
	} {
		if err := n.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.procs)+len(rec.events)+len(rec.exits) != 0 {
		t.Errorf("recorded something: %+v", rec)
	}
}

func TestSinkErrorsAreReturned(t *testing.T) {
	agent := proc("a", "", "/home/ubuntu/.local/bin/claude", "")
	for _, c := range []struct {
		failOn string
		evs    []*tetragon.GetEventsResponse
	}{
		{"process", []*tetragon.GetEventsResponse{execEvent(agent)}},
		{"event", []*tetragon.GetEventsResponse{execEvent(agent)}},
		{"exit", []*tetragon.GetEventsResponse{execEvent(agent), exitEvent(agent, 0)}},
	} {
		rec := newRecorder()
		rec.failOn = c.failOn
		n := normalizer(rec)
		var err error
		for _, e := range c.evs {
			if err = n.Handle(ctx, e); err != nil {
				break
			}
		}
		if err != errFail {
			t.Errorf("fail on %s: err = %v", c.failOn, err)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	for in, want := range map[string][]string{
		``:                             {},
		`a b  c`:                       {"a", "b", "c"},
		`-c "echo hi there" x`:         {"-c", "echo hi there", "x"},
		`-c "say \"hi\" now"`:          {"-c", `say "hi" now`},
		`"" x`:                         {"", "x"},
		`trailing "unterminated quote`: {"trailing", "unterminated quote"},
	} {
		got := splitArgs(in)
		if len(got) != len(want) {
			t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
			}
		}
	}
}

func TestAgentBinariesAreMatchedByPrefixOrExactly(t *testing.T) {
	n := NewNormalizer(newRecorder(), policy.AgentPrefixes([]string{"/home/a", "/Users/b"}))
	for bin, want := range map[string]bool{
		"/home/a/.local/bin/claude":                    true,
		"/home/a/.local/bin/claude-other":              false, // exact entry, not a prefix
		"/home/a/.local/share/claude/versions/2.1.305": true,  // directory entry
		"/Users/b/.local/share/claude/versions/9.9.9":  true,
		"/usr/local/bin/claude":                        true,
		"/home/c/.local/bin/claude":                    false,
		"/usr/bin/node":                                false,
	} {
		if got := n.isAgent(bin); got != want {
			t.Errorf("isAgent(%q) = %v, want %v", bin, got, want)
		}
	}
}
