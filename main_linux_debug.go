//go:build linux && cgo && snapshotdebug

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"jiraffe/internal/gtkwin"
)

// Debug builds: JIRAFFE_SNAP="<js>|<js>|…" runs each step 4 s apart after the
// window opens and writes $JIRAFFE_SNAP_DIR/snapN.png after each step.
func init() {
	steps := os.Getenv("JIRAFFE_SNAP")
	dir := os.Getenv("JIRAFFE_SNAP_DIR")
	if steps == "" || dir == "" {
		return
	}
	go func() {
		time.Sleep(6 * time.Second)
		for i, js := range strings.Split(steps, "|") {
			gtkwin.Post(func() {
				if w := debugUI.w; w != nil {
					if js != "" {
						w.Eval(js)
					}
				}
			})
			time.Sleep(4 * time.Second)
			path := fmt.Sprintf("%s/snap%d.png", dir, i)
			gtkwin.Post(func() {
				if w := debugUI.w; w != nil {
					w.Snapshot(path)
				}
			})
			time.Sleep(time.Second)
		}
	}()
}
