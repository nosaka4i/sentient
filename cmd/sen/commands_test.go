package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/api"
	"github.com/nosaka4i/sentient/internal/store"
	"github.com/nosaka4i/sentient/internal/version"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// The help text from docs/mocks/cli/sen.md.
const wantHelp = `sen — see what your coding agent actually did

Usage:
  sen [command]

Available Commands:
  status      What needs your attention, and the last 24h of activity
  serve       Serve the web dashboard on localhost
  version     Print the sen version
  help        Help about any command

Flags:
  -h, --help   help for sen

Use "sen [command] --help" for more information about a command.
`

func TestHelpMatchesMock(t *testing.T) {
	out, err := run(t)
	if err != nil {
		t.Fatal(err)
	}
	if out != wantHelp {
		t.Errorf("got:\n%s\nwant:\n%s", out, wantHelp)
	}
}

func TestVersion(t *testing.T) {
	out, err := run(t, "version")
	if err != nil || out != "sen "+version.Version+"\n" {
		t.Errorf("version = %q, %v", out, err)
	}
}

func TestStatusUnreachable(t *testing.T) {
	_, err := run(t, "status", "--socket", filepath.Join(t.TempDir(), "none.sock"))
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusAgainstServer(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "sen")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	tx, _ := st.Begin(ctx)
	sid, _ := tx.UpsertSession(ctx, store.Session{Agent: "claude", AgentSessionID: "s"})
	tx.AppendRaw(ctx, store.RawRecord{SessionID: sid, TS: storeTime(now.Add(-10 * time.Second)), SourceFile: "f", Type: "user", Record: "{}"})
	tx.AddPrompt(ctx, sid, "fix the flaky test", storeTime(now.Add(-10*time.Second)))
	tx.AddAction(ctx, store.Action{SessionID: sid, TS: storeTime(now.Add(-time.Second)), Resource: "command", Action: "execute", Path: "ls", Evidence: "log"})
	// Rolling 24h window: 23h ago counts, 25h ago doesn't.
	tx.AddAction(ctx, store.Action{SessionID: sid, TS: storeTime(now.Add(-23 * time.Hour)), Resource: "command", Action: "execute", Path: "pwd", Evidence: "log"})
	tx.AddAction(ctx, store.Action{SessionID: sid, TS: storeTime(now.Add(-25 * time.Hour)), Resource: "command", Action: "execute", Path: "old", Evidence: "log"})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s.sock")
	lis, err := api.Listen(sock, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go api.Serve(sctx, lis, api.NewServer(st.DB))

	t.Setenv(socketEnv, sock)
	out, err := run(t, "st")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Monitored from ", "(just now)", "NEEDS ATTENTION (4) (sample data)",
		"LAST 24H\n", "command\texecute\t2\t\n", "PROMPTS (1)\n", "\tclaude\t\ts\t<1m+\t0\t\tfix the flaky test\n", "  sen session <id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func storeTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
