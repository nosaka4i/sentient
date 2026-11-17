package es

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector"
	"github.com/nosaka4i/sentient/internal/store"
)

// lockSink is a Sink that is safe to read while the collector writes to it.
type lockSink struct {
	mu          sync.Mutex
	procs       []store.Process
	exits       []exitRec
	events      []store.KernelEvent
	gaps        []*gapRec
	failProcess error
}

type gapRec struct {
	reason string
	closed bool
}

func (s *lockSink) Process(_ context.Context, p store.Process) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs = append(s.procs, p)
	return s.failProcess
}
func (s *lockSink) Exit(_ context.Context, id string, _ time.Time, code int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exits = append(s.exits, exitRec{id, code})
	return nil
}
func (s *lockSink) Event(_ context.Context, e store.KernelEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}
func (s *lockSink) GapOpened(_ context.Context, _ time.Time, reason string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gaps = append(s.gaps, &gapRec{reason: reason})
	return int64(len(s.gaps)), nil
}
func (s *lockSink) GapClosed(_ context.Context, id int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gaps[id-1].closed = true
	return nil
}

// counts returns how many processes, exits, events and gaps were reported.
func (s *lockSink) counts() (procs, exits, events, gaps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs), len(s.exits), len(s.events), len(s.gaps)
}

