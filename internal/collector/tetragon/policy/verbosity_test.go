package policy

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseVerbosity(t *testing.T) {
	for in, want := range map[string]Verbosity{
		"": Essential, "essential": Essential, " Extended ": Extended, "COMPLETE": Complete,
	} {
		if got, err := ParseVerbosity(in); err != nil || got != want {
			t.Errorf("ParseVerbosity(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"standard", "full", "detailed", "verbose", "essentail"} {
		if _, err := ParseVerbosity(bad); err == nil {
			t.Errorf("ParseVerbosity(%q) succeeded", bad)
		}
	}
}

// fakeEnv is a machine: dirs that exist, files, the Go environment, variables.
func fakeEnv(dirs []string, files map[string]string, goenv map[string]string, vars map[string]string) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		GoEnv:  func() map[string]string { return goenv },
		IsDir:  func(p string) bool { return slices.Contains(dirs, p) },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
	}
}

func paths(ns []NoisePath) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Path
	}
	return out
}

func TestDetectNoiseFindsWhatThisMachineHas(t *testing.T) {
	env := fakeEnv(
		[]string{"/usr/local/go1.26", "/h/.cache/go-build", "/h/go/pkg/mod", "/h/.cargo/registry", "/h/.npm/_cacache", "/h/.nvm/versions"},
		nil,
		map[string]string{"GOROOT": "/usr/local/go1.26", "GOCACHE": "/h/.cache/go-build", "GOMODCACHE": "/h/go/pkg/mod"},
		nil)
	got := DetectNoise([]string{"/h"}, env)
	for _, want := range []string{
		"/usr/lib", "/lib", "/usr/share", "/etc/ld.so", // always
		"/usr/local/go1.26", "/h/.cache/go-build", "/h/go/pkg/mod", // from go env
		"/h/.cargo/registry", "/h/.npm/_cacache", "/h/.nvm/versions", // found on disk
	} {
		if !slices.Contains(paths(got), want) {
			t.Errorf("noise lacks %s: %v", want, paths(got))
		}
	}
	for _, absent := range []string{"/h/.cargo/git", "/h/.cache/pip"} { // not on this machine
		if slices.Contains(paths(got), absent) {
			t.Errorf("noise has %s, which does not exist", absent)
		}
	}
	if !slices.IsSorted(paths(got)) {
		t.Errorf("not sorted: %v", paths(got))
	}
	for _, n := range got {
		if n.Category == "" {
			t.Errorf("%s has no category: summaries group by it", n.Path)
		}
	}
	// the categories are the ones the extended level groups by
	cats := map[string]string{}
	for _, n := range got {
		cats[n.Path] = n.Category
	}
	if cats["/h/.cache/go-build"] != "Go build cache" || cats["/usr/lib"] != "system libraries" || cats["/h/.npm/_cacache"] != "npm cache" {
		t.Errorf("categories = %v", cats)
	}
}

func TestDetectNoiseWithoutGoAndWithEnvironmentOverrides(t *testing.T) {
	env := fakeEnv(
		[]string{"/opt/cargo/registry", "/opt/nvm/versions"}, nil, nil,
		map[string]string{"CARGO_HOME": "/opt/cargo", "NVM_DIR": "/opt/nvm"})
	got := paths(DetectNoise([]string{"/h"}, env))
	for _, want := range []string{"/opt/cargo/registry", "/opt/nvm/versions"} {
		if !slices.Contains(got, want) {
			t.Errorf("noise lacks %s: %v", want, got)
		}
	}
	if len(got) != len(systemNoise)+2 {
		t.Errorf("without Go, only the system directories and what exists: %v", got)
	}
}

func TestDetectNoiseReadsTheUsersExtrasAndDropsUnsafePaths(t *testing.T) {
	extras := "# my machine\n/data/builds   # big scratch\n\nrelative/dir\n/has\"quote\n/ok/dir\n/\n/dup\n/dup\n"
	env := fakeEnv(nil, map[string]string{"/h/" + ExtrasFile: extras}, nil, nil)
	got := paths(DetectNoise([]string{"/h", "/nobody"}, env)) // /nobody has no extras file
	for _, want := range []string{"/data/builds", "/ok/dir", "/dup"} {
		if !slices.Contains(got, want) {
			t.Errorf("extras lack %s: %v", want, got)
		}
	}
	for _, bad := range []string{"relative/dir", "/has\"quote", "/"} {
		if slices.Contains(got, bad) {
			t.Errorf("unsafe or meaningless path %q kept", bad)
		}
	}
	if n := strings.Count(strings.Join(got, "\n"), "/dup"); n != 1 {
		t.Errorf("/dup appears %d times", n)
	}
}

var testBins = []string{"/h/.local/bin/claude", "/h/.local/share/claude/versions/2.1.9"}

