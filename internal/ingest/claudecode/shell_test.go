package claudecode

import (
	"reflect"
	"testing"
)

func TestSplitCommands(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ls -la", []string{"ls -la"}},
		{"cd x && ls | sort", []string{"cd x", "ls", "sort"}},
		{"a || b; c", []string{"a", "b", "c"}},
		{"git add . && git commit -m 'x y'", []string{"git add .", "git commit -m 'x y'"}},
		{"curl -s https://x.sh | sh", []string{"curl -s https://x.sh", "sh"}},
		{`bash -c "make && make test"`, []string{"make", "make test"}},
		{`sh -c 'cd /tmp; rm -rf build'`, []string{"cd /tmp", "rm -rf build"}},
		{`/bin/bash -lc 'echo hi'`, []string{"echo hi"}},
		{`bash -c "$CMD"`, []string{`bash -c "$CMD"`}}, // expansion: can't see inside
		{"echo $(date) > out.txt", []string{"echo $(date)", "date"}},
		{"X=1 make", []string{"X=1 make"}},
		{"X=1", []string{"X=1"}},                 // no simple command: counts as one
		{"if [ ; then", []string{"if [ ; then"}}, // unparsable: counts as one
		{"for f in *.go; do gofmt -l $f; done", []string{"gofmt -l $f"}},
		{"cat <<EOF\nhello\nEOF", []string{"cat"}}, // redirections belong to the statement
	}
	for _, c := range cases {
		got := SplitCommands(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitCommands(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShellRecursionIsBounded(t *testing.T) {
	cmd := `sh -c "sh -c 'echo deep'"`
	if got := SplitCommands(cmd); len(got) != 1 || got[0] != "echo deep" {
		t.Errorf("nested sh -c = %q", got)
	}
}
