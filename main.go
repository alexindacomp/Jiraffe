// Command jiraffe is a Windows tray companion for Jira Server/Data Center:
// automatic daily worklogs, a board of my open issues, status transitions
// and comment alerts. See SPEC.md.
package main

import (
	"embed"
	"os"
)

// uiFS holds the single-page UI; it is inlined into one HTML document at
// startup (see internal/webui).
//
//go:embed ui
var uiFS embed.FS

func main() {
	os.Exit(run(os.Args[1:]))
}
