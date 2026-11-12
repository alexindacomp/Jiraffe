package app

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"jiraffe/internal/config"
)

// HTML description contract (SPEC v0.5): the only HTML the app writes to
// Jira. checkHTML rejects anything else; it never rewrites.

// htmlTags are the allowed elements; void ones have no end tag.
var htmlTags = map[string]bool{
	"p": true, "br": true, "strong": true, "em": true, "u": true, "s": true, "code": true, "pre": true,
	"ul": true, "ol": true, "li": true, "a": true, "table": true, "thead": true, "tbody": true, "tr": true,
	"td": true, "th": true, "h3": true, "h4": true, "hr": true, "blockquote": true, "div": true, "span": true,
	"img": true,
}

var voidTags = map[string]bool{"br": true, "hr": true, "img": true}

// panelStyles are the exact JEditor class/style pairs of div elements.
var panelStyles = map[string]string{
	"jePanel_blue":      "background-color:#e0efff; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em",
	"jePanel_yell":      "background-color:#ffffce; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em",
	"jePanelHeader":     "border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center",
	"jePanelHeader_red": "background-color:#f7d6c1; border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center",
	"jePanelContent":    "padding:.5em 2em .5em 2em",
}

// maxHTMLDepth bounds element nesting.
const maxHTMLDepth = 64

// htmlRules are the context of one check.
type htmlRules struct {
	jiraURL string          // image sources must be on this origin…
	known   map[string]bool // …or already be in the issue's description (edit)
}

// dangerousTags make a value HTML for the check even when it does not look
// like a JEditor description (looksLikeHTML), so they are never written.
var dangerousTags = []string{"script", "style", "iframe", "frame", "frameset", "object", "embed", "applet", "svg",
	"math", "form", "input", "button", "select", "textarea", "link", "meta", "base", "details", "summary", "dialog",
	"marquee", "center", "label", "option", "plaintext", "xmp", "title", "noembed", "noscript", "template", "portal",
	"img", "video", "audio", "source", "body", "html", "head"}

// needsHTMLCheck reports whether a description/textarea value for a wiki
// target is checked as HTML (and so rejected unless it is valid contract
// HTML): it looks like HTML, or has a comment, CDATA or a processing
// instruction, or a tag ("<" + ASCII letter, read like a browser) that has
// any attribute, is deny-listed, or never ends. Bare "<T>", "List<String>"
// stay wiki text. Linear in the length of s.
// The whole text is screened (needsHTMLCheckWiki exempts the regions Jira's
// wiki renderer shows literally when the target is known to be wiki).
func needsHTMLCheck(s string) bool { return needsHTMLCheckWiki(s, false) }

// needsHTMLCheckWiki is needsHTMLCheck with the literal wiki regions
// exempt when maskCode (the target is positively known to be wiki).
func needsHTMLCheckWiki(s string, maskCode bool) bool {
	if strings.IndexByte(s, '<') < 0 {
		return false
	}
	if maskCode {
		if s = maskWikiCode(s); strings.IndexByte(s, '<') < 0 {
			return false
		}
	}
	if looksLikeHTML(s) {
		return true
	}
	for i := 0; i < len(s); {
		if s[i] != '<' {
			i++
			continue
		}
		j := i + 1
		if j < len(s) && (s[j] == '!' || s[j] == '?') {
			return true
		}
		if j < len(s) && s[j] == '/' {
			j++
		}
		if j >= len(s) || !(s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			i++
			continue
		}
		end, flagged := scanTag(s, j)
		if flagged {
			return true
		}
		i = end
	}
	return false
}

// Wiki code limits.
const maxMacroOpen = 256 // "{code:lang|title=…}" length

