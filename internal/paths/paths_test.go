package paths

import "testing"

func TestCanonical(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"/private/tmp/x", "/private/tmp/x"},
		{"/tmp/x", "/private/tmp/x"},
		{"/tmp", "/private/tmp"},
		{"/var/folders/ab/T/f", "/private/var/folders/ab/T/f"},
		{"/etc/hosts", "/private/etc/hosts"},
		{"/tmpfoo/x", "/tmpfoo/x"}, // a sibling that only starts with the same letters
		{"/System/Volumes/Data/Users/a/b", "/Users/a/b"},
		{"/System/Volumes/Data/tmp/x", "/private/tmp/x"},
		{"/System/Volumes/Data", "/"},
		{"/Users/a//b/../c", "/Users/a/c"},
		{"relative/x", "relative/x"},
	} {
		if got := Canonical(c.in); got != c.want {
			t.Errorf("Canonical(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSame(t *testing.T) {
	if !Same("/tmp/outside-test.txt", "/private/tmp/outside-test.txt") {
		t.Error("/tmp/x and /private/tmp/x are the same file")
	}
	if Same("/tmp/a", "/tmp/b") {
		t.Error("different files compared equal")
	}
	if Same("", "") {
		t.Error("two empty paths are not a match")
	}
}

func TestUnder(t *testing.T) {
	for _, c := range []struct {
		dir, p string
		want   bool
	}{
		{"/Users/a/proj", "/Users/a/proj/src/x.go", true},
		{"/Users/a/proj", "/Users/a/proj", true},
		{"/Users/a/proj", "/Users/a/project/x", false},
		{"/tmp", "/private/tmp/x", true},
		{"/private/tmp", "/tmp/x", true},
		{"/System/Volumes/Data/Users/a", "/Users/a/f", true},
		{"/", "/anything", true},
		{"", "/x", false},
		{"/x", "", false},
	} {
		if got := Under(c.dir, c.p); got != c.want {
			t.Errorf("Under(%q, %q) = %v, want %v", c.dir, c.p, got, c.want)
		}
	}
}
