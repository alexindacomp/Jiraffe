//go:build !windows && !(linux && cgo)

package main

import (
	"fmt"
	"os"
)

// run is a stub: Jiraffe runs on Windows and on Linux built with cgo (GTK, WebKitGTK).
func run([]string) int {
	fmt.Fprintln(os.Stderr, "jiraffe needs Windows (build.sh) or Linux with cgo (build-linux.sh)")
	return 1
}
