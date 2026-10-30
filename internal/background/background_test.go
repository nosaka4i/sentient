package background

import "testing"

func TestKey(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		key  string
		want bool
	}{
		// the agent's own binary is not a background command: a copy of it is only
		// the stub a process is before it runs its program
		{"2.1.305 -c", "", false},
		{"claude --resume f95ecbb7 --model opus", "", false},
		{"2.1.305 -G --hidden", "", false},
		{"node server.js", "", false},
		{"git status --short", "git status", true},
		{"git -c core.askPass= -c protocol.ext.allow=never status --short", "git status", true},
		{"git -C /repo rev-parse --show-toplevel", "git rev-parse", true},
		{"git -c core.fsmonitor= --no-optional-locks config --get remote.origin.url", "git config --get", true},
		{"git config --get-regexp ^remote", "git config --get-regexp", true},
		{"git config user.name x", "", false}, // a write
		{"git remote get-url origin", "git remote get-url", true},
		{"git remote add x y", "", false},
		{"git remote", "", false},
		{"git ls-files --others", "git ls-files", true},
		{"git push origin main", "", false},
		// the GitHub CLI's issue listing, as Claude Code runs it when it starts
		{"gh issue list -R anthropics/claude-code --author @me --state closed --search closed:>2026-09-30 --json number,title --limit 30", "gh issue list", true},
		{"gh issue list", "gh issue list", true},
		{"gh --repo x/y issue list", "gh issue list", true},
		{"gh issue create --title x", "", false}, // writes to GitHub: not housekeeping
		{"gh pr list", "", false},
		{"gh issue", "", false},
		{"gh", "", false},
		{"git", "", false},
		{"ls -la", "", false},
		{"", "", false},
	} {
		key, ok := Key(tc.cmd)
		if key != tc.key || ok != tc.want {
			t.Errorf("Key(%q) = %q, %v; want %q, %v", tc.cmd, key, ok, tc.key, tc.want)
		}
	}
}

func TestIsToolRun(t *testing.T) {
	for argv, want := range map[string]bool{
		`["-G","--ignore-files","--hidden","-I","--exclude-dir=.git","-n","x"]`: true, // the shell's grep
		`["-S","dfs","-regextype","findutils-default",".","-name","x"]`:         true, // the shell's find
		`["-G","--hidden"]`: false, // not the injected flags
		`["-c"]`:            false, // `claude -c`, continuing a conversation
		`[]`:                false,
		``:                  false,
	} {
		if got := IsToolRun(argv); got != want {
			t.Errorf("IsToolRun(%q) = %v, want %v", argv, got, want)
		}
	}
}

func TestEffectiveCommand(t *testing.T) {
	for _, tc := range []struct{ argv, want string }{
		{`["-G","--ignore-files","--hidden","-I","--exclude-dir=.git","--exclude-dir=.svn","-n","a b","f.go"]`, "grep -n a b f.go"},
		{`["-G","--ignore-files","--hidden","-I"]`, "grep"},
		{`["-S","dfs","-regextype","findutils-default",".","-name","x"]`, "find . -name x"},
		{`["status"]`, "claude status"},
		{`bad json`, "claude"},
	} {
		if got := EffectiveCommand("/x/claude", tc.argv); got != tc.want {
			t.Errorf("EffectiveCommand(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
	if p := ToolRunPrefixes(); len(p) != 2 {
		t.Errorf("ToolRunPrefixes = %v", p)
	}
}

func TestCommandLine(t *testing.T) {
	for _, tc := range []struct{ bin, argv, want string }{
		{"/usr/bin/git", `["status","--short"]`, "git status --short"},
		{"/x/2.1.305", `["-c"]`, "2.1.305 -c"},
		{"/bin/ls", ``, "ls"},
		{"/bin/ls", `not json`, "ls"},
	} {
		if got := CommandLine(tc.bin, tc.argv); got != tc.want {
			t.Errorf("CommandLine(%q, %q) = %q, want %q", tc.bin, tc.argv, got, tc.want)
		}
	}
}
