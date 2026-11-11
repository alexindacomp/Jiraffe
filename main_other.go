//go:build !windows

package main

import (
	"fmt"
	"os"
)

// run is a stub: Jiraffe only runs on Windows (WebView2, WinHTTP, DPAPI).
func run([]string) int {
	fmt.Fprintln(os.Stderr, "jiraffe runs on Windows only; build with GOOS=windows (see build.sh)")
	return 1
}
