package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nosaka4i/sentient/internal/api/sentientpb"
	"github.com/nosaka4i/sentient/internal/store"
	"github.com/nosaka4i/sentient/internal/version"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// shortDir returns a temp dir with a short path: Unix socket paths are
// limited to ~108 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// startServer serves a store with one session, prompt and action.
func startServer(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := shortDir(t)
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s", Cwd: "/w", RepoRoot: "/w"})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: sid, TS: "2026-10-17T09:00:00.000000000Z", SourceFile: "f", Type: "user", Record: "{}"})
	tx.AppendRaw(ctx, store.RawRecord{SourceFile: "f", Unknown: true, Record: "{bad"})
	tx.AddPrompt(ctx, sid, "hi", "2026-10-17T09:00:00.000000000Z")
	tx.AddAction(ctx, store.Action{SessionID: sid, TS: "2026-10-17T09:01:00.000000000Z", Resource: "file", Action: "read", Path: "/w/a", Evidence: "log"})
	// an agent process, running, with two background commands
	for _, p := range []store.Process{
		{ExecID: "root", Collector: "tetragon", PID: 1, Binary: "/opt/claude/versions/2.1.9", Cwd: "/w", StartedAt: "2026-10-17T08:00:00.000000000Z"},
		{ExecID: "g1", Collector: "tetragon", PID: 2, ParentExecID: "root", Binary: "/usr/bin/git", Argv: `["status","--short"]`, StartedAt: "2026-10-17T09:02:00.000000000Z"},
		{ExecID: "g2", Collector: "tetragon", PID: 3, ParentExecID: "root", Binary: "/usr/bin/git", Argv: `["config","--get","remote.origin.url"]`, StartedAt: "2026-10-17T09:03:00.000000000Z"},
	} {
		if err := tx.AddProcess(ctx, p); err != nil {
			t.Fatal(err)
		}
		if err := tx.AttributeProcess(ctx, p.ExecID, sid, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.SetBackground(ctx, "g1", "git status", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.SetBackground(ctx, "g2", "git config --get", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// one open alert on the action above
	if _, err := st.DB.Exec(`INSERT INTO alerts (action_id, ts, severity, rule, status) SELECT id, ts, 'medium', 'outside-read', 'open' FROM actions LIMIT 1`); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(dir, "s.sock")
	lis, err := Listen(sock, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- Serve(sctx, lis, NewServer(st.DB)) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return sock
}

func TestStatusRoundTrip(t *testing.T) {
	sock := startServer(t)
	c, err := Dial(sock, "test client")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	st, ver, err := c.Status(context.Background(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ver != version.Version {
		t.Errorf("daemon version = %q", ver)
	}
	if !st.HistoryBegins.Equal(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("HistoryBegins = %v", st.HistoryBegins)
	}
	if st.PromptsTotal != 1 || len(st.Prompts) != 1 || st.Prompts[0].Text != "hi" || st.Prompts[0].Repo != "w" ||
		st.Prompts[0].Agent != "claude" || !st.Prompts[0].Started.Equal(time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("prompts = %d %+v", st.PromptsTotal, st.Prompts)
	}
	if st.Activity[0].Repos != 1 {
		t.Errorf("structured fields not carried: %+v", st.Activity[0])
	}
	if st.UnknownRecords != 1 {
		t.Errorf("UnknownRecords = %d", st.UnknownRecords)
	}
	if len(st.Activity) < 2 || st.Activity[0].Resource != "prompt" || st.Activity[0].Summary != "1 repo" ||
		st.Activity[1].Resource != "file" || st.Activity[1].Action != "read" || st.Activity[1].Count != 1 {
		t.Errorf("Activity = %+v", st.Activity)
	}
}

func TestStatusRequiresWindow(t *testing.T) {
	srv := NewServer(nil)
	_, err := srv.Status(context.Background(), &sentientpb.StatusRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("err = %v, want InvalidArgument", err)
	}
	now := time.Now()
	_, err = srv.Status(context.Background(), &sentientpb.StatusRequest{From: timestampOf(now), To: timestampOf(now), PromptLimit: 1000})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("prompt_limit 1000: err = %v, want InvalidArgument", err)
	}
}

func TestStatusInternalError(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.Close() // queries now fail
	srv := NewServer(st.DB)
	from := time.Now()
	_, err = srv.Status(context.Background(), &sentientpb.StatusRequest{
		From: timestampOf(from), To: timestampOf(from),
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("err = %v, want Internal", err)
	}
}

func TestUnreachable(t *testing.T) {
	c, err := Dial(filepath.Join(shortDir(t), "none.sock"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, err = c.Status(context.Background(), time.Now(), time.Now())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v, want ErrUnreachable", err)
	}
}

func TestListenStaleAndLive(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, "sub", "s.sock")

	// A stale socket file (no listener) is replaced.
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lis, err := Listen(sock, "no-such-group-sentient-test", quiet)
	if err != nil {
		t.Fatalf("Listen over stale file: %v", err)
	}
	defer lis.Close()
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o660 {
		t.Errorf("socket mode = %v, %v; want 0660", fi.Mode().Perm(), err)
	}
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// A live socket is not stolen.
	if _, err := Listen(sock, "", quiet); err == nil {
		t.Error("Listen on a live socket should fail")
	}

	// The caller's own primary group is accepted.
	sock2 := filepath.Join(dir, "g.sock")
	if g := currentGroup(t); g != "" {
		l2, err := Listen(sock2, g, quiet)
		if err != nil {
			t.Fatal(err)
		}
		l2.Close()
	}
}

func TestListenBadDir(t *testing.T) {
	if _, err := Listen("/proc/nope/s.sock", "", quiet); err == nil {
		t.Error("want error")
	}
}

func TestServeReturnsListenerError(t *testing.T) {
	lis, err := net.Listen("unix", filepath.Join(shortDir(t), "x.sock"))
	if err != nil {
		t.Fatal(err)
	}
	lis.Close() // Serve fails immediately on a closed listener
	if err := Serve(context.Background(), lis, NewServer(nil)); err == nil {
		t.Error("want error from closed listener")
	}
}
