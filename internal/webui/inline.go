// Package webui turns the embedded ui/ directory into one self-contained
// HTML document for WebView2's NavigateToString: local stylesheets and
// scripts are inlined, mock.js is dropped and a restrictive CSP is added.
package webui

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// CSP returns the page policy: no network access, only the scripts that
// carry the per-start nonce (everything the app inlines), inline styles.
func CSP(scriptNonce string) string {
	return "default-src 'none'; script-src 'nonce-" + scriptNonce + "'; style-src 'unsafe-inline'; " +
		"img-src data: blob:; font-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'"
}

type attr struct{ name, value string }

// parseAttrs parses the attributes of a start tag body such as
// `script src="a.js" defer` (the part between "<" and ">").
func parseAttrs(tag string) []attr {
	var out []attr
	i := 0
	// skip tag name
	for i < len(tag) && !isSpace(tag[i]) {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (isSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		start := i
		for i < len(tag) && !isSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' {
			i++
		}
		if start == i {
			break
		}
		a := attr{name: strings.ToLower(tag[start:i])}
		for i < len(tag) && isSpace(tag[i]) {
			i++
		}
		if i < len(tag) && tag[i] == '=' {
			i++
			for i < len(tag) && isSpace(tag[i]) {
				i++
			}
			if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
				q := tag[i]
				i++
				vs := i
				for i < len(tag) && tag[i] != q {
					i++
				}
				a.value = tag[vs:i]
				if i < len(tag) {
					i++
				}
			} else {
				vs := i
				for i < len(tag) && !isSpace(tag[i]) {
					i++
				}
				a.value = tag[vs:i]
			}
		}
		out = append(out, a)
	}
	return out
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func get(attrs []attr, name string) (string, bool) {
	for _, a := range attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

func isLocal(ref string) bool {
	r := strings.ToLower(strings.TrimSpace(ref))
	return r != "" && !strings.Contains(r, "://") && !strings.HasPrefix(r, "//") &&
		!strings.HasPrefix(r, "data:") && !strings.HasPrefix(r, "blob:")
}

func readLocal(fsys fs.FS, dir, ref string) (string, error) {
	p := path.Clean(path.Join(dir, strings.TrimPrefix(strings.TrimSpace(ref), "./")))
	b, err := fs.ReadFile(fsys, p)
	if err != nil {
		return "", fmt.Errorf("inline %s: %w", ref, err)
	}
	return string(b), nil
}

func renderAttrs(attrs []attr, skip ...string) string {
	var b strings.Builder
outer:
	for _, a := range attrs {
		for _, s := range skip {
			if a.name == s {
				continue outer
			}
		}
		b.WriteByte(' ')
		b.WriteString(a.name)
		if a.value != "" {
			b.WriteString(`="`)
			b.WriteString(strings.ReplaceAll(a.value, `"`, "&quot;"))
			b.WriteByte('"')
		}
	}
	return b.String()
}

// NewNonce returns a random 128-bit hex nonce for the binding guard.
func NewNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// Inline builds the single-document UI from dir/index.html in fsys.
//
//   - <link rel="stylesheet" href="local.css"> → <style>…</style>
//   - <script src="local.js" …></script> → inline <script> (other attributes
//     kept); scripts with "defer" (classic) are moved to the end of <body> in
//     order, which matches defer semantics for inline code
//   - mock.js is removed (the real backend is present)
//   - a Content-Security-Policy meta is added unless the page has one; its
//     script-src allows only scripts with the CSP nonce scriptNonce, which
//     every inline script of the document gets (inlined files, inline
//     scripts of the page, the bootstrap below) — no 'unsafe-inline', so an
//     injected script or on…= handler does not run
//   - <script>window.__jiraffeNonce="<nonce>";</script> is placed first in
//     <head>, before every other script; only this document knows the nonce
//     the binding requires (a dropped file or foreign page does not)
//
// nonce (binding) and scriptNonce (CSP) are random lowercase hex values of
// one start and must differ.
func Inline(fsys fs.FS, dir, nonce, scriptNonce string) (string, error) {
	if !isHex(nonce) || !isHex(scriptNonce) {
		return "", fmt.Errorf("nonces must be lowercase hex")
	}
	if nonce == scriptNonce {
		return "", fmt.Errorf("the CSP nonce must differ from the binding nonce")
	}
	nonceAttr := ` nonce="` + scriptNonce + `"`
	b, err := fs.ReadFile(fsys, path.Join(dir, "index.html"))
	if err != nil {
		return "", fmt.Errorf("read index.html: %w", err)
	}
	src := string(b)
	lower := strings.ToLower(src)
	if hasCSPMeta(src) {
		return "", fmt.Errorf("index.html has its own Content-Security-Policy meta; the page policy is set here")
	}
	var out strings.Builder
	var deferred []string
	i := 0
	for i < len(src) {
		lt := strings.IndexByte(src[i:], '<')
		if lt < 0 {
			out.WriteString(src[i:])
			break
		}
		lt += i
		out.WriteString(src[i:lt])
		// HTML comments are copied verbatim.
		if strings.HasPrefix(src[lt:], "<!--") {
			end := strings.Index(src[lt+4:], "-->")
			if end < 0 {
				out.WriteString(src[lt:])
				break
			}
			end += lt + 4 + 3
			out.WriteString(src[lt:end])
			i = end
			continue
		}
		gt := strings.IndexByte(src[lt:], '>')
		if gt < 0 {
			out.WriteString(src[lt:])
			break
		}
		gt += lt
		tag := src[lt+1 : gt]
		tagLower := lower[lt+1 : gt]
		switch {
		case strings.HasPrefix(tagLower, "link") && len(tagLower) > 4 && isSpace(tagLower[4]):
			attrs := parseAttrs(tag)
			rel, _ := get(attrs, "rel")
			href, _ := get(attrs, "href")
			if strings.EqualFold(strings.TrimSpace(rel), "stylesheet") && isLocal(href) {
				css, err := readLocal(fsys, dir, href)
				if err != nil {
					return "", err
				}
				out.WriteString("<style>")
				out.WriteString(strings.ReplaceAll(css, "</style", `<\/style`))
				out.WriteString("</style>")
				i = gt + 1
				continue
			}
		case strings.HasPrefix(tagLower, "script") && (len(tagLower) == 6 || isSpace(tagLower[6])):
			attrs := parseAttrs(tag)
			s, hasSrc := get(attrs, "src")
			if hasSrc && isLocal(s) {
				closeIdx := strings.Index(lower[gt+1:], "</script")
				if closeIdx < 0 {
					return "", fmt.Errorf("unterminated <script src=%q>", s)
				}
				closeEnd := strings.IndexByte(lower[gt+1+closeIdx:], '>')
				if closeEnd < 0 {
					return "", fmt.Errorf("unterminated </script> for %q", s)
				}
				next := gt + 1 + closeIdx + closeEnd + 1
				i = next
				if path.Base(s) == "mock.js" {
					continue
				}
				js, err := readLocal(fsys, dir, s)
				if err != nil {
					return "", err
				}
				typ, _ := get(attrs, "type")
				_, isDefer := get(attrs, "defer")
				el := "<script" + renderAttrs(attrs, "src", "defer", "async", "nonce") + nonceAttr + ">" +
					strings.ReplaceAll(js, "</script", `<\/script`) + "</script>"
				if isDefer && !strings.EqualFold(typ, "module") {
					deferred = append(deferred, el)
				} else {
					out.WriteString(el)
				}
				continue
			}
			if !hasSrc { // an inline script of the page: allowed by the nonce
				out.WriteString("<script" + renderAttrs(attrs, "nonce") + nonceAttr + ">")
				i = gt + 1
				continue
			}
		}
		out.WriteString(src[lt : gt+1])
		i = gt + 1
	}
	html := out.String()
	if len(deferred) > 0 {
		all := strings.Join(deferred, "\n")
		if k := strings.LastIndex(strings.ToLower(html), "</body"); k >= 0 {
			html = html[:k] + all + "\n" + html[k:]
		} else {
			html += all
		}
	}
	// Always our policy, first in <head> (text such as "content-security-policy"
	// in inlined JS or CSS never suppresses it).
	head := `<meta http-equiv="Content-Security-Policy" content="` + CSP(scriptNonce) + `">` +
		`<script` + nonceAttr + `>window.__jiraffeNonce="` + nonce + `";</script>`
	if k := headStart(html); k >= 0 {
		html = html[:k] + head + html[k:]
	} else {
		html = head + html
	}
	return html, nil
}

// hasCSPMeta reports whether a page has a <meta http-equiv=
// "Content-Security-Policy"> tag (before inlining, so only the page's own
// markup counts).
func hasCSPMeta(src string) bool {
	lower := strings.ToLower(src)
	for from := 0; ; {
		k := strings.Index(lower[from:], "<meta")
		if k < 0 {
			return false
		}
		k += from
		end := strings.IndexByte(src[k:], '>')
		if end < 0 {
			return false
		}
		if v, _ := get(parseAttrs(src[k+1:k+end]), "http-equiv"); strings.EqualFold(strings.TrimSpace(v), "content-security-policy") {
			return true
		}
		from = k + end
	}
}

// headStart returns the index just after the <head …> start tag, or -1.
func headStart(html string) int {
	lower := strings.ToLower(html)
	for from := 0; ; {
		k := strings.Index(lower[from:], "<head")
		if k < 0 {
			return -1
		}
		k += from
		after := k + len("<head")
		if after < len(lower) && (lower[after] == '>' || isSpace(lower[after])) {
			if e := strings.IndexByte(html[k:], '>'); e >= 0 {
				return k + e + 1
			}
			return -1
		}
		from = after // e.g. <header>
	}
}
