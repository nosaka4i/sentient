package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPolicyNamesTheGivenBinariesExactly(t *testing.T) {
	p, err := Render([]string{"/home/u/.local/bin/claude", "/home/u/.local/share/claude/versions/2.1.9"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kind: TracingPolicy",
		`name: "sentient"`,
		`operator: "In"`,
		`- "/home/u/.local/bin/claude"`,
		`- "/home/u/.local/share/claude/versions/2.1.9"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("policy lacks %q", want)
		}
	}
	// Tetragon 1.7.1 rejects followChildren with a prefix match, and a rejected policy takes Tetragon down
	if strings.Contains(p, "Prefix") || strings.Contains(p, "{{") {
		t.Error("prefix operator or template syntax in the policy")
	}
	// every hook is limited to the agent tree
	hooks, selectors, follow := strings.Count(p, "- call:"), strings.Count(p, "matchBinaries:"), strings.Count(p, "followChildren: true")
	if hooks == 0 || hooks != selectors || hooks != follow {
		t.Errorf("%d hooks, %d matchBinaries, %d followChildren", hooks, selectors, follow)
	}
	// every hook the collector turns into an event is present
	for _, call := range []string{"security_file_open", "security_path_mknod", "security_path_unlink", "security_path_mkdir",
		"security_path_rmdir", "security_path_rename", "security_path_chmod", "tcp_connect", "sys_kill", "sys_tgkill"} {
		if !strings.Contains(p, `call: "`+call+`"`) {
			t.Errorf("no hook for %s", call)
		}
	}
}

func TestRenderRejectsBadBinaries(t *testing.T) {
	for _, bins := range [][]string{nil, {"relative/claude"}, {`/home/x"y`}, {"/home/a\nb"}, {`/home/a\b`}} {
		if _, err := Render(bins); err == nil {
			t.Errorf("Render(%q) succeeded", bins)
		}
	}
}

func TestDiscoverFindsTheLauncherAndEveryVersion(t *testing.T) {
	home := t.TempDir()
	versions := filepath.Join(home, ".local/share/claude/versions")
	must(t, os.MkdirAll(versions, 0o755))
	must(t, os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755))
	for _, v := range []string{"2.1.8", "2.1.9"} {
		must(t, os.WriteFile(filepath.Join(versions, v), []byte("x"), 0o755))
	}
	must(t, os.Symlink(filepath.Join(versions, "2.1.9"), filepath.Join(home, ".local/bin/claude")))
	must(t, os.MkdirAll(filepath.Join(versions, "a-directory"), 0o755)) // not a binary

	noSystemBinaries(t) // the box this runs on may have one in /usr/local/bin
	got := Discover([]string{home, filepath.Join(home, "nobody")})
	want := []string{
		filepath.Join(home, ".local/bin/claude"),
		filepath.Join(versions, "2.1.8"),
		filepath.Join(versions, "2.1.9"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("Discover = %q, want %q", got, want)
	}
}

func TestDiscoverFindsNothingWhenNothingIsInstalled(t *testing.T) {
	noSystemBinaries(t)
	if got := Discover([]string{t.TempDir()}); len(got) != 0 {
		t.Errorf("Discover = %q", got)
	}
}

func TestAgentPrefixes(t *testing.T) {
	got := AgentPrefixes([]string{"/home/a"})
	want := []string{"/usr/local/bin/claude", "/home/a/.local/bin/claude", "/home/a/.local/share/claude/versions/"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func noSystemBinaries(t *testing.T) {
	t.Helper()
	old := systemBinaries
	systemBinaries = nil
	t.Cleanup(func() { systemBinaries = old })
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
