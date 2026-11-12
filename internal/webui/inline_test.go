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
	html, err := Inline(fsys, "ui", "00ff", "ab12")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<style>body{color:red}</style>",
		`<script nonce="ab12">const I18N = {a:"<\/script>"};</script>`,
		`<script nonce="ab12">console.log(I18N)</script>`,
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
	if _, err := Inline(fsys, "ui", "00ff", "ab12"); err == nil {
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
	html, err := Inline(fsys, "ui", nonce, "ab12")
	if err != nil {
		t.Fatal(err)
	}
	tag := `<script nonce="ab12">window.__jiraffeNonce="` + nonce + `";</script>`
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
		if _, err := Inline(fsys, "ui", bad, "ab12"); err == nil {
			t.Errorf("nonce %q accepted", bad)
		}
	}
}

// The page policy allows only scripts carrying the per-start CSP nonce:
// every script the document has (inlined files, inline page scripts, the
// bootstrap) carries it; no 'unsafe-inline' for scripts.
func TestScriptNonceCSP(t *testing.T) {
	fsys := fstest.MapFS{
		"ui/index.html": {Data: []byte(`<!doctype html><html><head><link rel="stylesheet" href="a.css">` +
			`<script src="i18n.js"></script><script type="module" nonce="evil">inline()</script></head>` +
			`<body><script src="mock.js"></script><script src="app.js" defer></script></body></html>`)},
		"ui/a.css":   {Data: []byte(`p{}`)},
		"ui/i18n.js": {Data: []byte(`I()`)},
		"ui/app.js":  {Data: []byte(`A()`)},
		"ui/mock.js": {Data: []byte(`M()`)},
	}
	const bind, script = "0011", "aa99"
	html, err := Inline(fsys, "ui", bind, script)
	if err != nil {
		t.Fatal(err)
	}
	csp := CSP(script)
	if !strings.Contains(html, `<meta http-equiv="Content-Security-Policy" content="`+csp+`">`) ||
		!strings.Contains(csp, "script-src 'nonce-aa99';") || !strings.Contains(csp, "style-src 'unsafe-inline'") {
		t.Fatalf("CSP %q in\n%s", csp, html)
	}
	scriptSrc := csp[strings.Index(csp, "script-src"):]
	scriptSrc = scriptSrc[:strings.IndexByte(scriptSrc, ';')]
	if strings.Contains(scriptSrc, "unsafe-inline") || strings.Contains(scriptSrc, "unsafe-eval") {
		t.Errorf("script-src %q", scriptSrc)
	}
	tags := strings.Count(html, "<script")
	if tags != 4 || strings.Count(html, `<script nonce="aa99">`)+strings.Count(html, `<script type="module" nonce="aa99">`) != 4 {
		t.Errorf("%d script tags, not all with the nonce:\n%s", tags, html)
	}
	if strings.Contains(html, `nonce="evil"`) || strings.Contains(html, "M()") {
		t.Errorf("page nonce kept or mock included:\n%s", html)
	}
	if strings.Contains(html, "nonce-"+bind) || strings.Count(html, bind) != 1 {
		t.Error("the binding nonce must not be the CSP nonce")
	}
	if _, err := Inline(fsys, "ui", "0011", "0011"); err == nil {
		t.Error("equal nonces accepted")
	}
	for _, bad := range []string{"", "AB", `x"`} {
		if _, err := Inline(fsys, "ui", bind, bad); err == nil {
			t.Errorf("CSP nonce %q accepted", bad)
		}
	}
}

// Our CSP is always added: the phrase in inlined code does not suppress it;
// a page with its own CSP meta is refused.
func TestCSPAlwaysAdded(t *testing.T) {
	fsys := fstest.MapFS{
		"ui/index.html": {Data: []byte(`<html><head><script src="a.js"></script><link rel="stylesheet" href="a.css"></head><body></body></html>`)},
		"ui/a.js": {Data: []byte(`// we never set a Content-Security-Policy here
var s = "<meta http-equiv=\"Content-Security-Policy\" content=\"script-src *\">";`)},
		"ui/a.css": {Data: []byte(`/* content-security-policy */ p{}`)},
	}
	html, err := Inline(fsys, "ui", "0011", "aa99")
	if err != nil {
		t.Fatal(err)
	}
	meta := `<meta http-equiv="Content-Security-Policy" content="` + CSP("aa99") + `">`
	if !strings.Contains(html, meta) || strings.Index(html, meta) > strings.Index(html, "<script") {
		t.Errorf("CSP missing or not first:\n%s", html)
	}
	for _, page := range []string{
		`<html><head><meta http-equiv="Content-Security-Policy" content="script-src *"></head></html>`,
		`<html><head><META HTTP-EQUIV='content-security-policy' CONTENT="x"></head></html>`,
	} {
		if _, err := Inline(fstest.MapFS{"ui/index.html": {Data: []byte(page)}}, "ui", "0011", "aa99"); err == nil {
			t.Errorf("own CSP accepted: %s", page)
		}
	}
}
