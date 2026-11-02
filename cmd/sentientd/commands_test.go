package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nosaka4i/sentient/internal/version"
)

func TestVersionFlag(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), version.Version) {
		t.Errorf("--version = %q", out.String())
	}
}

func TestRunReportsStartupError(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--data-dir", "/proc/nope", "--socket", "/proc/nope/s.sock", "--home", "/nonexistent"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("want error for unwritable data dir")
	}
}

func TestVerbosityFlag(t *testing.T) {
	cmd := newRootCmd()
	f := cmd.Flags().Lookup("verbosity")
	if f == nil || f.DefValue != "essential" {
		t.Fatalf("verbosity flag = %v", f)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--verbosity", "detailed", "--data-dir", t.TempDir(), "--socket", t.TempDir() + "/s.sock", "--home", "/nonexistent"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown verbosity") {
		t.Errorf("err = %v", err)
	}
}