// maskWikiCode returns s with the regions Jira's wiki renderer shows
// literally replaced by spaces: {code…}…{code} and {noformat…}…{noformat}
// (an unclosed one runs to the end of the text, as Jira renders it) and
// {{…}} within one line. Macro names are matched case-insensitively.
// Linear in the length of s.
func maskWikiCode(s string) string {
	if strings.IndexByte(s, '{') < 0 {
		return s
	}
	lb := []byte(s)
	for k, c := range lb {
		if c >= 'A' && c <= 'Z' {
			lb[k] = c + 'a' - 'A'
		}
	}
	low := string(lb)
	// Macros that render raw markup ({html} and friends) make nothing
	// literal: no masking at all.
	for _, m := range []string{"{html", "{iframe", "{script", "{javascript"} {
		if strings.Contains(low, m) {
			return s
		}
	}
	out := []byte(s)
	mask := func(from, to int) {
		for k := from; k < to; k++ {
			out[k] = ' '
		}
	}
	// Cached positions of the next "}}" and the next line break (searched
	// forward only, so the whole scan stays linear).
	closeAt, nlAt := -1, -1
	next := func(cache *int, from int, sep string) int {
		if *cache == -2 || *cache >= from {
			return *cache
		}
		if k := strings.Index(low[from:], sep); k >= 0 {
			*cache = from + k
		} else {
			*cache = -2 // none any more
		}
		return *cache
	}
	for i := 0; i < len(low); {
		if low[i] != '{' {
			i++
			continue
		}
		if strings.HasPrefix(low[i:], "{{") {
			c, nl := next(&closeAt, i+2, "}}"), next(&nlAt, i+2, "\n")
			if c >= 0 && (nl < 0 || c < nl) {
				mask(i, c+2)
				i = c + 2
				continue
			}
			i += 2
			continue
		}
		name := ""
		for _, m := range []string{"code", "noformat"} {
			if r := low[i+1:]; strings.HasPrefix(r, m) && len(r) > len(m) && (r[len(m)] == '}' || r[len(m)] == ':') {
				name = m
			}
		}
		if name == "" {
			i++
			continue
		}
		// The opening macro ends at "}" on the same line, within maxMacroOpen.
		window := low[i:min(len(low), i+maxMacroOpen)]
		if nl := strings.IndexByte(window, '\n'); nl >= 0 {
			window = window[:nl]
		}
		ob := strings.IndexByte(window, '}')
		if ob < 0 {
			i++
			continue
		}
		body := i + ob + 1
		closeTag := "{" + name + "}"
		c := strings.Index(low[body:], closeTag)
		if c < 0 {
			mask(i, len(out)) // unclosed: code to the end
			break
		}
		end := body + c + len(closeTag)
		mask(i, end)
		i = end
	}
	return string(out)
}

// scanTag reads the tag whose name starts at s[i] the way a browser does
// (it ends at the first ">" outside quotes; quoted values may contain ">"
// and "<"; unquoted ones end at whitespace or ">"). flagged: the tag has an
// attribute, a deny-listed name, or no end (browsers drop such a tag, so
// flagging it is safe). end is the index after the tag.
func scanTag(s string, i int) (end int, flagged bool) {
	start := i
	for i < len(s) && !isSpaceByte(s[i]) && s[i] != '/' && s[i] != '>' {
		i++
	}
	name := strings.ToLower(s[start:i])
	for _, t := range dangerousTags {
		if name == t {
			return i, true
		}
	}
	for i < len(s) {
		switch c := s[i]; {
		case c == '>':
			return i + 1, false
		case isSpaceByte(c) || c == '/':
			i++
		default:
			return i, true // an attribute
		}
	}
	return i, true // no end
}

// htmlAttr is a parsed attribute (value entity-decoded).
type htmlAttr struct{ name, value string }

