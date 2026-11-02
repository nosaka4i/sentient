package collector

import (
	"context"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

// recordingSink remembers the events that reached it.
type recordingSink struct {
	Sink
	events []store.KernelEvent
}

func (r *recordingSink) Event(_ context.Context, e store.KernelEvent) error {
	r.events = append(r.events, e)
	return nil
}

func (r *recordingSink) Exit(context.Context, string, time.Time, int) error { return nil }

var filterNoise = []policy.NoisePath{
	{Path: "/home/u/.cache/go-build", Category: "Go build cache"},
	{Path: "/usr/lib", Category: "system libraries"},
}

func feedLevel(t *testing.T, level policy.Verbosity) (*FilterSink, *recordingSink) {
	t.Helper()
	rec := &recordingSink{}
	f := &FilterSink{Sink: rec, Level: level, Noise: filterNoise}
	ctx := context.Background()
	for _, e := range []store.KernelEvent{
		{Type: "file_open", Path: "/home/u/.cache/go-build/ab/cd"},
		{Type: "file_open", Path: "/usr/lib/x86_64-linux-gnu/libc.so.6"},
		{Type: "file_open", Path: "/proc/123/stat"},
		{Type: "file_open", Path: "/proc/123/environ"},
		{Type: "file_open", Path: "/home/u/work/repo/main.go"},
		{Type: "file_create", Path: "/home/u/.cache/go-build/ab/new"}, // a write is never noise
		{Type: "exec", Path: "/usr/lib/go/bin/go"},                    // nor is an exec
	} {
		if err := f.Event(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	return f, rec
}

func TestEssentialDropsOnlyNoiseOpens(t *testing.T) {
	f, rec := feedLevel(t, policy.Essential)
	var kept []string
	for _, e := range rec.events {
		kept = append(kept, e.Type+" "+e.Path)
	}
	want := []string{"file_open /proc/123/environ", "file_open /home/u/work/repo/main.go", "file_create /home/u/.cache/go-build/ab/new", "exec /usr/lib/go/bin/go"}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, kept[i], want[i])
		}
	}
	d := f.Dropped()
	if d["Go build cache"] != 1 || d["system libraries"] != 1 || d["agent polling itself"] != 1 || len(d) != 3 {
		t.Errorf("dropped = %v", d)
	}
}

func TestExtendedAlsoDropsAndCounts(t *testing.T) {
	f, rec := feedLevel(t, policy.Extended)
	if len(rec.events) != 4 {
		t.Errorf("kept %d events, want 4", len(rec.events))
	}
	if len(f.Dropped()) != 3 {
		t.Errorf("dropped = %v", f.Dropped())
	}
}

func TestCompleteKeepsEverything(t *testing.T) {
	f, rec := feedLevel(t, policy.Complete)
	if len(rec.events) != 7 || len(f.Dropped()) != 0 {
		t.Errorf("kept %d, dropped %v", len(rec.events), f.Dropped())
	}
}

func TestFilterPassesOtherCallsThrough(t *testing.T) {
	rec := &recordingSink{}
	f := &FilterSink{Sink: rec, Level: policy.Essential}
	if err := f.Exit(context.Background(), "e", time.Now(), 0); err != nil {
		t.Fatal(err)
	}
}
