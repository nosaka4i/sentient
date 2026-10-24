package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agentHome is a home directory with Claude installed the way its installer
// does: a versioned binary and a launcher link.
func agentHome(t *testing.T, version string) string {
	t.Helper()
	home := t.TempDir()
	versions := filepath.Join(home, ".local/share/claude/versions")
	for _, d := range []string{versions, filepath.Join(home, ".local/bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(versions, version), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(versions, version), filepath.Join(home, ".local/bin/claude")); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestPolicyNamesTheInstalledAgentForTheUsersHome(t *testing.T) {
	home := agentHome(t, "2.1.307")
	t.Setenv("HOME", home)
	out, err := run(t, "policy")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "# sentient:") || !strings.Contains(out, `"`+home+`/.local/share/claude/versions/2.1.307"`) ||
		!strings.Contains(out, `"`+home+`/.local/bin/claude"`) {
		t.Errorf("policy = %.300s", out)
	}
}

func TestPolicyHomeFlagIsRepeatable(t *testing.T) {
	a, b := agentHome(t, "1.0.0"), agentHome(t, "2.0.0")
	out, err := run(t, "policy", "--home", a, "--home", b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{a + "/.local/share/claude/versions/1.0.0", b + "/.local/share/claude/versions/2.0.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("policy lacks %s", want)
		}
	}
}

func TestPolicyWithNoAgentSaysSo(t *testing.T) {
	if _, err := run(t, "policy", "--home", t.TempDir()); err == nil || !strings.Contains(err.Error(), "no agent found") {
		t.Errorf("err = %v", err)
	}
}

func TestPolicyWithoutAHomeSaysSo(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := run(t, "policy"); err == nil || !strings.Contains(err.Error(), "--home") {
		t.Errorf("err = %v", err)
	}
}

func TestPolicyCheckFailsWithoutAnInstalledPolicy(t *testing.T) {
	home := agentHome(t, "2.1.307")
	out, err := run(t, "policy", "--check", "--home", home, "--policy-file", filepath.Join(t.TempDir(), "none.yaml"))
	if err == nil || !strings.Contains(err.Error(), "policy check failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "! policy:") || !strings.Contains(out, "not installed") {
		t.Errorf("out = %q", out)
	}
}
