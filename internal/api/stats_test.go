package api

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nosaka4i/sentient/internal/health"
	"github.com/nosaka4i/sentient/internal/store"
)

func TestStatsSnapshot(t *testing.T) {
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := NewStats()
	s.now = func() time.Time { return clock }
	s.started = clock.Add(-time.Hour)

	for i := 1; i <= 100; i++ {
		s.record("Status", time.Duration(i)*time.Millisecond, nil)
	}
	s.record("Health", time.Millisecond, status.Error(codes.Unavailable, "x"))
	s.record("Health", time.Millisecond, errors.New("plain")) // Unknown code
	s.record("Health", time.Millisecond, status.Error(codes.Unavailable, "y"))

	a := s.Snapshot()
	if a.Requests != 103 || a.Errors != 3 {
		t.Errorf("requests %d errors %d", a.Requests, a.Errors)
	}
	if a.Methods[0] != (health.NamedCount{Name: "Status", Count: 100}) || a.Methods[1].Count != 3 {
		t.Errorf("methods = %+v", a.Methods)
	}
	if a.ErrorsByCode[0] != (health.NamedCount{Name: "Unavailable", Count: 2}) || a.ErrorsByCode[1].Name != "Unknown" {
		t.Errorf("errors = %+v", a.ErrorsByCode)
	}
	if a.LatencyCount != 103 || a.P50 < 45*time.Millisecond || a.P50 > 55*time.Millisecond || a.P99 < 95*time.Millisecond {
		t.Errorf("latency p50 %v p95 %v p99 %v (n=%d)", a.P50, a.P95, a.P99, a.LatencyCount)
	}
	// All 103 requests happened in the current second: 103 over the last minute.
	if want := 103.0 / 60; a.PerSecNow < want-0.01 || a.PerSecNow > want+0.01 {
		t.Errorf("per sec = %v, want %v", a.PerSecNow, want)
	}

	// A minute later the rate window is empty; totals stay.
	clock = clock.Add(2 * time.Minute)
	if a := s.Snapshot(); a.PerSecNow != 0 || a.Requests != 103 {
		t.Errorf("later: per sec %v requests %d", a.PerSecNow, a.Requests)
	}

	// The latency ring keeps only the newest samples.
	for i := 0; i < latencySamples+10; i++ {
		s.record("Status", time.Second, nil)
	}
	if a := s.Snapshot(); a.LatencyCount != latencySamples || a.P50 != time.Second {
		t.Errorf("ring: n %d p50 %v", a.LatencyCount, a.P50)
	}

	// Early after start the rate divides by uptime, not a full minute.
	s2 := NewStats()
	s2.record("Status", 0, nil)
	if a := s2.Snapshot(); a.PerSecNow < 0.9 {
		t.Errorf("fresh per sec = %v", a.PerSecNow)
	}
}

func TestHealthOverSocket(t *testing.T) {
	ctx := context.Background()
	dir := shortDir(t)
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(st.DB)

	sock := filepath.Join(dir, "h.sock")
	lis, err := Listen(sock, "", quiet)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- Serve(sctx, lis, srv) }()
	defer func() { cancel(); <-done }()

	c, err := Dial(sock, "sen serve")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Without a health source the call is Unimplemented.
	if _, err := c.Health(ctx); status.Code(err) != codes.Unimplemented {
		t.Errorf("no health func: %v", err)
	}
	srv.SetHealth(func(ctx context.Context) (*health.Report, error) { return nil, errors.New("boom") })
	if _, err := c.Health(ctx); status.Code(err) != codes.Internal {
		t.Errorf("failing health func: %v", err)
	}

	want := &health.Report{
		CheckedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC),
		Daemon: health.Daemon{Version: "1", Started: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), CPUPercent: 0.5,
			CPUWindow: time.Minute, RSSBytes: 10, Socket: sock, User: "u", Root: true},
		Storage:   health.Storage{DataDir: "d", DBPath: "d/x.db", DBBytes: 1, FreeBytes: 2, SizeBytes: 3},
		Ingest:    []health.IngestAgent{{Agent: "claude", Files: 2, Unknown: 1}},
		Integrity: health.Integrity{Records: 5, OK: true, Interval: time.Hour, BadRecordID: 0},
		Agents:    []health.Agent{{Agent: "claude", Version: "2.0", Logs: "~/.claude/projects"}},
		Live:      []health.LiveSession{{SessionID: "s", Agent: "claude", Repo: "r", LastActivity: time.Date(2026, 10, 17, 11, 0, 0, 0, time.UTC)}},
		Sudo:      health.Sudo{Checked: true, Passwordless: []string{"ubuntu"}},
	}
	srv.SetHealth(func(ctx context.Context) (*health.Report, error) { cp := *want; return &cp, nil })
	got, err := c.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Daemon, want.Daemon) || !reflect.DeepEqual(got.Storage, want.Storage) ||
		!reflect.DeepEqual(got.Ingest, want.Ingest) || !reflect.DeepEqual(got.Agents, want.Agents) ||
		!reflect.DeepEqual(got.Live, want.Live) || !reflect.DeepEqual(got.Sudo, want.Sudo) ||
		!got.CheckedAt.Equal(want.CheckedAt) || !got.HistoryBegins.IsZero() ||
		got.Integrity.Records != 5 || !got.Integrity.OK || got.Integrity.Interval != time.Hour || !got.Integrity.CheckedAt.IsZero() {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
	// The server adds its own traffic: the three Health calls so far
	// (the snapshot is taken during the third), from this named client.
	if got.API.Requests != 2 || got.API.Errors != 2 || !reflect.DeepEqual(got.API.Clients, []string{"sen serve"}) {
		t.Errorf("api = %+v", got.API)
	}
	if !strings.Contains(joinNames(got.API.ErrorsByCode), "Unimplemented") {
		t.Errorf("errors by code = %+v", got.API.ErrorsByCode)
	}
}

func joinNames(c []health.NamedCount) string {
	var s []string
	for _, x := range c {
		s = append(s, x.Name)
	}
	return strings.Join(s, ",")
}
