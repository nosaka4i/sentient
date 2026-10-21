package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
)

func TestRunIngestsAndServes(t *testing.T) {
	dir, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	home := filepath.Join(dir, "home")
	if err := os.CopyFS(home, os.DirFS("../../testdata/claudecode/home")); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "d.sock")
	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- Run(ctx, Config{
			DataDir:      filepath.Join(dir, "data"),
			Socket:       sock,
			Homes:        []string{home},
			PollInterval: 20 * time.Millisecond,
			Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			Ready:        ready,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited early: %v", err)
	}

	c, err := api.Dial(sock, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	from := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, _, err := c.Status(ctx, from, from.Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Activity) > 0 && st.Activity[0].Count == 2 && st.UnknownRecords == 2 { // 2 fixture prompts
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture not ingested: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Health over the same socket, once the startup integrity check ran.
	for {
		h, err := c.Health(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !h.Integrity.CheckedAt.IsZero() {
			// The startup check may run before ingest stored anything.
			if !h.Integrity.OK || h.Integrity.Interval != time.Hour {
				t.Errorf("integrity = %+v", h.Integrity)
			}
			if h.Daemon.Socket != sock || h.Storage.DBBytes <= 0 || len(h.Agents) != 1 || h.Agents[0].Version != "2.0.5" {
				t.Errorf("health = %+v", h)
			}
			if len(h.Ingest) != 1 || h.Ingest[0].Files != 2 || h.Ingest[0].Unknown != 2 {
				t.Errorf("ingest = %+v", h.Ingest)
			}
			if len(h.API.Clients) != 1 || h.API.Clients[0] != "test" || h.API.Requests == 0 {
				t.Errorf("api = %+v", h.API)
			}
			if os.Geteuid() != 0 && h.Sudo.Checked {
				t.Errorf("sudo checked without root: %+v", h.Sudo)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("integrity check never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket not removed on shutdown")
	}
}

func TestRunFailsOnBadDataDir(t *testing.T) {
	err := Run(context.Background(), Config{DataDir: "/proc/nope", Socket: "/proc/nope/s.sock",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestDefaultHomesByPlatform(t *testing.T) {
	// a fake filesystem root with both layouts
	root := t.TempDir()
	for _, d := range []string{"home/alice", "home/bob", "Users/hu", "Users/other", "Users/Shared", "Users/Guest"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "home", "afile"), nil, 0o644) // a file under /home is not a home
	join := func(ds ...string) []string {
		var out []string
		for _, d := range ds {
			out = append(out, filepath.Join(root, d))
		}
		return out
	}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	// Linux: /root plus every directory under /home
	if got := defaultHomes("linux", "/home/alice", false, root); !eq(got, join("root", "home/alice", "home/bob")) {
		t.Errorf("linux = %v", got)
	}
	// macOS as a normal user: just that user's home
	if got := defaultHomes("darwin", "/Users/hu", false, root); !eq(got, []string{"/Users/hu"}) {
		t.Errorf("darwin user = %v", got)
	}
	// macOS as root: every user, without Shared and Guest
	if got := defaultHomes("darwin", "/var/root", true, root); !eq(got, join("Users/hu", "Users/other")) {
		t.Errorf("darwin root = %v", got)
	}
	// macOS with no known home: nothing, not a wrong guess
	if got := defaultHomes("darwin", "", false, root); len(got) != 0 {
		t.Errorf("darwin no home = %v", got)
	}
	// the real thing on this machine returns something sensible
	if h := DefaultHomes(); len(h) == 0 {
		t.Errorf("DefaultHomes = %v", h)
	}
}

func TestDefaultPathsByPlatform(t *testing.T) {
	for _, c := range []struct{ goos, home, data, group string }{
		{"linux", "/home/u", "/opt/sentient/data", "sentient"},
		{"darwin", "/Users/hu", "/Users/hu/Library/Application Support/sentient/data", ""},
		{"darwin", "", "/opt/sentient/data", ""}, // no home to put it in: the system path
	} {
		if got := defaultDataDir(c.goos, c.home); got != c.data {
			t.Errorf("data dir %s %q = %q, want %q", c.goos, c.home, got, c.data)
		}
		if got := defaultSocketGroup(c.goos); got != c.group {
			t.Errorf("socket group %s = %q, want %q", c.goos, got, c.group)
		}
	}
}

func TestMaxRSSUnits(t *testing.T) {
	if got := maxRSSBytes("linux", 2048); got != 2048*1024 {
		t.Errorf("linux reports KB: %d", got)
	}
	if got := maxRSSBytes("darwin", 2048); got != 2048 {
		t.Errorf("macOS reports bytes: %d", got)
	}
}
