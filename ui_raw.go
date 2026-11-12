//go:build !uibundle

package main

import (
	"embed"
	"io/fs"
)

// uiRaw holds the UI files of development builds. The list is explicit to
// leave the browser mock (ui/mock.js) out; ui_test.go fails when a file
// referenced by ui/index.html is missing here. Release builds embed the
// whole ui/ directory as a bundle instead (ui_bundle.go).
//
//go:embed ui/index.html ui/app.css ui/i18n.js ui/app.js ui/editor.js
var uiRaw embed.FS

func loadUI() (fs.FS, error) { return fs.Sub(uiRaw, "ui") }
