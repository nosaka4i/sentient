package api

import "testing"

func TestDefaultSocketByPlatform(t *testing.T) {
	for _, c := range []struct{ goos, home, want string }{
		{"linux", "/home/u", "/run/sentient/sentientd.sock"},
		{"darwin", "/Users/hu", "/Users/hu/Library/Application Support/sentient/sentientd.sock"},
		{"darwin", "", "/run/sentient/sentientd.sock"}, // no home to put it in
	} {
		if got := defaultSocket(c.goos, c.home); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.goos, c.home, got, c.want)
		}
	}
	// a Unix socket path is limited to about 100 characters on macOS
	if got := defaultSocket("darwin", "/Users/a-fairly-long-username"); len(got) > 100 {
		t.Errorf("%q is %d characters", got, len(got))
	}
	if DefaultSocket == "" {
		t.Error("the default socket must not be empty")
	}
}
