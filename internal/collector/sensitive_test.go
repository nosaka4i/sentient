package collector

import (
	"context"
	"strings"
	"testing"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

func TestSensitivePaths(t *testing.T) {
	got := SensitivePaths([]string{"/Users/a", "/Users/b"})
	has := func(want string) bool {
		for _, g := range got {
			if g == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"/Users/a/.ssh/", "/Users/b/.aws/", "/Users/a/.config/gh/", "/Users/a/.netrc", "/Users/a/.docker/config.json"} {
		if !has(want) {
			t.Errorf("%s missing from %v", want, got)
		}
	}
	if len(got) != 18 {
		t.Errorf("%d entries, want 9 per home", len(got))
	}
	if SensitivePaths(nil) != nil {
		t.Error("no homes, no entries")
	}
}

// recording is a Sink that remembers the events it gets.
type recording struct {
	Sink
	events []store.KernelEvent
}

func (r *recording) Event(_ context.Context, e store.KernelEvent) error {
	r.events = append(r.events, e)
	return nil
}

func TestFilterSinkKeepsSensitiveOpensInsideNoise(t *testing.T) {
	rec := &recording{}
	f := &FilterSink{
		Sink:      rec,
		Level:     policy.Essential,
		Noise:     []policy.NoisePath{{Path: "/Users/a/Library/", Category: "user library"}},
		Sensitive: []string{"/Users/a/Library/Keychains/"},
	}
	ctx := context.Background()
	for _, p := range []string{"/Users/a/Library/Keychains/login.keychain-db", "/Users/a/Library/Caches/x", "/Users/a/proj/main.go"} {
		if err := f.Event(ctx, store.KernelEvent{Type: "file_open", Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	var kept []string
	for _, e := range rec.events {
		kept = append(kept, e.Path)
	}
	if strings.Join(kept, " ") != "/Users/a/Library/Keychains/login.keychain-db /Users/a/proj/main.go" {
		t.Errorf("kept %v", kept)
	}
	if f.Dropped()["user library"] != 1 {
		t.Errorf("dropped %v", f.Dropped())
	}
}
