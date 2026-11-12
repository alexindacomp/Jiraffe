package app

// QA property tests of the server-side HTML validator (SPEC v0.5 "HTML
// description contract"):
//
//   - completeness: every document a reference serializer of the allowed
//     subset emits (random trees, escaping, attribute quoting/order/space
//     variants, panels, image chips, links, tables) is accepted;
//   - soundness: a valid document with one construct outside the contract
//     inserted anywhere between tags is rejected, and anything checkHTML
//     accepts also passes an independent, regexp-based contract oracle
//     (FuzzCheckHTML; seeds run in every `go test`);
//   - termination: the checks return on any input (the fuzz target and a
//     table of pathological inputs).

import (
	"fmt"
	"html"
	"math/rand"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const qaJira = "https://sd.local"

var qaRules = htmlRules{jiraURL: qaJira}

// --- reference serializer -----------------------------------------------------------

type htmlGen struct {
	r     *rand.Rand
	nodes int
}

func (g *htmlGen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

// escText escapes text like the frontend serializer, sometimes using
// numeric references.
func (g *htmlGen) escText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString(g.pick("&amp;", "&#38;", "&#x26;"))
		case r == '<':
			b.WriteString(g.pick("&lt;", "&#60;"))
		case r == '>':
			b.WriteString(g.pick("&gt;", "&#62;", ">"))
		case r == ' ':
			b.WriteString(g.pick("&nbsp;", "&#160;", " "))
		case r > 0xFFFF && g.r.Intn(2) == 0:
			fmt.Fprintf(&b, "&#x%X;", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escAttr escapes an attribute value for the chosen quote.
func escAttr(v string, q byte) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	if q == '"' {
		return strings.ReplaceAll(v, `"`, "&quot;")
	}
	return strings.ReplaceAll(v, "'", "&#39;")
}

func (g *htmlGen) space() string { return g.pick(" ", " ", "  ", "\n", "\t", "\r\n ") }

// tag renders a start tag with the attributes in random order, quotes and
// spacing (names in lower case, as the serializer emits them).
func (g *htmlGen) tag(name string, attrs ...[2]string) string {
	g.r.Shuffle(len(attrs), func(i, j int) { attrs[i], attrs[j] = attrs[j], attrs[i] })
	var b strings.Builder
	b.WriteString("<" + name)
	for _, a := range attrs {
		q := byte('"')
		if g.r.Intn(4) == 0 {
			q = '\''
		}
		b.WriteString(g.space() + a[0])
		if g.r.Intn(5) == 0 {
			b.WriteString(g.pick(" ", "\n") + "=" + g.pick("", " "))
		} else {
			b.WriteString("=")
		}
		b.WriteString(string(q) + escAttr(a[1], q) + string(q))
	}
	if g.r.Intn(6) == 0 {
		b.WriteString(g.pick(" ", "\n"))
	}
	if name == "br" || name == "hr" || name == "img" {
		b.WriteString(g.pick(">", ">", "/>", " />"))
	} else {
		b.WriteString(">")
	}
	return b.String()
}

func (g *htmlGen) end(name string) string { return "</" + name + g.pick("", "", " ", "\n") + ">" }

var qaWords = []string{"word", "Привіт", "日本語", "a&b", "1 < 2", "x > y", `"quoted"`, "it's", "😀", "tab\there", "line\nbreak",
	" ", "&amp; literal", "<script>", "javascript:alert(1)", "on" + "click=x", "%3Cb%3E", " ", "café"}

func (g *htmlGen) text() string {
	var parts []string
	for i := g.r.Intn(3) + 1; i > 0; i-- {
		parts = append(parts, qaWords[g.r.Intn(len(qaWords))])
	}
	return g.escText(strings.Join(parts, " "))
}

var qaHrefs = []string{"https://example.com/", "http://example.com/a?b=1&c=2#d", "https://sd.local/browse/RBI-1",
	"HTTPS://Example.com/x", "https://example.com/%E2%9C%93?q=a%20b", "https://example.com/a'b\"c"}

var qaImgs = []string{"https://sd.local/secure/attachment/10001/a.png", "https://sd.local/secure/thumbnail/1/b%20c.png",
	"https://sd.local/plugins/servlet/jeditor_file_provider?imgId=abc&fileName=x.png", "https://sd.local/rest/api/2/attachment/content/5"}

func (g *htmlGen) inline(depth int) string {
	g.nodes++
	if depth <= 0 || g.nodes > 400 {
		return g.text()
	}
	switch g.r.Intn(9) {
	case 0, 1, 2:
		return g.text()
	case 3:
		return g.tag("br")
	case 4:
		n := g.pick("strong", "em", "u", "s", "code")
		return g.tag(n) + g.inlines(depth-1) + g.end(n)
	case 5:
		return g.tag("a", [2]string{"href", g.pick(qaHrefs...)}) + g.inlines(depth-1) + g.end("a")
	case 6:
		return g.tag("span") + g.inlines(depth-1) + g.end("span")
	case 7:
		return g.imageChip()
	default:
		return g.tag("a") + g.text() + g.end("a")
	}
}

func (g *htmlGen) imageChip() string {
	attrs := [][2]string{{"src", g.pick(qaImgs...)}}
	if g.r.Intn(2) == 0 {
		attrs = append(attrs, [2]string{"alt", g.pick("", "pic", `a "b" > & 'd'`, "Схема")})
	}
	if g.r.Intn(2) == 0 {
		attrs = append(attrs, [2]string{"width", fmt.Sprint(g.r.Intn(2000) + 1)}, [2]string{"height", fmt.Sprint(g.r.Intn(2000) + 1)})
	}
	return g.tag("span", [2]string{"class", "image-wrap"}) + g.tag("img", attrs...) + g.end("span")
}

func (g *htmlGen) inlines(depth int) string {
	var b strings.Builder
	for i := g.r.Intn(3) + 1; i > 0; i-- {
		b.WriteString(g.inline(depth))
	}
	return b.String()
}

func (g *htmlGen) cell(depth int) string {
	n := g.pick("td", "td", "th")
	var attrs [][2]string
	if g.r.Intn(3) == 0 {
		attrs = append(attrs, [2]string{"colspan", fmt.Sprint(g.r.Intn(50) + 1)})
	}
	if g.r.Intn(3) == 0 {
		attrs = append(attrs, [2]string{"rowspan", fmt.Sprint(g.r.Intn(50) + 1)})
	}
	return g.tag(n, attrs...) + g.inlines(depth-1) + g.end(n)
}

func (g *htmlGen) block(depth int) string {
	g.nodes++
	if depth <= 1 || g.nodes > 400 {
		return g.tag("p", [2]string{"dir", "auto"}) + g.text() + g.end("p")
	}
	switch g.r.Intn(12) {
	case 0, 1:
		if g.r.Intn(2) == 0 {
			return g.tag("p", [2]string{"dir", "auto"}) + g.inlines(depth-1) + g.end("p")
		}
		return g.tag("p") + g.inlines(depth-1) + g.end("p")
	case 2:
		h := g.pick("h3", "h4")
		return g.tag(h) + g.inlines(depth-1) + g.end(h)
	case 3:
		l := g.pick("ul", "ol")
		var attrs [][2]string
		if l == "ol" && g.r.Intn(2) == 0 {
			attrs = append(attrs, [2]string{"start", fmt.Sprint(g.r.Intn(2000) - 1000)})
		}
		s := g.tag(l, attrs...)
		for i := g.r.Intn(3) + 1; i > 0; i-- {
			if g.r.Intn(4) == 0 {
				s += g.tag("li") + g.inlines(depth-1) + g.block(depth-2) + g.end("li")
			} else {
				s += g.tag("li") + g.inlines(depth-1) + g.end("li")
			}
		}
		return s + g.end(l)
	case 4:
		s := g.tag("table")
		if g.r.Intn(2) == 0 {
			s += g.tag("thead") + g.tag("tr") + g.cell(depth-2) + g.cell(depth-2) + g.end("tr") + g.end("thead")
		}
		s += g.tag("tbody")
		for i := g.r.Intn(3) + 1; i > 0; i-- {
			s += g.tag("tr") + g.cell(depth-2) + g.cell(depth-2) + g.end("tr")
		}
		return s + g.end("tbody") + g.end("table")
	case 5:
		return g.tag("pre") + g.tag("code") + g.text() + g.end("code") + g.end("pre")
	case 6:
		return g.tag("blockquote") + g.blocks(depth-1) + g.end("blockquote")
	case 7:
		return g.tag("hr")
	case 8, 9:
		panel, head := "jePanel_blue", "jePanelHeader"
		if g.r.Intn(2) == 0 {
			panel, head = "jePanel_yell", "jePanelHeader_red"
		}
		return g.tag("div", [2]string{"class", panel}, [2]string{"style", panelStyles[panel]}) +
			g.tag("div", [2]string{"class", head}, [2]string{"style", panelStyles[head]}) + g.text() + g.end("div") +
			g.tag("div", [2]string{"class", "jePanelContent"}, [2]string{"style", panelStyles["jePanelContent"]}) + g.blocks(depth-2) + g.end("div") +
			g.end("div")
	case 10:
		return g.tag("div") + g.blocks(depth-1) + g.end("div")
	default:
		return g.tag("p", [2]string{"dir", "auto"}) + g.imageChip() + g.end("p")
	}
}

func (g *htmlGen) blocks(depth int) string {
	var b strings.Builder
	for i := g.r.Intn(3) + 1; i > 0; i-- {
		b.WriteString(g.block(depth))
		if g.r.Intn(4) == 0 {
			b.WriteString(g.pick("\n", "\n\n", " "))
		}
	}
	return b.String()
}

// qaDoc is a random valid document of the contract subset.
func qaDoc(seed int64) string {
	g := &htmlGen{r: rand.New(rand.NewSource(seed))}
	return g.blocks(8)
}

// --- independent contract oracle --------------------------------------------------------

var (
	oracleTag   = regexp.MustCompile(`^<(/?)([A-Za-z][A-Za-z0-9]*)((?:[ \t\n\r\f]+[A-Za-z][A-Za-z0-9_:-]*[ \t\n\r\f]*=[ \t\n\r\f]*(?:"[^"]*"|'[^']*'))*)[ \t\n\r\f]*(/?)>`)
	oracleAttr  = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_:-]*)[ \t\n\r\f]*=[ \t\n\r\f]*(?:"([^"]*)"|'([^']*)')`)
	oracleRef   = regexp.MustCompile(`^&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]{0,31});`)
	oracleAllow = map[string]map[string]bool{
		"p": {"dir": true}, "br": {}, "strong": {}, "em": {}, "u": {}, "s": {}, "code": {}, "pre": {}, "ul": {}, "ol": {"start": true},
		"li": {}, "a": {"href": true}, "table": {}, "thead": {}, "tbody": {}, "tr": {}, "td": {"colspan": true, "rowspan": true},
		"th": {"colspan": true, "rowspan": true}, "h3": {}, "h4": {}, "hr": {}, "blockquote": {}, "div": {"class": true, "style": true},
		"span": {"class": true}, "img": {"src": true, "alt": true, "width": true, "height": true},
	}
	oracleVoid = map[string]bool{"br": true, "hr": true, "img": true}
	oracleInt  = regexp.MustCompile(`^-?[1-9][0-9]*$|^0$`)
)

