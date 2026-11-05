package merge

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/store"
)

// runFor runs the merger for a short while and returns what it logged. The
// store is closed, so any pass it makes fails and says so.
func runFor(t *testing.T, paused func() bool) string {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	var logs bytes.Buffer
	m := &Merger{St: st, Log: slog.New(slog.NewTextHandler(&logs, nil)), Paused: paused}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := m.Run(ctx, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	return logs.String()
}

func TestMergerMakesNoPassWhilePaused(t *testing.T) {
	if logs := runFor(t, func() bool { return true }); logs != "" {
		t.Errorf("a pass was made while paused:\n%s", logs)
	}
	if logs := runFor(t, func() bool { return false }); !strings.Contains(logs, "merge log and kernel") {
		t.Errorf("no pass when not paused:\n%s", logs)
	}
	if logs := runFor(t, nil); !strings.Contains(logs, "merge log and kernel") {
		t.Errorf("no pass without a Paused func:\n%s", logs)
	}
}
