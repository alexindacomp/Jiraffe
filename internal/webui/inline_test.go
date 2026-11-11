package webui

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestInline(t *testing.T) {
	fsys := fstest.MapFS{
		"ui/index.html": {Data: []byte(`<!doctype html>
<html><head><meta charset="utf-8">
<link rel="stylesheet" href="app.css">
<link rel="icon" href="data:,">
<!-- <script src="commented.js"></script> -->
<script src="i18n.js" defer></script>
<script src="./mock.js"></script>
<script src="app.js" defer></script>
</head><body><div id="app"></div></body></html>`)},
		"ui/app.css": {Data: []byte(`body{color:red}`)},
		"ui/i18n.js": {Data: []byte(`const I18N = {a:"</script>"};`)},
		"ui/app.js":  {Data: []byte(`console.log(I18N)`)},
		"ui/mock.js": {Data: []byte(`MOCK`)},
	}
	html, err := Inline(fsys, "ui", "00ff")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<style>body{color:red}</style>",
		`<script>const I18N = {a:"<\/script>"};</script>`,
		`<script>console.log(I18N)</script>`,
		`<!-- <script src="commented.js"></script> -->`,
		`<link rel="icon" href="data:,">`,
		"Content-Security-Policy",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in\n%s", want, html)
		}
	}
	if strings.Contains(html, "MOCK") || strings.Contains(html, "mock.js") {
		t.Error("mock.js must not be included")
	}
	// deferred scripts go to the end of body, in order
	i18n, app, div := strings.Index(html, "const I18N"), strings.Index(html, "console.log"), strings.Index(html, `<div id="app">`)
	if !(div < i18n && i18n < app && app < strings.Index(html, "</body>")) {
		t.Errorf("deferred scripts not moved to the end of body in order:\n%s", html)
	}
}

func TestInlineMissingFile(t *testing.T) {
	fsys := fstest.MapFS{"ui/index.html": {Data: []byte(`<script src="nope.js"></script>`)}}
	if _, err := Inline(fsys, "ui", "00ff"); err == nil {
		t.Fatal("expected error for missing script")
	}
}

func TestNonceIsFirstScriptInHead(t *testing.T) {
	fsys := fstest.MapFS{
		"ui/index.html": {Data: []byte(`<!doctype html><html><HEAD lang="x"><title>t</title><script src="a.js"></script></head><body><header>h</header></body></html>`)},
		"ui/a.js":       {Data: []byte(`A()`)},
	}
	nonce, err := NewNonce()
	if err != nil || len(nonce) != 32 || !isHex(nonce) {
		t.Fatalf("nonce %q %v", nonce, err)
	}
	html, err := Inline(fsys, "ui", nonce)
	if err != nil {
		t.Fatal(err)
	}
	tag := `<script>window.__jiraffeNonce="` + nonce + `";</script>`
	i := strings.Index(html, tag)
	if i < 0 || i > strings.Index(html, "<title>") || i > strings.Index(html, "A()") || i < strings.Index(html, `<HEAD lang="x">`) {
		t.Fatalf("nonce script not first in head:\n%s", html)
	}
	if strings.Index(html, "Content-Security-Policy") > i {
		t.Error("CSP meta should precede the scripts")
	}
	if strings.Count(html, nonce) != 1 {
		t.Error("nonce must appear exactly once")
	}
	if other, _ := NewNonce(); other == nonce {
		t.Error("nonces must differ")
	}
	for _, bad := range []string{"", `x"</script>`, "ABCD"} {
		if _, err := Inline(fsys, "ui", bad); err == nil {
			t.Errorf("nonce %q accepted", bad)
		}
	}
}