func TestEssentialLeavesOutTheNoiseAndKeepsTheRestOfProc(t *testing.T) {
	noise := []NoisePath{{"/h/.cache/go-build", "Go build cache"}, {"/usr/lib", "system libraries"}}
	p, err := RenderWith(Options{Binaries: testBins, Verbosity: Essential, Noise: noise})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Verbosity: essential.",
		"#   /h/.cache/go-build",
		"(Go build cache)",
		"/proc/<pid>/stat",
		// the first selector: regular files, not under any noise prefix nor /proc
		`operator: "NotPrefix"`,
		`- "/h/.cache/go-build"`, `- "/usr/lib"`, `- "/proc/"`,
		// the second selector: /proc, except stat and statm (environ and mem stay recorded)
		`operator: "Prefix"`, `operator: "NotPostfix"`, `- "/stat"`, `- "/statm"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("essential policy lacks %q", want)
		}
	}
	// both selectors name the agents and follow children, and there are exactly two on file_open
	hook := p[strings.Index(p, `call: "security_file_open"`):strings.Index(p, `call: "security_path_mknod"`)]
	if n := strings.Count(hook, "- matchBinaries:"); n != 2 {
		t.Errorf("file_open has %d selectors, want 2", n)
	}
	if n := strings.Count(hook, "followChildren: true"); n != 2 {
		t.Errorf("%d selectors follow children, want 2", n)
	}
	// the other hooks (exec, writes, network, kill) are untouched by the level
	rest := p[strings.Index(p, `call: "security_path_mknod"`):]
	if strings.Contains(rest, "NotPrefix") || strings.Contains(rest, "NotPostfix") {
		t.Error("the level changed a hook other than file_open")
	}
}

func TestExtendedAndCompleteAskForEveryOpen(t *testing.T) {
	for _, v := range []Verbosity{Extended, Complete} {
		p, err := RenderWith(Options{Binaries: testBins, Verbosity: v, Noise: []NoisePath{{"/usr/lib", "system libraries"}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p, "NotPrefix") || strings.Contains(p, "NotPostfix") || strings.Contains(p, `operator: "Prefix"`) {
			t.Errorf("%s policy excludes something", v)
		}
		if !strings.Contains(p, "# Verbosity: "+string(v)+".") {
			t.Errorf("%s policy lacks its header", v)
		}
		hook := p[strings.Index(p, `call: "security_file_open"`):strings.Index(p, `call: "security_path_mknod"`)]
		if n := strings.Count(hook, "- matchBinaries:"); n != 1 {
			t.Errorf("%s: file_open has %d selectors, want 1", v, n)
		}
	}
}

func TestRenderWithDefaultsToEssentialAndRejectsBadInput(t *testing.T) {
	p, err := RenderWith(Options{Binaries: testBins})
	if err != nil || !strings.Contains(p, "# Verbosity: essential.") {
		t.Errorf("default level: %v\n%.120s", err, p)
	}
	for name, o := range map[string]Options{
		"no binaries":      {Verbosity: Essential},
		"relative binary":  {Binaries: []string{"claude"}, Verbosity: Essential},
		"unknown level":    {Binaries: testBins, Verbosity: "standard"},
		"relative noise":   {Binaries: testBins, Noise: []NoisePath{{"cache", "x"}}},
		"quote in noise":   {Binaries: testBins, Noise: []NoisePath{{"/a\"b", "x"}}},
		"newline in noise": {Binaries: testBins, Noise: []NoisePath{{"/a\nb", "x"}}},
	} {
		if _, err := RenderWith(o); err == nil {
			t.Errorf("%s: succeeded", name)
		}
	}
	// Render, as before, asks for everything
	old, err := Render(testBins)
	if err != nil || strings.Contains(old, "NotPrefix") || !strings.Contains(old, "# Verbosity: complete.") {
		t.Errorf("Render = %v", err)
	}
}

func TestSystemEnvIsUsable(t *testing.T) {
	env := SystemEnv()
	if env.Getenv == nil || env.IsDir == nil || env.ReadFile == nil || env.GoEnv == nil {
		t.Fatal("SystemEnv has a nil function")
	}
	if !env.IsDir(os.TempDir()) || env.IsDir("/definitely/not/here") {
		t.Error("IsDir")
	}
	_ = env.GoEnv() // may be nil when Go is not installed; must not panic
	if _, err := env.ReadFile("/definitely/not/here"); err == nil {
		t.Error("ReadFile of a missing file succeeded")
	}
	// detecting on the real machine gives at least the system directories
	if got := DetectNoise([]string{t.TempDir()}, env); len(got) < len(systemNoise) {
		t.Errorf("DetectNoise = %v", got)
	}
}
