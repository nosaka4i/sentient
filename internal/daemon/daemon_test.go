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

	c, err := api.Dial(sock)
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

func TestDefaultHomes(t *testing.T) {
	h := DefaultHomes()
	if len(h) == 0 || h[0] != "/root" {
		t.Errorf("DefaultHomes = %v", h)
	}
}
