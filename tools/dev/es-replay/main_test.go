package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/es"
	"github.com/nosaka4i/sentient/internal/store"
)

func lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("testdata/capture.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestConvert(t *testing.T) {
	c := &Converter{UID: 501}
	var all []es.Message
	var errs int
	for _, l := range lines(t) {
		msgs, err := c.Convert([]byte(l))
		if err != nil {
			errs++
			continue
		}
		all = append(all, msgs...)
	}
	if errs != 1 {
		t.Errorf("%d unreadable lines, want the one garbage line", errs)
	}
	// 14 lines: the garbage one, a close and two terminal/null writes are not sent (10 messages), plus the hello
	if len(all) != 11 {
		t.Fatalf("%d messages: %+v", len(all), all)
	}
	if all[0].T != es.KindHello || all[0].ESClient != "ok" || all[0].Seq != 1 {
		t.Errorf("hello: %+v", all[0])
	}
	for i, m := range all {
		if m.V != es.SchemaVersion || m.Seq != uint64(i+1) {
			t.Errorf("message %d: v=%d seq=%d", i, m.V, m.Seq)
		}
	}
	root := all[1] // exec of the agent
	if root.T != es.KindExec || root.PID != 100 || root.PIDVer != 2 || root.Start != 1000 || root.PPIDVer != 1 ||
		root.Exe != "/Users/dev/cctest/claude-code/agent-standin" || len(root.Args) != 2 || root.UID != 501 {
		t.Errorf("root exec: %+v", root)
	}
	if o := all[2]; o.Op != es.OpOpen || o.Flags != 537919489 || o.PIDVer != 2 {
		t.Errorf("open: %+v", o)
	}
	fork := all[3]
	if fork.T != es.KindFork || fork.PID != 101 || fork.PIDVer != 1 || fork.PPID != 100 || fork.PPIDVer != 2 || fork.Start != 1001 {
		t.Errorf("fork: %+v", fork)
	}
	if w := all[4]; w.Op != es.OpWrite || w.PID != 101 || w.PIDVer != 1 {
		t.Errorf("write before the exec: %+v", w)
	}
	goExec := all[5]
	if goExec.T != es.KindExec || goExec.PIDVer != 2 || goExec.Start != 1001 || goExec.Exe != "/usr/local/go/bin/go" {
		t.Errorf("go exec: %+v", goExec)
	}
	if u := all[8]; u.Op != es.OpUnlink || u.Path != "/Users/dev/proj/old.txt" {
		t.Errorf("unlink: %+v", u)
	}
	ren := all[7]
	if ren.Op != es.OpRename || ren.Path != "/Users/dev/proj/new.txt.tmp.1" || ren.Path2 != "/Users/dev/proj/new.txt" {
		t.Errorf("rename: %+v", ren)
	}
	if last := all[len(all)-1]; last.T != es.KindExit || last.PID != 100 {
		t.Errorf("last: %+v", last)
	}
	if p := c.Ping(2000); p.T != es.KindPing || p.Seq != uint64(len(all)+1) {
		t.Errorf("ping: %+v", p)
	}
}

func TestConvertRejectsUnusableLines(t *testing.T) {
	c := &Converter{}
	for _, l := range []string{`not json`, `{"type":"write"}`, `{"pid":4}`} {
		if _, err := c.Convert([]byte(l)); err == nil {
			t.Errorf("Convert(%q) succeeded", l)
		}
	}
	if msgs, err := c.Convert([]byte(`{"type":"chmod","pid":4,"ts_sec":1}`)); err != nil || len(msgs) != 1 || msgs[0].T != es.KindHello {
		t.Errorf("an unknown type after nothing: %v %+v", err, msgs)
	}
}