func (s *lockSink) gapList() []gapRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]gapRec, len(s.gaps))
	for i, g := range s.gaps {
		out[i] = *g
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// start runs a collector on a fresh socket and returns the socket path, a
// stop function that waits for Run to return, and the sink.
func start(t *testing.T, c *Collector, sink collector.Sink) (sock string, stop func() error) {
	t.Helper()
	dir, err := os.MkdirTemp("", "es")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	c.Socket = filepath.Join(dir, "s.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx, sink) }()
	waitFor(t, "the socket", func() bool { _, err := os.Stat(c.Socket); return err == nil })
	var once sync.Once
	var runErr error
	stop = func() error {
		once.Do(func() { cancel(); runErr = <-done })
		return runErr
	}
	t.Cleanup(func() { stop() })
	return c.Socket, stop
}

func dial(t *testing.T, sock string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func send(t *testing.T, conn net.Conn, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if _, err := conn.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
}

const helloOK = `{"v":1,"t":"hello","seq":1,"ts_sec":1000,"ext":"1.0","build":"4","macos":"26.0","es_version":8,"es_client":"ok"}`

func TestNeverConnectedOpensAGapAndTheSocketIsRootOnly(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{}
	sock, stop := start(t, c, sink)
	waitFor(t, "the first gap", func() bool { _, _, _, g := sink.counts(); return g == 1 })
	if g := sink.gapList()[0]; !strings.Contains(g.reason, "has not connected") || g.closed {
		t.Errorf("gap: %+v", g)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode: %v %v", fi, err)
	}
	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestHelloSendsTheConfigAndClosesTheGap(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{
		Config: Config{Agents: []string{"/a/claude-code/"}, Noise: []string{"/System/"}, Sensitive: []string{"/Users/x/.ssh/"}},
		Status: NewStatus("", time.Now()),
	}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"t":"config"`, `"agents":["/a/claude-code/"]`, `"noise":["/System/"]`, `"sensitive":["/Users/x/.ssh/"]`} {
		if !strings.Contains(line, want) {
			t.Errorf("config %q lacks %s", line, want)
		}
	}
	waitFor(t, "the gap to close", func() bool { g := sink.gapList(); return len(g) == 1 && g[0].closed })
	snap := c.Status.Snapshot(time.Now())
	if snap.State != collector.Connected || snap.Version != "1.0 (build 4)" || snap.System != "Endpoint Security" {
		t.Errorf("status: %+v", snap)
	}
}

func TestExtensionThatCouldNotStartShowsTheReason(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{Status: NewStatus("", time.Now())}
	sock, _ := start(t, c, sink)
	notPermitted := `{"v":1,"t":"hello","seq":1,"ts_sec":1000,"ext":"1.0","es_client":"not_permitted"}`
	for i := 0; i < 2; i++ { // the second try fails the same way: still one gap
		conn := dial(t, sock)
		send(t, conn, notPermitted)
		waitFor(t, "the failure gap", func() bool {
			g := sink.gapList()
			return len(g) >= 2 && strings.Contains(g[1].reason, "Full Disk Access") && !g[1].closed
		})
		conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	g := sink.gapList()
	if len(g) != 2 || !g[0].closed {
		t.Errorf("gaps: %+v", g)
	}
	if s := c.Status.Snapshot(time.Now()); s.State != collector.Reconnecting || !strings.Contains(s.LastError, "not_permitted") {
		t.Errorf("status: %+v", s)
	}
	// another failure (not the permission one) replaces the reason
	conn := dial(t, sock)
	send(t, conn, `{"v":1,"t":"hello","seq":1,"ts_sec":1001,"es_client":"too_many_clients"}`)
	waitFor(t, "the new reason", func() bool { return len(sink.gapList()) == 3 })
	if g := sink.gapList(); strings.Contains(g[2].reason, "Full Disk Access") || !g[1].closed {
		t.Errorf("gaps: %+v", g)
	}
}

func TestSessionThroughTheSocket(t *testing.T) {
	data, err := os.ReadFile("testdata/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	sink := &lockSink{}
	c := &Collector{Status: NewStatus("", time.Now())}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, strings.Split(strings.TrimSpace(string(data)), "\n")...)
	waitFor(t, "the session", func() bool { p, x, _, g := sink.counts(); return p == 3 && x == 2 && g == 2 })
	// the stats message in the fixture reports 3 dropped events: a closed gap with the reason
	gaps := sink.gapList()
	if !gaps[0].closed || !gaps[1].closed || !strings.Contains(gaps[1].reason, "dropped 3 events (queue_full 3)") {
		t.Errorf("gaps: %+v", gaps)
	}
	by, bad := c.Dropped()
	if by["queue_full"] != 3 || bad != 0 {
		t.Errorf("dropped %v, undecodable %d", by, bad)
	}
	if s := c.Status.Snapshot(time.Now()); s.Events == 0 {
		t.Error("no events counted")
	}
}

func TestSequenceJumpIsAGap(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK,
		`{"v":1,"t":"exec","seq":2,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"exe":"/bin/x"}`,
		`{"v":1,"t":"file","seq":5,"ts_sec":1004,"pid":5,"pidver":1,"op":"write","path":"/a"}`)
	waitFor(t, "the jump gap", func() bool {
		for _, g := range sink.gapList() {
			if strings.Contains(g.reason, "lost 2 messages") && g.closed {
				return true
			}
		}
		return false
	})
}

func TestDisconnectOpensAGapAndReconnectClosesIt(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{Status: NewStatus("", time.Now())}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK,
		`{"v":1,"t":"exec","seq":2,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"exe":"/bin/x"}`)
	waitFor(t, "the process", func() bool { p, _, _, _ := sink.counts(); return p == 1 })
	conn.Close()
	waitFor(t, "the disconnect gap", func() bool {
		g := sink.gapList()
		return len(g) == 2 && strings.Contains(g[1].reason, "disconnected") && !g[1].closed
	})
	if s := c.Status.Snapshot(time.Now()); s.State != collector.Reconnecting {
		t.Errorf("state %s", s.State)
	}
	conn2 := dial(t, sock)
	send(t, conn2, helloOK)
	waitFor(t, "the gap to close", func() bool { g := sink.gapList(); return g[1].closed })
	if s := c.Status.Snapshot(time.Now()); s.State != collector.Connected || s.Reconnects != 1 {
		t.Errorf("status %+v", s)
	}
}

func TestSilentExtensionIsAGap(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{PingTimeout: 80 * time.Millisecond}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK)
	waitFor(t, "the hung gap", func() bool {
		g := sink.gapList()
		return len(g) == 2 && strings.Contains(g[1].reason, "stopped responding")
	})
}

func TestPingKeepsAConnectionAlive(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{PingTimeout: 150 * time.Millisecond}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK)
	for i := 0; i < 6; i++ {
		time.Sleep(50 * time.Millisecond)
		send(t, conn, `{"v":1,"t":"ping","seq":`+string(rune('2'+i))+`,"ts_sec":1001}`)
	}
	g := sink.gapList()
	if len(g) != 1 || !g[0].closed {
		t.Errorf("gaps: %+v", g)
	}
}

func TestUndecodableLinesAreCountedAndSkipped(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK, `not json`, `{"v":9,"t":"exec"}`,
		`{"v":1,"t":"exec","seq":2,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"exe":"/bin/x"}`,
		`{"v":1,"t":"file","pid":5,"pidver":1,"op":"write","path":"/before-any-hello-in-this-case-it-is-fine"}`)
	waitFor(t, "the events", func() bool { p, _, e, _ := sink.counts(); return p == 1 && e == 2 })
	if _, bad := c.Dropped(); bad != 2 {
		t.Errorf("undecodable = %d, want 2", bad)
	}
}

func TestMessagesBeforeHelloAreIgnored(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn,
		`{"v":1,"t":"exec","seq":1,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"exe":"/bin/x"}`,
		helloOK)
	waitFor(t, "the hello", func() bool { g := sink.gapList(); return len(g) == 1 && g[0].closed })
	if p, _, _, _ := sink.counts(); p != 0 {
		t.Errorf("%d processes from before the hello", p)
	}
}

func TestSinkFailureEndsRun(t *testing.T) {
	boom := errors.New("disk full")
	sink := &lockSink{failProcess: boom}
	c := &Collector{}
	dir, err := os.MkdirTemp("", "es")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	c.Socket = filepath.Join(dir, "s.sock")
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), sink) }()
	waitFor(t, "the socket", func() bool { _, err := os.Stat(c.Socket); return err == nil })
	conn := dial(t, c.Socket)
	send(t, conn, helloOK, `{"v":1,"t":"exec","seq":2,"ts_sec":1001,"pid":5,"pidver":1,"start":1000,"exe":"/bin/x"}`)
	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestTooLongLineEndsTheConnection(t *testing.T) {
	sink := &lockSink{}
	c := &Collector{}
	sock, _ := start(t, c, sink)
	conn := dial(t, sock)
	send(t, conn, helloOK)
	go conn.Write([]byte(strings.Repeat("x", maxLine+1<<10) + "\n"))
	waitFor(t, "the disconnect gap", func() bool {
		g := sink.gapList()
		return len(g) == 2 && strings.Contains(g[1].reason, "disconnected")
	})
}

func TestRunReplacesAStaleSocketAndFailsOnABadPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "es")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	stale := filepath.Join(dir, "s.sock")
	if err := os.WriteFile(stale, []byte("left behind"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Collector{Socket: stale}).Run(ctx, &lockSink{}) }()
	waitFor(t, "a listening socket", func() bool {
		conn, err := net.Dial("unix", stale)
		if err == nil {
			conn.Close()
		}
		return err == nil
	})
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
	if err := (&Collector{Socket: filepath.Join(dir, "no", "such", "dir", "s.sock")}).Run(context.Background(), &lockSink{}); err == nil {
		t.Error("Run succeeded with a socket in a missing directory")
	}
}

func TestDefaults(t *testing.T) {
	c := &Collector{}
	if c.Name() != "endpointsecurity" || c.socket() != DefaultSocket || c.pingTimeout() != 30*time.Second {
		t.Errorf("%s %s %s", c.Name(), c.socket(), c.pingTimeout())
	}
	if c.now().IsZero() {
		t.Error("no clock")
	}
	if s := NewStatus("", time.Now()).Snapshot(time.Now()); s.Detail != DefaultSocket || s.Platform != "Mac" {
		t.Errorf("%+v", s)
	}
	if dropReasons(map[string]int64{"b": 2, "a": 1}) != "a 1, b 2" {
		t.Error("dropReasons")
	}
	if helloVersion(Message{Ext: "1.0"}) != "1.0" {
		t.Error("helloVersion")
	}
	c.logf("quiet without a logger")
}
