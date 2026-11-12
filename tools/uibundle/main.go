// Command uibundle writes the compressed UI bundle of release builds:
//
//	go run ./tools/uibundle -src ui -out build/ui.bundle.gz
//
// Every file of src except mock.js and hidden files is included, so new UI
// files need no changes here.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"jiraffe/internal/uibundle"
)

func main() {
	src := flag.String("src", "ui", "UI source directory")
	out := flag.String("out", "build/ui.bundle.gz", "bundle to write")
	flag.Parse()
	b, err := uibundle.Build(os.DirFS(*src))
	if err == nil {
		err = os.MkdirAll(filepath.Dir(*out), 0o755)
	}
	if err == nil {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uibundle:", err)
		os.Exit(1)
	}
	fmt.Printf("uibundle: %s, %d bytes\n", *out, len(b))
}