// checkHTML validates s against the HTML description contract; the error
// says what is not allowed (English).
func checkHTML(s string, r htmlRules) error {
	var stack []string
	for i := 0; i < len(s); {
		switch s[i] {
		case '&':
			n, err := entityAt(s[i:])
			if err != nil {
				return err
			}
			i += n
		case '<':
			if i+1 >= len(s) {
				return errors.New(`a lone "<"`)
			}
			switch c := s[i+1]; {
			case c == '!' || c == '?':
				return errors.New("comments, CDATA, doctype and processing instructions")
			case c == '/':
				name, n := elemName(s[i+2:])
				j := i + 2 + n
				for j < len(s) && isSpaceByte(s[j]) {
					j++
				}
				if name == "" || j >= len(s) || s[j] != '>' {
					return errors.New("a malformed end tag")
				}
				if len(stack) == 0 || stack[len(stack)-1] != name {
					return fmt.Errorf("an unexpected </%s>", name)
				}
				stack = stack[:len(stack)-1]
				i = j + 1
			default:
				name, attrs, selfClose, n, err := startTag(s[i:])
				if err != nil {
					return err
				}
				if !htmlTags[name] {
					return fmt.Errorf("the element <%s>", name)
				}
				if err := checkAttrs(name, attrs, r); err != nil {
					return err
				}
				switch {
				case voidTags[name]:
				case selfClose:
					return fmt.Errorf("a self-closing <%s/>", name)
				default:
					if stack = append(stack, name); len(stack) > maxHTMLDepth {
						return fmt.Errorf("nesting deeper than %d", maxHTMLDepth)
					}
				}
				i += n
			}
		default:
			if c := s[i]; c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
				return fmt.Errorf("the control character %#02x", c)
			}
			// C1 controls U+0080–U+009F (UTF-8 C2 80 … C2 9F), as for
			// numeric references.
			if s[i] == 0xC2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9F {
				return fmt.Errorf("the control character U+%04X", rune(s[i+1]))
			}
			i++
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("an unclosed <%s>", stack[len(stack)-1])
	}
	return nil
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// entityAt validates the character reference at the start of s ("&…;"):
// a standard named entity or a numeric one of a valid, non-control
// character. It returns its length.
func entityAt(s string) (int, error) {
	end := strings.IndexByte(s, ';')
	if end < 2 || end > 34 {
		return 0, errors.New(`a "&" that is not a character reference`)
	}
	body := s[1:end]
	if body[0] == '#' {
		num, base := body[1:], 10
		if len(num) > 0 && (num[0] == 'x' || num[0] == 'X') {
			num, base = num[1:], 16
		}
		v, err := strconv.ParseUint(num, base, 32)
		if err != nil || v == 0 || v > 0x10FFFF || v >= 0xD800 && v <= 0xDFFF || v < 0x20 && v != '\t' && v != '\n' && v != '\r' || v >= 0x7F && v < 0xA0 {
			return 0, fmt.Errorf("the character reference &%s;", body)
		}
		return end + 1, nil
	}
	for i := 0; i < len(body); i++ {
		if !(body[i] >= 'a' && body[i] <= 'z' || body[i] >= 'A' && body[i] <= 'Z' || body[i] >= '0' && body[i] <= '9') {
			return 0, errors.New(`a "&" that is not a character reference`)
		}
	}
	// A known name decodes to one or two characters. Legacy prefixes decode
	// only partly ("&ampxyz;" → "&xyz;", "&notit;" → "¬it;"): not entities.
	if ref := "&" + body + ";"; html.UnescapeString(ref) == ref || utf8.RuneCountInString(html.UnescapeString(ref)) > 2 {
		return 0, fmt.Errorf("the unknown entity &%s;", body)
	}
	return end + 1, nil
}

// checkEntities validates every character reference of an attribute value.
func checkEntities(v string) error {
	for i := 0; i < len(v); {
		if v[i] != '&' {
			i++
			continue
		}
		n, err := entityAt(v[i:])
		if err != nil {
			return err
		}
		i += n
	}
	return nil
}

// startTag parses a start tag at the start of s ("<name attr="v" …>"):
// lower-cased names, values quoted with " or ' (">" allowed inside),
// entity-decoded, no duplicates. n is its length.
func startTag(s string) (name string, attrs []htmlAttr, selfClose bool, n int, err error) {
	if len(s) < 2 || s[0] != '<' {
		return "", nil, false, 0, errors.New(`a lone "<"`)
	}
	name, k := elemName(s[1:])
	if name == "" {
		return "", nil, false, 0, errors.New(`a lone "<"`)
	}
	i := 1 + k
	seen := map[string]bool{}
	for {
		sp := i
		for i < len(s) && isSpaceByte(s[i]) {
			i++
		}
		switch {
		case i >= len(s):
			return "", nil, false, 0, fmt.Errorf("an unterminated <%s>", name)
		case s[i] == '>':
			return name, attrs, false, i + 1, nil
		case strings.HasPrefix(s[i:], "/>"):
			return name, attrs, true, i + 2, nil
		case i == sp:
			return "", nil, false, 0, fmt.Errorf("a malformed <%s>", name)
		}
		an, ak := attrName(s[i:])
		if an == "" {
			return "", nil, false, 0, fmt.Errorf("a malformed attribute in <%s>", name)
		}
		i += ak
		for i < len(s) && isSpaceByte(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '=' {
			return "", nil, false, 0, fmt.Errorf("the attribute %s without a value", an)
		}
		i++
		for i < len(s) && isSpaceByte(s[i]) {
			i++
		}
		if i >= len(s) || s[i] != '"' && s[i] != '\'' {
			return "", nil, false, 0, fmt.Errorf("the unquoted attribute %s", an)
		}
		q := s[i]
		end := strings.IndexByte(s[i+1:], q)
		if end < 0 {
			return "", nil, false, 0, fmt.Errorf("an unterminated attribute %s", an)
		}
		raw := s[i+1 : i+1+end]
		i += end + 2
		if err := checkEntities(raw); err != nil {
			return "", nil, false, 0, err
		}
		// A raw "<" is refused; "&lt;" is fine (alt text shows as text; a
		// decoded "<" in href/src fails the URL check).
		if strings.ContainsRune(raw, '<') {
			return "", nil, false, 0, fmt.Errorf(`a "<" in the attribute %s`, an)
		}
		val := html.UnescapeString(raw)
		for k := 0; k < len(val); k++ {
			if val[k] < 0x20 && val[k] != '\t' || val[k] == 0x7f {
				return "", nil, false, 0, fmt.Errorf("a control character in the attribute %s", an)
			}
		}
		if seen[an] {
			return "", nil, false, 0, fmt.Errorf("a repeated attribute %s", an)
		}
		seen[an] = true
		attrs = append(attrs, htmlAttr{an, val})
	}
}

// elemName reads an element name (a letter, then letters and digits;
// lower-cased).
func elemName(s string) (string, int) {
	n := 0
	for n < len(s) && (s[n] >= 'a' && s[n] <= 'z' || s[n] >= 'A' && s[n] <= 'Z' || n > 0 && s[n] >= '0' && s[n] <= '9') {
		n++
	}
	return strings.ToLower(s[:n]), n
}

// attrName reads an attribute name (letters, digits, "-", "_", ":";
// lower-cased).
func attrName(s string) (string, int) {
	n := 0
	for n < len(s) && (isWordByte(s[n]) || s[n] == '-' || s[n] == ':') {
		n++
	}
	if n == 0 || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z') {
		return "", 0
	}
	return strings.ToLower(s[:n]), n
}

// checkAttrs applies the per-element attribute rules.
func checkAttrs(tag string, attrs []htmlAttr, r htmlRules) error {
	get := func(name string) (string, bool) {
		for _, a := range attrs {
			if a.name == name {
				return a.value, true
			}
		}
		return "", false
	}
	allowed := map[string]map[string]bool{
		"p": {"dir": true}, "a": {"href": true}, "td": {"colspan": true, "rowspan": true}, "th": {"colspan": true, "rowspan": true},
		"ol": {"start": true}, "img": {"src": true, "alt": true, "width": true, "height": true}, "div": {"class": true, "style": true},
		"span": {"class": true},
	}
	for _, a := range attrs {
		if !allowed[tag][a.name] {
			return fmt.Errorf("the attribute %s on <%s>", a.name, tag)
		}
	}
	intIn := func(name string, lo, hi int) error {
		if v, ok := get(name); ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < lo || n > hi || strconv.Itoa(n) != v {
				return fmt.Errorf("%s=%q on <%s>", name, v, tag)
			}
		}
		return nil
	}
	switch tag {
	case "p":
		if v, ok := get("dir"); ok && v != "auto" {
			return fmt.Errorf("dir=%q", v)
		}
	case "a":
		if v, ok := get("href"); ok && !httpURL(v) {
			return fmt.Errorf("the link %q", v)
		}
	case "td", "th":
		if err := intIn("colspan", 1, 50); err != nil {
			return err
		}
		return intIn("rowspan", 1, 50)
	case "ol":
		return intIn("start", -1000000, 1000000)
	case "img":
		src, ok := get("src")
		if !ok || !httpURL(src) || !(r.known[strings.TrimSpace(src)] || jiraAttachment(src, r.jiraURL)) {
			return fmt.Errorf("the image source %q (only images already in Jira)", src)
		}
		if err := intIn("width", 1, 100000); err != nil {
			return err
		}
		return intIn("height", 1, 100000)
	case "div":
		class, hasClass := get("class")
		style, hasStyle := get("style")
		if hasClass || hasStyle {
			if want, ok := panelStyles[class]; !ok || !hasStyle || style != want {
				return fmt.Errorf("the panel class/style %q", class)
			}
		}
	case "span":
		if v, ok := get("class"); ok && v != "image-wrap" {
			return fmt.Errorf("class=%q on <span>", v)
		}
	}
	return nil
}