// oracleRefOK: a numeric reference of an allowed character, or a named one
// that the HTML entity table decodes completely (so "&ampxyz;", decoded by
// the legacy-prefix rule to "&xyz;", is not a standard entity).
func oracleRefOK(ref string) bool {
	if ref[1] == '#' {
		num, base := ref[2:len(ref)-1], 10
		if num[0] == 'x' || num[0] == 'X' {
			num, base = num[1:], 16
		}
		// Any number of leading zeros ("&#000000065;" is "A").
		if num = strings.TrimLeft(num, "0"); len(num) > 8 {
			return false
		}
		if num == "" {
			return false // &#0; / &#x0;
		}
		v, err := strconv.ParseInt(num, base, 64)
		r := rune(v)
		return err == nil && v > 0 && v <= utf8.MaxRune && utf8.ValidRune(r) &&
			!(r < 0x20 && r != '\t' && r != '\n' && r != '\r') && !(r >= 0x7f && r < 0xa0)
	}
	d := html.UnescapeString(ref)
	return d != ref && (!strings.HasSuffix(d, ";") || d == ";")
}

func oracleURL(v string, image bool) bool {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" || u.User != nil || strings.ContainsAny(v, " \t\n\r\x00<") {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	if !image {
		return true
	}
	// Same origin as the Jira URL, an explicit default port included.
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[scheme]
	}
	if scheme != "https" || !strings.EqualFold(u.Hostname(), "sd.local") || port != "443" {
		return false
	}
	// Only real dot segments ("." / "..", also percent-encoded) leave the
	// attachment folder; "a..png" is a file name.
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	for _, p := range attachmentPaths {
		if strings.HasPrefix(u.Path, p) {
			return true
		}
	}
	return false
}

