package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"jiraffe/internal/uibundle"
	"jiraffe/internal/webui"
)

const (
	testNonce       = "0123456789abcdef0123456789abcdef"
	testScriptNonce = "fedcba9876543210fedcba9876543210"
)

// The release bundle (built from ui/ like build.sh does) gives exactly the
// same page as the raw files.
func TestUIBundleMatchesRaw(t *testing.T) {
	b, err := uibundle.Build(os.DirFS("ui"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ui.bundle.gz")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := uibundle.Open(read)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := loadUI()
	if err != nil {
		t.Fatal(err)
	}
	fromBundle, err := webui.Inline(bundle, ".", testNonce, testScriptNonce)
	if err != nil {
		t.Fatal(err)
	}
	// Every script of the real page carries the CSP nonce: the bootstrap
	// plus one per script of index.html except the mock.
	index, _ := os.ReadFile("ui/index.html")
	scripts := 1 + strings.Count(string(index), "<script") - strings.Count(string(index), `<script src="mock.js"`)
	if got := strings.Count(fromBundle, `<script nonce="`+testScriptNonce+`">`); got != scripts || strings.Contains(fromBundle, "<script>") {
		t.Errorf("%d scripts with the CSP nonce, want %d", got, scripts)
	}
	for name, fsys := range map[string]fs.FS{"ui/ on disk": os.DirFS("ui"), "embedded": embedded} {
		raw, err := webui.Inline(fsys, ".", testNonce, testScriptNonce)
		if err != nil {
			t.Fatal(err)
		}
		if raw != fromBundle {
			t.Errorf("bundle page differs from the %s page (%d vs %d bytes)", name, len(fromBundle), len(raw))
		}
	}
	// Every ui/ file except mock.js is in the bundle, without new code.
	var want, got []string
	_ = fs.WalkDir(os.DirFS("ui"), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !uibundle.Skipped(p) {
			want = append(want, p)
		}
		return nil
	})
	for _, p := range want {
		if _, err := fs.ReadFile(bundle, p); err != nil {
			t.Errorf("bundle lacks %s", p)
		} else {
			got = append(got, p)
		}
	}
	if _, err := fs.ReadFile(bundle, "mock.js"); err == nil {
		t.Error("mock.js is in the bundle")
	}
	sort.Strings(got)
	if len(got) == 0 || !contains(got, "index.html") {
		t.Errorf("bundle files %v", got)
	}
	if len(b) > len(fromBundle)/2 {
		t.Errorf("bundle of %d bytes is barely compressed (page %d bytes)", len(b), len(fromBundle))
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var refRE = regexp.MustCompile(`(?is)<(?:script[^>]*\ssrc|link[^>]*\shref)\s*=\s*["']?([^"' >]+)`)

// Every local script and stylesheet of ui/index.html (except the browser
// mock) is in the embedded UI of this build: a new UI file cannot be
// missed silently; mock.js is never embedded.
func TestUIEmbedsReferencedFiles(t *testing.T) {
	index, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := loadUI()
	if err != nil {
		t.Fatal(err)
	}
	refs := 0
	for _, m := range refRE.FindAllStringSubmatch(string(index), -1) {
		ref := strings.TrimPrefix(m[1], "./")
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "/") || uibundle.Skipped(ref) {
			continue // data:, http(s):, the mock
		}
		refs++
		if _, err := fs.ReadFile(embedded, ref); err != nil {
			t.Errorf("ui/index.html references %s, which is not embedded (add it to ui_raw.go)", ref)
		}
	}
	if refs < 2 {
		t.Errorf("only %d local references found in ui/index.html", refs)
	}
	if _, err := fs.ReadFile(embedded, "mock.js"); err == nil {
		t.Error("mock.js is embedded")
	}
}
