// Command sen is sentient's CLI: see what your coding agent actually did.
// It runs as the user and gets all its data from sentientd over the local
// API.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sen:", err)
		os.Exit(1)
	}
}
