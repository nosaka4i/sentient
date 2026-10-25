package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestParseVmRSS(t *testing.T) {
	n, ok := parseVmRSS([]byte("Name:\tsentientd\nVmPeak:\t 900 kB\nVmRSS:\t   31000 kB\n"))
	if !ok || n != 31000*1024 {
		t.Errorf("parseVmRSS = %d %v", n, ok)
	}
	if _, ok := parseVmRSS([]byte("Name: x\n")); ok {
		t.Error("no VmRSS line should not parse")
	}
	if rssBytes() <= 0 {
		t.Error("rssBytes should be positive")
	}
}

func TestCPUSampler(t *testing.T) {
	clock := time.Unix(1000, 0)
	cpu := time.Duration(0)
	c := &cpuSampler{now: func() time.Time { return clock }, cpuTime: func() time.Duration { return cpu }}
	c.sample()
	if p, w := c.percent(); p != 0 || w != 0 {
		t.Errorf("no time passed: %v %v", p, w)
	}
	for i := 0; i < 20; i++ {
		clock = clock.Add(30 * time.Second)
		cpu += 300 * time.Millisecond // 1% of a core
		c.sample()
	}
	p, w := c.percent()
	if p < 0.99 || p > 1.01 || w != 5*time.Minute {
		t.Errorf("percent = %v over %v, want 1%% over 5m (window capped)", p, w)
	}
	if processCPUTime() <= 0 {
		t.Error("processCPUTime should be positive")
	}
	if newCPUSampler(time.Now) == nil {
		t.Error("newCPUSampler")
	}
}

func TestIntegrity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	add := func(n int) {
		tx, _ := st.Begin(ctx)
		for i := 0; i < n; i++ {
			tx.AppendRaw(ctx, store.RawRecord{SourceFile: "f", Type: "user", Record: "{}"})
		}
		tx.Commit()
	}
	add(3)
	clock := time.Unix(1000, 0)
	in := &integrity{st: st, now: func() time.Time { return clock }, interval: time.Hour, log: quiet}

	in.incremental(ctx) // before the first full check: skipped
	if s := in.state(); !s.CheckedAt.IsZero() {
		t.Fatalf("incremental before full ran: %+v", s)
	}
	in.full(ctx)
	if s := in.state(); !s.OK || s.Records != 3 || !s.FullAt.Equal(clock) {
		t.Fatalf("after full: %+v", s)
	}

	add(2)
	in.incremental(ctx) // throttled: too soon
	if s := in.state(); s.Records != 3 {
		t.Errorf("throttled incremental ran: %+v", s)
	}
	clock = clock.Add(time.Minute)
	in.incremental(ctx)
	if s := in.state(); s.Records != 5 || !s.CheckedAt.Equal(clock) || !s.FullAt.Equal(time.Unix(1000, 0)) {
		t.Errorf("after incremental: %+v", s)
	}

	st.DB.Exec(`DROP TRIGGER raw_log_records_no_update`)
	st.DB.Exec(`UPDATE raw_log_records SET record = '{"x":1}' WHERE id = 2`)
	in.full(ctx)
	if s := in.state(); s.OK || s.BadRecordID != 2 {
		t.Errorf("tampered: %+v", s)
	}
	clock = clock.Add(time.Minute)
	in.incremental(ctx) // a failure stands until the next full check
	if s := in.state(); s.OK {
		t.Errorf("incremental cleared a failure: %+v", s)
	}

	// The loop runs a full check and stops with its context.
	lctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { in.loop(lctx); close(done) }()
	cancel()
	<-done

	st.Close()
	in.full(ctx) // errors are logged, state kept
	if s := in.state(); s.BadRecordID != 2 {
		t.Errorf("state after failed run: %+v", s)
	}
}

func TestSudoChecker(t *testing.T) {
	clock := time.Unix(1000, 0)
	calls := 0
	outputs := map[string]string{
		"ubuntu": "User ubuntu may run the following commands on box:\n    (ALL : ALL) ALL\n    (ALL) NOPASSWD: ALL\n",
		"dev":    "User dev may run the following commands on box:\n    (ALL : ALL) ALL\n",
	}
	run := func(ctx context.Context, u string) (string, error) {
		calls++
		if out, ok := outputs[u]; ok {
			return out, nil
		}
		return "", errors.New("exit status 1")
	}
	s := &sudoChecker{now: func() time.Time { return clock }, run: run, root: false, users: []string{"ubuntu"}}
	if r := s.check(context.Background()); r.Checked || r.Reason != "unknown (sentientd not running as root)" {
		t.Errorf("unprivileged = %+v", r)
	}

	s.root = true
	s.users = nil
	if r := s.check(context.Background()); r.Checked || r.Reason == "" {
		t.Errorf("no users = %+v", r)
	}

	s.users = []string{"dev", "ubuntu", "ghost"}
	r := s.check(context.Background())
	if !r.Checked || len(r.Passwordless) != 1 || r.Passwordless[0] != "ubuntu" || calls != 3 {
		t.Errorf("check = %+v (calls %d)", r, calls)
	}
	s.check(context.Background()) // cached
	if calls != 3 {
		t.Errorf("cache not used: %d calls", calls)
	}
	clock = clock.Add(sudoCacheFor + time.Second)
	s.users = []string{"ghost"}
	if r := s.check(context.Background()); r.Checked || r.Reason != "unknown (sudo -l failed)" {
		t.Errorf("all failed = %+v", r)
	}
}

func TestAgentAccountsAndStorage(t *testing.T) {
	home := t.TempDir()
	other := t.TempDir() // no ~/.claude/projects: not an agent account
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	me, _ := user.Current()
	got := agentAccounts([]string{home, other, home})
	if len(got) != 1 || got[0] != me.Username {
		t.Errorf("agentAccounts = %v, want [%s]", got, me.Username)
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, store.DBFileName), make([]byte, 1000), 0o600)
	os.WriteFile(filepath.Join(dir, store.DBFileName+"-wal"), make([]byte, 24), 0o600)
	s := storage(dir)
	if s.DBBytes != 1024 || s.FreeBytes <= 0 || s.SizeBytes < s.FreeBytes || s.DBPath != filepath.Join(dir, "sentient.db") {
		t.Errorf("storage = %+v", s)
	}
}
