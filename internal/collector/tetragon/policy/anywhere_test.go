package policy

import "testing"

func TestMatchAnywhere(t *testing.T) {
	noise := []NoisePath{
		{Path: "/usr/lib", Category: "system libraries"},
		{Path: "/pkg/mod/", Category: "Go module cache", Anywhere: true},
	}
	for _, c := range []struct {
		path     string
		category string
	}{
		{"/usr/lib/libc.so", "system libraries"},
		{"/Users/a/work/pkg/mod/github.com/x/y@v1/y.go", "Go module cache"}, // a GOPATH nobody listed
		{"/home/a/go/pkg/mod/z", "Go module cache"},
	} {
		n, ok := Match(noise, c.path)
		if !ok || n.Category != c.category {
			t.Errorf("Match(%q) = %+v %v, want %s", c.path, n, ok, c.category)
		}
	}
	// a prefix entry still matches only at the start; "Anywhere" does not leak to it
	if _, ok := Match(noise, "/Users/a/usr/lib/x"); ok {
		t.Error("a prefix entry matched in the middle of a path")
	}
	if _, ok := Match(noise, "/Users/a/proj/main.go"); ok {
		t.Error("project file matched")
	}
}

func TestMatchDirectoryItselfAndExact(t *testing.T) {
	noise := []NoisePath{
		{Path: "/dev/", Category: "devices"},
		{Path: "/", Category: "the root directory", Exact: true},
	}
	for _, p := range []string{"/dev", "/dev/null", "/"} {
		if _, ok := Match(noise, p); !ok {
			t.Errorf("%q is not noise", p)
		}
	}
	// "/dev/" is a directory entry, not a prefix of a sibling; "/" is only itself
	for _, p := range []string{"/device-notes.txt", "/Users/a/x", "/etc"} {
		if n, ok := Match(noise, p); ok {
			t.Errorf("%q matched %+v", p, n)
		}
	}
}