// attachmentPaths are the Jira paths images may come from (below the base
// path of the Jira URL).
var attachmentPaths = []string{"/secure/attachment/", "/secure/thumbnail/", "/rest/api/2/attachment/",
	"/plugins/servlet/jeditor_file_provider", "/secure/useravatar", "/secure/projectavatar"}

// jiraAttachment reports whether src is an attachment-like URL on the Jira
// origin, below its base path.
func jiraAttachment(src, jiraURL string) bool {
	if !config.SameOrigin(src, jiraURL) {
		return false
	}
	u, err1 := url.Parse(src)
	b, err2 := url.Parse(jiraURL)
	if err1 != nil || err2 != nil || u.RawPath != "" || strings.Contains(u.Path, "/..") || strings.Contains(u.Path, "/./") {
		return false
	}
	rel, ok := strings.CutPrefix(u.Path, strings.TrimRight(b.Path, "/"))
	if !ok {
		return false
	}
	for _, p := range attachmentPaths {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// httpURL reports whether v is an absolute http(s) URL with a host, no user
// info and no spaces, control characters or "<".
func httpURL(v string) bool {
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c <= ' ' || c == 0x7f, c == '<':
			return false // a decoded "&lt;" lands here
		}
	}
	u, err := url.Parse(v)
	return err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) &&
		u.Host != "" && u.User == nil && u.Opaque == ""
}