// oracleAccepts is an independent check of the contract (regexp tokenizer).
func oracleAccepts(s string) (bool, string) {
	var stack []string
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '&':
			ref := oracleRef.FindString(s[i:])
			if ref == "" || !oracleRefOK(ref) {
				return false, "reference at " + fmt.Sprint(i)
			}
			i += len(ref)
		case c == '<':
			m := oracleTag.FindStringSubmatch(s[i:])
			if m == nil {
				return false, "not a tag at " + fmt.Sprint(i)
			}
			name := strings.ToLower(m[2])
			allowed, ok := oracleAllow[name]
			if !ok {
				return false, "element " + name
			}
			if m[1] == "/" {
				if m[3] != "" || m[4] != "" || len(stack) == 0 || stack[len(stack)-1] != name {
					return false, "end tag " + name
				}
				stack = stack[:len(stack)-1]
				i += len(m[0])
				continue
			}
			attrs := map[string]string{}
			for _, am := range oracleAttr.FindAllStringSubmatch(m[3], -1) {
				an := strings.ToLower(am[1])
				raw := am[2] + am[3]
				if strings.Contains(raw, "<") {
					return false, "raw < in " + an
				}
				for j := strings.IndexByte(raw, '&'); j >= 0; {
					ref := oracleRef.FindString(raw[j:])
					if ref == "" || !oracleRefOK(ref) {
						return false, "reference in " + an
					}
					k := strings.IndexByte(raw[j+len(ref):], '&')
					if k < 0 {
						break
					}
					j += len(ref) + k
				}
				v := html.UnescapeString(raw)
				if _, dup := attrs[an]; dup || !allowed[an] {
					return false, "attribute " + an + " on " + name
				}
				for _, r := range v {
					if r < 0x20 && r != '\t' || r == 0x7f {
						return false, "control in " + an
					}
				}
				attrs[an] = v
			}
			for an, v := range attrs {
				switch {
				case name == "p" && an == "dir" && v != "auto",
					name == "a" && an == "href" && !oracleURL(v, false),
					name == "img" && an == "src" && !oracleURL(v, true),
					(an == "colspan" || an == "rowspan") && !(oracleInt.MatchString(v) && atoiQA(v) >= 1 && atoiQA(v) <= 50),
					(an == "width" || an == "height") && !(oracleInt.MatchString(v) && atoiQA(v) >= 1),
					an == "start" && !oracleInt.MatchString(v),
					name == "span" && an == "class" && v != "image-wrap":
					return false, fmt.Sprintf("%s=%q on %s", an, v, name)
				}
			}
			if name == "img" && attrs["src"] == "" {
				return false, "img without src"
			}
			if name == "div" && (attrs["class"] != "" || attrs["style"] != "") {
				if want, ok := panelStyles[attrs["class"]]; !ok || want != attrs["style"] {
					return false, "panel " + attrs["class"]
				}
			}
			if name == "div" {
				_, hc := attrs["class"]
				_, hs := attrs["style"]
				if hc != hs {
					return false, "panel class/style pair"
				}
			}
			if !oracleVoid[name] {
				if m[4] == "/" {
					return false, "self-closing " + name
				}
				if stack = append(stack, name); len(stack) > maxHTMLDepth {
					return false, "depth"
				}
			}
			i += len(m[0])
		case c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f,
			c == 0xC2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f: // C1 (U+0080–U+009F)
			return false, "control character"
		default:
			i++
		}
	}
	if len(stack) > 0 {
		return false, "unclosed " + stack[len(stack)-1]
	}
	return true, ""
}

