package attribute

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

// runFor runs the attributor for a short while and returns what it logged. The
// store is closed, so any pass it makes fails and says so.
func runFor(t *testing.T, paused func() bool) string {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	var logs bytes.Buffer
	a := &Attributor{St: st, Log: slog.New(slog.NewTextHandler(&logs, nil)), Paused: paused, IsAgent: func(string) bool { return false }}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := a.Run(ctx, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	return logs.String()
}

func TestAttributorMakesNoPassWhilePaused(t *testing.T) {
	if logs := runFor(t, func() bool { return true }); logs != "" {
		t.Errorf("a pass was made while paused:\n%s", logs)
	}
	if logs := runFor(t, func() bool { return false }); !strings.Contains(logs, "attribute processes") {
		t.Errorf("no pass when not paused:\n%s", logs)
	}
	if logs := runFor(t, nil); !strings.Contains(logs, "attribute processes") {
		t.Errorf("no pass without a Paused func:\n%s", logs)
	}
}
