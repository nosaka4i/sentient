// Package paths makes two spellings of the same file compare equal. The
// kernel reports the path a file really has; the agent's log says the path the
// agent typed. On the Mac they differ: /tmp, /var and /etc are links into
// /private, and the user's data volume is also reachable through the
// /System/Volumes/Data firmlink prefix. Kernel paths are stored verbatim, as
// evidence; this package is only used when two paths are compared (merge,
// attribution, the outside-the-repo rule). It is pure string work, so it
// builds and runs everywhere. See docs/mac-collector-design.md, "Paths".
package paths

import (
	"path"
	"strings"
)

// dataVolume is the firmlink prefix of the Mac's data volume: a path under it
// is the same file as the path without it.
const dataVolume = "/System/Volumes/Data"

// privateLinks are the top-level directories that are links into /private.
var privateLinks = []string{"/tmp", "/var", "/etc"}

// Canonical returns p in one form: cleaned, without the data volume prefix,
// and with /tmp, /var and /etc spelled /private/tmp, /private/var and
// /private/etc. A path that is empty or relative is returned cleaned, with no
// other change (there is nothing to resolve it against).
func Canonical(p string) string {
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if p == dataVolume {
		return "/"
	}
	if rest, ok := strings.CutPrefix(p, dataVolume+"/"); ok {
		p = "/" + rest
	}
	for _, link := range privateLinks {
		if p == link || strings.HasPrefix(p, link+"/") {
			return "/private" + p
		}
	}
	return p
}

// Spellings returns every way the same file can be written: the path cleaned,
// its canonical form, and the short form (/tmp/x for /private/tmp/x). Matching
// a stored path against all of them needs no platform check: on Linux the
// spellings that do not exist there simply match nothing.
func Spellings(p string) []string {
	if p == "" {
		return nil
	}
	out := []string{path.Clean(p)}
	add := func(s string) {
		for _, have := range out {
			if have == s {
				return
			}
		}
		out = append(out, s)
	}
	c := Canonical(p)
	add(c)
	for _, link := range privateLinks {
		if c == "/private"+link || strings.HasPrefix(c, "/private"+link+"/") {
			add(strings.TrimPrefix(c, "/private"))
		}
	}
	return out
}

// Same reports whether a and b are the same file path once canonicalized.
func Same(a, b string) bool {
	return a != "" && b != "" && Canonical(a) == Canonical(b)
}

// Under reports whether p is dir itself or inside it, comparing canonical
// forms, so /tmp/x is under /private/tmp and /private/tmp/x is under /tmp.
func Under(dir, p string) bool {
	if dir == "" || p == "" {
		return false
	}
	d, c := Canonical(dir), Canonical(p)
	if d == "/" {
		return strings.HasPrefix(c, "/")
	}
	return c == d || strings.HasPrefix(c, d+"/")
}
