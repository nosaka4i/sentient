package collector

import "path/filepath"

// SensitivePaths is the core list of locations whose opens are always kept,
// whatever the noise rules say: credentials and keys an agent has no business
// reading unprompted. A platform adds its own on top (the Mac adds the
// keychains). Each entry is a path prefix; a directory ends in "/". The list is
// Go code for now and moves under the user-editable policy later (backlog
// 9e99919). `.env` files are not here: they live in project folders, which no
// noise rule covers.
func SensitivePaths(homes []string) []string {
	var out []string
	for _, h := range homes {
		for _, p := range []string{
			".ssh/",
			".aws/",
			".gnupg/",
			".kube/",
			".config/gh/",
			".docker/config.json",
			".netrc",
			".npmrc",
			".pypirc",
		} {
			out = append(out, filepath.Join(h, p)+trailingSlash(p))
		}
	}
	return out
}

// trailingSlash keeps the "/" that filepath.Join removes, so a directory
// entry still ends in one and cannot match a sibling that starts the same way.
func trailingSlash(p string) string {
	if p != "" && p[len(p)-1] == '/' {
		return "/"
	}
	return ""
}
