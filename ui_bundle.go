//go:build uibundle

package main

import (
	_ "embed"
	"io/fs"

	"jiraffe/internal/uibundle"
)

// uiBundle is build/ui.bundle.gz, written by tools/uibundle (build.sh runs
// it before building with -tags uibundle).
//
//go:embed build/ui.bundle.gz
var uiBundle []byte

func loadUI() (fs.FS, error) { return uibundle.Open(uiBundle) }