// lenientAttr returns the value of attribute name in the tag text s (after
// the tag name), read the way a browser does: quoted or unquoted values,
// the tag ends at ">" outside quotes; entity-decoded and trimmed; "" when
// absent. Attribute names are case-insensitive. end is the index after the
// tag (len(s) when it never ends), so callers can continue from there.
func lenientAttr(s, name string) (value string, end int) {
	found := false
	for i := 0; i < len(s); {
		for i < len(s) && (isSpaceByte(s[i]) || s[i] == '/') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '>' {
			return value, i + 1
		}
		start := i
		for i < len(s) && !isSpaceByte(s[i]) && s[i] != '/' && s[i] != '>' && (s[i] != '=' || i == start) {
			i++
		}
		an := s[start:i]
		for i < len(s) && isSpaceByte(s[i]) {
			i++
		}
		v := ""
		if i < len(s) && s[i] == '=' {
			i++
			for i < len(s) && isSpaceByte(s[i]) {
				i++
			}
			if i < len(s) && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				e := strings.IndexByte(s[i+1:], q)
				if e < 0 {
					return "", len(s) // unterminated: a browser drops the tag
				}
				v = s[i+1 : i+1+e]
				i += e + 2
			} else {
				vs := i
				for i < len(s) && !isSpaceByte(s[i]) && s[i] != '>' {
					i++
				}
				v = s[vs:i]
			}
		}
		if !found && strings.EqualFold(an, name) {
			found, value = true, strings.TrimSpace(html.UnescapeString(v))
		}
	}
	return value, len(s)
}

// imgSources returns the image sources of an existing (possibly sloppy)
// HTML description: the entity-decoded, trimmed src of every <img> tag,
// read leniently like a browser (quoted or unquoted values; the editor
// trims the src of its image chips).
func imgSources(s string) map[string]bool {
	out := map[string]bool{}
	// ASCII-only lowering keeps the offsets of s (also for invalid UTF-8).
	lb := []byte(s)
	for k, c := range lb {
		if c >= 'A' && c <= 'Z' {
			lb[k] = c + 'a' - 'A'
		}
	}
	lower := string(lb)
	// One pass: each tag is read once and the search goes on after it.
	for i := 0; i < len(lower); {
		k := strings.Index(lower[i:], "<img")
		if k < 0 {
			break
		}
		i += k + 4
		if i < len(s) && !isSpaceByte(s[i]) && s[i] != '/' && s[i] != '>' {
			continue // "<imgx": another element
		}
		v, end := lenientAttr(s[i:], "src")
		if v != "" {
			out[v] = true
		}
		i += end
	}
	return out
}
