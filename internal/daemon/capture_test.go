package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nosaka4i/sentient/internal/collector/tetragon/policy"
	"github.com/nosaka4i/sentient/internal/store"
)

func captureEnv(files map[string]string) policy.Env {
	return policy.Env{
		Getenv: func(string) string { return "" },
		GoEnv:  func() map[string]string { return nil },
		IsDir:  func(string) bool { return false },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

func startWith(t *testing.T, cfg Config, files map[string]string) (*capture, string, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var logs bytes.Buffer
	c, err := startCapture(context.Background(), st, cfg, captureEnv(files), time.Now(), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return c, logs.String(), st
}

func installedPolicy(t *testing.T, level policy.Verbosity) string {
	t.Helper()
	p, err := policy.RenderWith(policy.Options{
		Binaries: []string{"/h/.local/bin/claude"}, Verbosity: level,
		Noise: []policy.NoisePath{{Path: "/mnt/builds/cache", Category: "listed in ~/.config/sentient/noise-paths"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartCaptureAgreeingWithThePolicy(t *testing.T) {
	cfg := Config{Verbosity: policy.Essential, Homes: []string{"/h"}, PolicyFile: "/p.yaml"}
	c, logs, st := startWith(t, cfg, map[string]string{"/p.yaml": installedPolicy(t, policy.Essential)})
	if strings.Contains(logs, "WARN") {
		t.Errorf("warned although the levels agree:\n%s", logs)
	}
	if !strings.Contains(logs, "capture level") {
		t.Errorf("no startup line:\n%s", logs)
	}
	found := false
	for _, n := range c.noise {
		found = found || n.Path == "/mnt/builds/cache" // from the policy, not from this machine
	}
	if !found {
		t.Errorf("noise lacks the policy's location: %v", c.noise)
	}
	if got, _ := st.CaptureLevels(context.Background()); len(got) != 1 || got[0].Level != "essential" {
		t.Errorf("levels = %+v", got)
	}
}

func TestStartCaptureWarnsWhenThePolicyIsMoreVerbose(t *testing.T) {
	cfg := Config{Verbosity: policy.Essential, Homes: []string{"/h"}, PolicyFile: "/p.yaml"}
	_, logs, _ := startWith(t, cfg, map[string]string{"/p.yaml": installedPolicy(t, policy.Complete)})
	if !strings.Contains(logs, "policy level differs") || !strings.Contains(logs, "policy=complete") || !strings.Contains(logs, "sentientd=essential") ||
		!strings.Contains(logs, "sen policy --verbosity essential") {
		t.Errorf("no mismatch warning:\n%s", logs)
	}
}

func TestStartCaptureWithoutAUsablePolicyFile(t *testing.T) {
	cfg := Config{Verbosity: policy.Complete, Homes: []string{"/h"}, PolicyFile: "/p.yaml"}
	c, logs, _ := startWith(t, cfg, nil) // no policy file: nothing to compare
	if strings.Contains(logs, "WARN") || c.level != policy.Complete {
		t.Errorf("level %q, logs:\n%s", c.level, logs)
	}
	_, logs, _ = startWith(t, cfg, map[string]string{"/p.yaml": "apiVersion: cilium.io/v1alpha1\n"}) // from before levels
	if !strings.Contains(logs, "no verbosity header") {
		t.Errorf("no warning for a headerless policy:\n%s", logs)
	}
}

func TestStartCaptureLogsALevelChange(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	for _, v := range []policy.Verbosity{policy.Complete, policy.Complete, policy.Essential} {
		if _, err := startCapture(ctx, st, Config{Verbosity: v}, captureEnv(nil), time.Now(), log); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(logs.String(), "capture level changed") || !strings.Contains(logs.String(), "from=complete to=essential") {
		t.Errorf("no change line:\n%s", logs.String())
	}
	if n := strings.Count(logs.String(), "capture level changed"); n != 1 {
		t.Errorf("%d change lines, want 1", n)
	}
}

func TestStartCaptureReportsAStoreError(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := startCapture(context.Background(), st, Config{Verbosity: policy.Essential}, captureEnv(nil), time.Now(), slog.Default()); err == nil {
		t.Error("closed store: no error")
	}
}

func TestRunRejectsAnUnknownVerbosity(t *testing.T) {
	err := Run(context.Background(), Config{DataDir: t.TempDir(), Socket: filepath.Join(t.TempDir(), "s.sock"), Verbosity: "detailed"})
	if err == nil || !strings.Contains(err.Error(), "unknown verbosity") {
		t.Errorf("err = %v", err)
	}
}

func TestCaptureReport(t *testing.T) {
	installed := time.Date(2026, 11, 2, 0, 59, 0, 0, time.UTC)
	mtime := func(string) time.Time { return installed }
	file := func(text string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != "/p.yaml" {
				return nil, os.ErrNotExist
			}
			return []byte(text), nil
		}
	}
	cfg := Config{Tetragon: "unix:///x", Verbosity: policy.Essential, PolicyFile: "/p.yaml"}

	c := captureReport(cfg, file(installedPolicy(t, policy.Complete)), mtime)
	if !c.Enabled || c.Level != "essential" || !c.PolicyKnown || c.PolicyLevel != "complete" || !c.PolicyInstalled.Equal(installed) || c.PolicyFile != "/p.yaml" {
		t.Errorf("mismatching policy: %+v", c)
	}
	if c = captureReport(cfg, file("apiVersion: v1\n"), mtime); c.PolicyKnown || !c.Enabled {
		t.Errorf("headerless policy: %+v", c)
	}
	if c = captureReport(cfg, func(string) ([]byte, error) { return nil, os.ErrNotExist }, mtime); c.PolicyKnown {
		t.Errorf("missing policy: %+v", c)
	}
	off := cfg
	off.Tetragon = ""
	if c = captureReport(off, file(installedPolicy(t, policy.Complete)), mtime); c.Enabled || c.PolicyKnown {
		t.Errorf("capture off: %+v", c)
	}
	none := Config{Tetragon: "unix:///x", Verbosity: policy.Essential}
	if c = captureReport(none, func(string) ([]byte, error) { return nil, os.ErrNotExist }, mtime); c.PolicyFile != policy.DefaultPolicyFile {
		t.Errorf("default policy file = %q", c.PolicyFile)
	}
}

func TestFileModTime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if !fileModTime(p).IsZero() {
		t.Error("missing file has a time")
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if time.Since(fileModTime(p)) > time.Minute {
		t.Error("fresh file is old")
	}
}
