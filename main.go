// Command jiraffe is a Windows tray companion for Jira Server/Data Center:
// automatic daily worklogs, a board of my open issues, status transitions
// and comment alerts. See SPEC.md.
package main

import "os"

// The single-page UI is inlined into one HTML document at startup (see
// internal/webui). loadUI returns it rooted at the ui/ directory: the raw
// files (ui_raw.go, default) or, in release builds (-tags uibundle, see
// build.sh), the compressed bundle (ui_bundle.go).

func main() {
	os.Exit(run(os.Args[1:]))
}
