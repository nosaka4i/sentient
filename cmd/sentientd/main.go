// Command sentientd is sentient's monitor daemon. In production it runs as
// root under systemd: it runs the Claude Code ingester, owns the SQLite
// store and serves the local gRPC API on a Unix socket. Every path can be
// overridden so it also runs unprivileged for development and tests.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sentientd:", err)
		os.Exit(1)
	}
}
