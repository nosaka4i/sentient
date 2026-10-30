// Package version holds the build version shared by sen and sentientd.
package version

// Version is set at build time:
//
//	go build -ldflags "-X github.com/nosaka4i/sentient/internal/version.Version=0.1.0" ./cmd/...
var Version = "0.0.2"
