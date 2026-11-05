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

func TestDiskThresholdFlags(t *testing.T) {
	cmd := newRootCmd()
	if f := cmd.Flags().Lookup("disk-high-threshold"); f == nil || f.DefValue != "80" {
		t.Fatalf("disk-high-threshold flag = %v", f)
	}
	if f := cmd.Flags().Lookup("disk-low-threshold"); f == nil || f.DefValue != "0" { // 0: five below the high one
		t.Fatalf("disk-low-threshold flag = %v", f)
	}
	// refused before anything is opened (0 means "the default" and would start the daemon)
	for _, c := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--disk-high-threshold", "9"}, "--disk-high-threshold must be between 10 and 99"},
		{[]string{"--disk-high-threshold", "100"}, "--disk-high-threshold must be between 10 and 99"},
		{[]string{"--disk-high-threshold", "-5"}, "--disk-high-threshold must be between 10 and 99"},
		{[]string{"--disk-high-threshold", "80", "--disk-low-threshold", "80"}, "--disk-low-threshold must be between 1 and 79 (below --disk-high-threshold 80)"},
		{[]string{"--disk-high-threshold", "60", "--disk-low-threshold", "75"}, "--disk-low-threshold must be between 1 and 59"},
		{[]string{"--disk-low-threshold", "-3"}, "--disk-low-threshold must be between 1 and 79"},
	} {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append(c.flags, "--data-dir", t.TempDir(), "--socket", t.TempDir()+"/s.sock", "--home", "/nonexistent"))
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.flags, err, c.want)
		}
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