func TestReplayPacing(t *testing.T) {
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	in := strings.NewReader(strings.Join(lines(t), "\n"))
	var out bytes.Buffer
	st, err := Replay(context.Background(), &Converter{}, in, &out, 1, sleep)
	if err != nil {
		t.Fatal(err)
	}
	if st.Lines != 14 || st.Sent != 11 || st.Skipped != 3 || st.Unusable != 1 {
		t.Errorf("stats: %+v", st)
	}
	if len(slept) == 0 {
		t.Error("real-time replay never waited")
	}
	for _, d := range slept {
		if d > maxPause {
			t.Errorf("waited %s, more than the cap", d)
		}
	}
	// as fast as possible: no waiting at all
	slept = nil
	if _, err := Replay(context.Background(), &Converter{}, strings.NewReader(strings.Join(lines(t), "\n")), &bytes.Buffer{}, 0, sleep); err != nil || len(slept) != 0 {
		t.Errorf("speed 0 waited: %v %v", slept, err)
	}
	// every output line is a message the daemon accepts
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if _, err := es.Decode([]byte(l)); err != nil {
			t.Errorf("%q: %v", l, err)
		}
	}
}

func TestReplayStopsOnCancelAndOnWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Replay(ctx, &Converter{}, strings.NewReader(strings.Join(lines(t), "\n")), &bytes.Buffer{}, 0, time.Sleep); err == nil {
		t.Error("a cancelled replay returned no error")
	}
	if _, err := Replay(context.Background(), &Converter{}, strings.NewReader(strings.Join(lines(t), "\n")), failWriter{}, 0, time.Sleep); err == nil {
		t.Error("a failing writer returned no error")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, os.ErrClosed }

func TestHoldSendsPings(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := hold(ctx, &Converter{}, &buf, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), `"t":"ping"`); n < 2 {
		t.Errorf("%d pings in %q", n, buf.String())
	}
	if err := hold(context.Background(), &Converter{}, failWriter{}, time.Millisecond); err == nil {
		t.Error("hold ignored a failing writer")
	}
}

// recSink collects what the collector reports.
type recSink struct {
	mu     sync.Mutex
	procs  []store.Process
	events []store.KernelEvent
	exits  []string
	gaps   int
	closed int
}

func (s *recSink) Process(_ context.Context, p store.Process) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs = append(s.procs, p)
	return nil
}
func (s *recSink) Exit(_ context.Context, id string, _ time.Time, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exits = append(s.exits, id)
	return nil
}
func (s *recSink) Event(_ context.Context, e store.KernelEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}
func (s *recSink) GapOpened(context.Context, time.Time, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gaps++
	return int64(s.gaps), nil
}
func (s *recSink) GapClosed(context.Context, int64, time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}
func (s *recSink) snapshot() (procs, events, exits, gaps, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs), len(s.events), len(s.exits), s.gaps, s.closed
}

// startCollector runs the real collector on a temp socket.
func startCollector(t *testing.T) (string, *recSink) {
	t.Helper()
	dir, err := os.MkdirTemp("", "esr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { (&es.Collector{Socket: sock}).Run(ctx, sink); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector never listened")
		}
	}
	return sock, sink
}

// The replay, through the real collector over a real socket, ends as the
// extension's own messages would: the agent's processes, their events, and the
// gap from before the first hello closed.
func TestReplayThroughTheRealCollector(t *testing.T) {
	sock, sink := startCollector(t)
	cmd := newRootCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--socket", sock, "--once", "testdata/capture.jsonl"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "replayed 14 lines: 11 messages sent, 3 left out") {
		t.Errorf("summary: %q", stderr.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		procs, events, exits, _, _ := sink.snapshot()
		// 2 processes; events: 2 execs, open, write(pre-exec), create, rename, unlink = 7; 2 exits
		if procs == 2 && events == 7 && exits == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("procs=%d events=%d exits=%d", procs, events, exits)
		}
		time.Sleep(10 * time.Millisecond)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.procs[0].ExecID != "es:100:2:1000" || sink.procs[1].ExecID != "es:101:2:1001" || sink.procs[1].ParentExecID != "es:100:2:1000" {
		t.Errorf("processes: %+v", sink.procs)
	}
	if sink.closed < 1 {
		t.Error("the not-connected gap was not closed by the replay's hello")
	}
}

func TestCommandErrors(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"--socket", "/nonexistent/s.sock", "testdata/capture.jsonl"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not listening") {
		t.Errorf("no daemon: %v", err)
	}
	cmd = newRootCmd()
	cmd.SetArgs([]string{"no-such-file.jsonl"})
	if err := cmd.Execute(); err == nil {
		t.Error("a missing capture was accepted")
	}
	cmd = newRootCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Error("no capture given was accepted")
	}
}