func atoiQA(s string) int {
	n := 0
	neg := strings.HasPrefix(s, "-")
	for _, c := range strings.TrimPrefix(s, "-") {
		if n > 1e8 {
			return 1 << 30
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}

// --- completeness ---------------------------------------------------------------------

func TestV5QASerializerOutputIsAccepted(t *testing.T) {
	for seed := int64(1); seed <= 3000; seed++ {
		doc := qaDoc(seed)
		if ok, why := oracleAccepts(doc); !ok {
			t.Fatalf("seed %d: the reference serializer produced something the oracle rejects (%s) — test bug:\n%s", seed, why, doc)
		}
		if err := checkHTML(doc, qaRules); err != nil {
			t.Fatalf("seed %d: valid document rejected: %v\n%s", seed, err, doc)
		}
		if !needsHTMLCheck(doc) && strings.Contains(doc, "<p") {
			t.Fatalf("seed %d: serializer output not recognised as HTML", seed)
		}
	}
}

// The contract's edge values are accepted.
func TestV5QAContractBoundariesAccepted(t *testing.T) {
	deep := strings.Repeat("<blockquote>", maxHTMLDepth) + "x" + strings.Repeat("</blockquote>", maxHTMLDepth)
	for name, doc := range map[string]string{
		"depth 64":               deep,
		"colspan 1 and 50":       `<table><tbody><tr><td colspan="1" rowspan="50">a</td></tr></tbody></table>`,
		"negative ol start":      `<ol start="-5"><li>x</li></ol>`,
		"image already in issue": `<span class="image-wrap"><img src="https://cdn.example/x.png"></span>`,
		"empty document":         ``,
		"only text":              `plain &amp; simple`,
		"semicolon entity":       `a&semi;b`,
		"supplementary numeric":  `&#x1F600;&#128512;`,
		"jira under a base path": `<img src="https://sd.local/jira/secure/attachment/1/a.png">`,
		"dots in a file name":    `<img src="https://sd.local/secure/thumbnail/1/a..png">`,
		"explicit default port":  `<img src="https://sd.local:443/secure/attachment/1/a.png">`,
		"zero-padded references": `&#000000000065;&#x0000041;`,
		"encoded space in a URL": `<img src="https://sd.local/secure/attachment/1/a%20b.png"><a href="https://example.com/a%20b">x</a>`,
	} {
		r := qaRules
		switch name {
		case "image already in issue":
			r.known = map[string]bool{"https://cdn.example/x.png": true}
		case "jira under a base path":
			r.jiraURL = "https://sd.local/jira"
		default:
			// The oracle accepts them too (it must never reject valid input).
			if ok, why := oracleAccepts(doc); !ok {
				t.Errorf("%s: oracle rejects valid input (%s)", name, why)
			}
		}
		if err := checkHTML(doc, r); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An image chip keeps its original alt text; the serializer escapes it, so
// an alt with "<" arrives as "&lt;" — still within the contract.
func TestV5QAEscapedLessThanInAltAccepted(t *testing.T) {
	doc := `<p dir="auto"><span class="image-wrap"><img src="https://sd.local/secure/attachment/1/a.png" alt="a &lt;b&gt; c"></span></p>`
	if ok, why := oracleAccepts(doc); !ok {
		t.Fatalf("oracle: %s", why)
	}
	if err := checkHTML(doc, qaRules); err != nil {
		t.Errorf("escaped alt text rejected: %v", err)
	}
}

// --- soundness ----------------------------------------------------------------------------

// qaForbidden are constructs outside the contract; each must make any
// valid document invalid wherever it is inserted between tags.
var qaForbidden = []string{
	`<script>alert(1)</script>`, `<style>p{}</style>`, `<iframe src="https://x/"></iframe>`, `<svg></svg>`, `<math></math>`,
	`<object data="https://x/"></object>`, `<form action="https://x/"></form>`, `<input value="x">`, `<textarea>x</textarea>`,
	`<b>x</b>`, `<i>x</i>`, `<h1>x</h1>`, `<h2>x</h2>`, `<h5>x</h5>`, `<font>x</font>`, `<center>x</center>`, `<del>x</del>`,
	`<sub>x</sub>`, `<tfoot></tfoot>`, `<section>x</section>`, `<x-y>x</x-y>`, `<p:x>y</p:x>`,
	`<a href="javascript:alert(1)">x</a>`, `<a href="JaVaScRiPt:alert(1)">x</a>`, `<a href="&#106;avascript:alert(1)">x</a>`,
	`<a href="&#x6A;avascript:alert(1)">x</a>`, `<a href="jav&#x09;ascript:alert(1)">x</a>`, `<a href=" javascript:alert(1)">x</a>`,
	`<a href="vbscript:x">x</a>`, `<a href="data:text/html,x">x</a>`, `<a href="//evil.example/">x</a>`, `<a href="/relative">x</a>`,
	`<a href="https://u:p@example.com/">x</a>`, `<a href="mailto:a@b.c">x</a>`, `<a href="ftp://x/">x</a>`, `<a href="https:x">x</a>`,
	`<a href="https://x/" title="t">x</a>`, `<a href="https://x/" href="https://y/">x</a>`, `<a href="https://x/" target="_blank">x</a>`,
	`<p onclick="x()">a</p>`, `<p ONMOUSEOVER="x()">a</p>`, `<p style="color:red">a</p>`, `<p class="x">a</p>`, `<p id="x">a</p>`,
	`<p dir="rtl">a</p>`, `<p dir="auto" dir="auto">a</p>`, `<p data-x="1">a</p>`, `<p dir=auto>a</p>`, `<p dir>a</p>`,
	`<td colspan="0">a</td>`, `<td colspan="51">a</td>`, `<td colspan="01">a</td>`, `<td colspan="2.0">a</td>`, `<td rowspan="-1">a</td>`,
	`<ol start="1e3"></ol>`, `<ol start="+1"></ol>`, `<ol start="x"></ol>`, `<ol type="a"></ol>`,
	`<img src="javascript:alert(1)">`, `<img src="data:image/png;base64,AAAA">`, `<img src="https://evil.example/secure/attachment/1/a.png">`,
	`<img src="https://sd.local/secure/attachment/1/a.png" onerror="x()">`, `<img src="https://sd.local/browse/RBI-1">`,
	`<img src="https://sd.local/secure/attachment/../../logout">`, `<img src="http://sd.local:8080/secure/attachment/1/a.png">`,
	`<img alt="x">`, `<img src="https://sd.local/secure/attachment/1/a.png" width="1.5">`, `<img src="https://sd.local/secure/attachment/1/a.png" width="0">`,
	`<img src="https://sd.local/secure/attachment/1/a.png" style="x">`, `<img src="https://sd.local/secure/attachment/1/a.png">x</img>`,
	`<div class="jePanel_blue">x</div>`, `<div style="` + panelStyles["jePanel_blue"] + `">x</div>`,
	`<div class="jePanel_blue" style="background-color:red">x</div>`, `<div class="jePanel_red" style="` + panelStyles["jePanel_blue"] + `">x</div>`,
	`<div class="jePanel_blue" style="` + panelStyles["jePanel_blue"] + `;">x</div>`, `<div class="userTemplate">x</div>`,
	`<div class="jePanelContent" style="` + panelStyles["jePanelHeader"] + `">x</div>`, `<span class="other">x</span>`,
	`<span style="color:red">x</span>`, `<span class="image-wrap" style="x">x</span>`, `<strong class="x">x</strong>`,
	`<!-- comment -->`, `<!---->`, `<![CDATA[x]]>`, `<!DOCTYPE html>`, `<?xml version="1.0"?>`, `<?php x ?>`,
	`&unknownentity;`, `&#0;`, `&#1;`, `&#x1B;`, `&#127;`, `&#x80;`, `&#xD800;`, `&#x110000;`, `&#;`, `&#x;`, `&;`, `& b`, `&amp`,
	`&ampxyz;`, `&notit;`,
	"\x00", "\x01", "\x1b", "\x7f", "\u0080", "\u0085", "\u009f",
	`<img src="https://sd.local/secure/attachment/1/a.png" alt="<b>">`, `<span class="image-<x>">x</span>`,
	`<a href="https://example.com/&lt;b&gt;">x</a>`, `<img src="https://sd.local/secure/attachment/1/a&lt;b.png">`,
	`<img src="https://sd.local/secure/attachment/1/a b.png">`, `<img src="https://sd.local/secure/attachment/./1/a.png">`,
	`<img src="https://sd.local/secure/attachment/%2e%2e/1/a.png">`, `<`, `a < b`, `<>`, `< p>x</p>`, `</>`, `</p>`, `</ p>`, `</p x="1">`, `<p>`, `<p/>`, `<span/>`, `<br></br>`,
	`<p>unclosed`, `<strong><em>x</strong></em>`, `<p x="1>`, `<p dir="auto>x</p>`, `<p dir='auto">x</p>`,
	`<a href="https://x/&#10;">x</a>`, strings.Repeat("<div>", maxHTMLDepth+1) + strings.Repeat("</div>", maxHTMLDepth+1),
}

// qaInsertionPoints are the offsets between tags of a document from qaDoc
// (its attribute values and text never contain a raw ">" except as text, so
// the offsets after a tag's ">" that start a "<" are tag boundaries).
func qaInsertionPoints(doc string) []int {
	out := []int{0, len(doc)}
	for i := 1; i < len(doc); i++ {
		if doc[i] == '<' {
			out = append(out, i)
		}
	}
	return out
}

func TestV5QAForbiddenConstructsRejectedAnywhere(t *testing.T) {
	r := rand.New(rand.NewSource(42))
	for _, bad := range qaForbidden {
		if ok, _ := oracleAccepts(bad); ok && bad != "" {
			t.Fatalf("oracle accepts the forbidden %q — test bug", bad)
		}
		if err := checkHTML(bad, qaRules); err == nil {
			t.Errorf("accepted on its own: %q", bad)
			continue
		}
		for n := 0; n < 15; n++ {
			doc := qaDoc(r.Int63())
			pts := qaInsertionPoints(doc)
			at := pts[r.Intn(len(pts))]
			mutated := doc[:at] + bad + doc[at:]
			if err := checkHTML(mutated, qaRules); err == nil {
				t.Errorf("accepted %q inserted at %d of:\n%s", bad, at, doc)
				break
			}
		}
	}
}

// The validator must say no, never rewrite, and stay within the depth cap
// without a stack overflow or quadratic blow-up.
func TestV5QAPathologicalInputsTerminate(t *testing.T) {
	for name, s := range map[string]string{
		"deep nesting":          strings.Repeat("<div>", 100000),
		"many entities":         strings.Repeat("&amp;", 200000),
		"unterminated quote":    `<p dir="` + strings.Repeat("a", 200000),
		"many attributes":       "<p" + strings.Repeat(` dir="auto"`, 50000) + ">",
		"many lone ampersands":  strings.Repeat("&", 100000),
		"semicolons far away":   "&" + strings.Repeat("a", 100000) + ";",
		"img-like text":         strings.Repeat("<img", 50000),
		"long valid paragraphs": strings.Repeat(`<p dir="auto">x</p>`, 20000),
	} {
		err := checkHTML(s, qaRules)
		if name == "long valid paragraphs" || name == "many entities" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		_ = needsHTMLCheck(s)
		_ = imgSources(s)
	}
}

// FuzzCheckHTML: whatever the input, the checks return, and an accepted
// document is accepted by the independent oracle (nothing outside the
// contract gets through). Run longer with:
//
//	go test -run '^$' -fuzz FuzzCheckHTML -fuzztime 2m ./internal/app
func FuzzCheckHTML(f *testing.F) {
	for seed := int64(1); seed <= 40; seed++ {
		f.Add(qaDoc(seed))
	}
	for _, bad := range qaForbidden {
		f.Add(`<p dir="auto">` + bad + `</p>`)
	}
	// Valid input the first oracle got wrong (QR-1).
	f.Add(`<p><img src="https://sd.local/secure/thumbnail/1/a..png"></p>`)
	f.Add(`<p><img src="https://sd.local:443/secure/attachment/1/a.png"></p>`)
	f.Add(`<p>&#000000000065;&#x0000041;</p>`)
	f.Fuzz(func(t *testing.T, s string) {
		_ = needsHTMLCheck(s)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("imgSources panicked on %q: %v", s, r)
				}
			}()
			_ = imgSources(s)
		}()
		if checkHTML(s, qaRules) != nil {
			return
		}
		if ok, why := oracleAccepts(s); !ok {
			t.Errorf("accepted outside the contract (%s): %q", why, s)
		}
	})
}
