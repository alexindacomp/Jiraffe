package app

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// jeditorSample is the v0.4 JEditor description as the editor serializer
// re-emits it: info and warning panels, paragraphs, formatting, lists, a
// table, a link, a code block, a rule and an image chip.
const jeditorSample = `<div class="jePanel_blue" style="background-color:#e0efff; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em">` +
	`<div class="jePanelHeader" style="border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center">BRIEF:</div>` +
	`<div class="jePanelContent" style="padding:.5em 2em .5em 2em"><p dir="auto">Load the <strong>data</strong> &amp; check&nbsp;it</p></div></div>` +
	`<div class="jePanel_yell" style="background-color:#ffffce; border:1px dashed #cccccc; margin-bottom:1em; margin-left:1em; margin-right:1em; margin-top:1em">` +
	`<div class="jePanelHeader_red" style="background-color:#f7d6c1; border-bottom-color:#cccccc; border-bottom-style:dashed; border-bottom-width:1px; font-weight:bold; padding:.3em; text-align:center">TASK:</div>` +
	`<div class="jePanelContent" style="padding:.5em 2em .5em 2em"><ol start="2"><li>one<br></li><li><em>two</em> <code>x &lt; y</code></li></ol>` +
	`<ul><li><u>a</u> <s>b</s></li></ul></div></div>` +
	"<h3>Heading</h3><h4>Sub</h4><blockquote><p dir=\"auto\">quote</p></blockquote><pre>line 1\nline 2 &gt; 1</pre><hr>" +
	`<table><thead><tr><th colspan="2">H</th></tr></thead><tbody><tr><td rowspan="1">1</td><td>2</td></tr></tbody></table>` +
	`<p dir="auto"><a href="https://sd.local/browse/RBI-1?x=1&amp;y=2">RBI-1</a> &#8212; &#x2014; ok</p>` +
	`<p dir="auto"><span class="image-wrap"><img src="https://sd.local/plugins/servlet/jeditor_file_provider?imgId=1&amp;fileName=a.png" alt="a &quot;b&quot;" width="320" height="200"></span></p>`

func TestCheckHTML(t *testing.T) {
	rules := htmlRules{jiraURL: "https://sd.local", known: map[string]bool{"https://cdn.example/kept.png": true}}
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"JEditor sample", jeditorSample, true},
		{"plain paragraph", `<p>x</p>`, true},
		{"upper-case tags", `<P DIR="auto">x</P><BR>`, true},
		{"self-closing void", `<p>a<br/>b<hr /></p>`, true},
		{"single-quoted attribute with >", `<a href='https://sd.local/a?b=>'>x</a>`, true},
		{"known image (edit)", `<img src="https://cdn.example/kept.png">`, true},
		{"bare div and span", `<div><span>x</span></div>`, true},
		{"text with >", `<p>a > b</p>`, true},
		{"http link", `<a href="http://example.com/x">x</a>`, true},

		{"script", `<p>x</p><script>alert(1)</script>`, false},
		{"script upper case", `<SCRIPT>alert(1)</SCRIPT>`, false},
		{"onclick", `<p onclick="alert(1)">x</p>`, false},
		{"onerror on img", `<img src="https://sd.local/a.png" onerror="alert(1)">`, false},
		{"javascript link", `<a href="javascript:alert(1)">x</a>`, false},
		{"javascript with leading space", `<a href=" javascript:alert(1)">x</a>`, false},
		{"entity-encoded scheme", `<a href="&#106;avascript:alert(1)">x</a>`, false},
		{"hex entity-encoded scheme", `<a href="&#x6A;avascript:alert(1)">x</a>`, false},
		{"tab inside the scheme", "<a href=\"java\tscript:alert(1)\">x</a>", false},
		{"data URL link", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, false},
		{"data URL image", `<img src="data:image/png;base64,AAAA">`, false},
		{"relative link", `<a href="/browse/RBI-1">x</a>`, false},
		{"user info", `<a href="https://user:pw@sd.local/">x</a>`, false},
		{"foreign image", `<img src="https://evil.example/track.png">`, false},
		{"image without src", `<img alt="x">`, false},
		{"svg", `<svg><circle/></svg>`, false},
		{"math", `<math></math>`, false},
		{"iframe", `<iframe src="https://sd.local"></iframe>`, false},
		{"style element", `<style>p{}</style>`, false},
		{"style attribute", `<p style="color:red">x</p>`, false},
		{"panel style mismatch", `<div class="jePanel_blue" style="background-color:#e0efff">x</div>`, false},
		{"panel class with another style", `<div class="jePanelContent" style="padding:.5em 2em .5em 2em; position:fixed">x</div>`, false},
		{"panel style without class", `<div style="padding:.5em 2em .5em 2em">x</div>`, false},
		{"unknown class", `<div class="userTemplate">x</div>`, false},
		{"span with another class", `<span class="x">x</span>`, false},
		{"span with style", `<span class="image-wrap" style="x">x</span>`, false},
		{"comment", `<p>x</p><!-- hidden -->`, false},
		{"CDATA", `<p><![CDATA[x]]></p>`, false},
		{"doctype", `<!DOCTYPE html><p>x</p>`, false},
		{"processing instruction", `<?xml version="1.0"?><p>x</p>`, false},
		{"unknown entity", `<p>&bogus;</p>`, false},
		{"legacy prefix entity", `<p>&ampxyz;</p>`, false},
		{"legacy prefix entity 2", `<p>&notit;</p>`, false},
		{"two-character entity", `<p>&NotEqualTilde; &semi; &not; &amp;</p>`, true},
		{"bare ampersand", `<p>AT&T</p>`, false},
		{"NUL character reference", `<p>&#0;</p>`, false},
		{"control character reference", `<p>&#7;</p>`, false},
		{"surrogate reference", `<p>&#xD800;</p>`, false},
		{"lone <", `<p>a < b</p>`, false},
		{"unclosed tag", `<p><strong>x</p>`, false},
		{"unclosed at end", `<p>x`, false},
		{"stray end tag", `</p>`, false},
		{"unterminated start tag", `<p dir="auto"`, false},
		{"unterminated attribute", `<p dir="auto>x</p>`, false},
		{"unquoted attribute", `<p dir=auto>x</p>`, false},
		{"attribute without value", `<td nowrap>x</td>`, false},
		{"repeated attribute", `<a href="https://sd.local/" href="javascript:x">x</a>`, false},
		{"dir other than auto", `<p dir="rtl">x</p>`, false},
		{"colspan out of range", `<td colspan="51">x</td>`, false},
		{"colspan not a number", `<td colspan="2x">x</td>`, false},
		{"ol start not a number", `<ol start="a"><li>x</li></ol>`, false},
		{"width not a number", `<img src="https://sd.local/a.png" width="100%">`, false},
		{"self-closing non-void", `<p/>`, false},
		{"h1 not allowed", `<h1>x</h1>`, false},
		{"form", `<form action="https://evil.example"><input></form>`, false},
		{"too deep", strings.Repeat("<blockquote>", 65) + strings.Repeat("</blockquote>", 65), false},
		// A raw "<" in an attribute value is refused; the escaped form of
		// the same text is fine where the value is not a URL.
		{"raw < in alt", `<img src="https://sd.local/secure/attachment/1/a.png" alt="<b>">`, false},
		{"escaped < in alt", `<img src="https://sd.local/secure/attachment/1/a.png" alt="&lt;b&gt;">`, true},
		{"raw < in class", `<span class="image-<x>">x</span>`, false},
		{"escaped < in a link", `<a href="https://example.com/&lt;b&gt;">x</a>`, false},
		// A URL with a space is not a clean URL; its encoded form is.
		{"space in an image URL", `<img src="https://sd.local/secure/attachment/1/a b.png">`, false},
		{"encoded space in an image URL", `<img src="https://sd.local/secure/attachment/1/a%20b.png">`, true},
		{"space in a link", `<a href="https://example.com/a b">x</a>`, false},
		{"encoded space in a link", `<a href="https://example.com/a%20b">x</a>`, true},
	}
	for _, tc := range tests {
		err := checkHTML(tc.in, rules)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	if err := checkHTML(strings.Repeat("<blockquote>", 64)+strings.Repeat("</blockquote>", 64), rules); err != nil {
		t.Errorf("depth 64: %v", err)
	}
	// On create no image is "known".
	if checkHTML(`<img src="https://cdn.example/kept.png">`, htmlRules{jiraURL: "https://sd.local"}) == nil {
		t.Error("foreign image accepted on create")
	}
	if checkHTML(`<img src="https://sd.local:8443/a.png">`, htmlRules{jiraURL: "https://sd.local"}) == nil {
		t.Error("another port is another origin")
	}
}

func TestNeedsHTMLCheck(t *testing.T) {
	for in, want := range map[string]bool{
		"plain text":                       false,
		"h1. Wiki *bold* {code}x{code}":    false,
		"generic <T> and a < b":            false,
		"<p>html</p>":                      true,
		"<script>alert(1)</script>":        true,
		"text <SCRIPT src=x>":              true,
		"<svg/onload=alert(1)>":            true,
		"<!-- x -->":                       true,
		"<iframe src=x>":                   true,
		"<img src=x onerror=alert(1)>":     true,
		"a <scripting> word":               false,
		"</style>":                         true,
		"<?php echo 1; ?>":                 true,
		"<details open ontoggle=alert(1)>": true, // an event handler in any tag
		"<x/onfocus=alert(1)>":             true,
		"<a2 href=javascript:x>":           true,
		"see <online> docs":                false,
		"if a<b and c>d":                   true, // "<b " looks like HTML (the shared detection)
	} {
		if got := needsHTMLCheck(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

func TestImgSources(t *testing.T) {
	got := imgSources(`<p><IMG src="https://a/x.png?a=1&amp;b=2"> <img alt="x" src='https://b/y.png'/> <img src=https://c/z.png> <imgx src="https://d/no.png"> <img</p>`)
	if len(got) != 3 || !got["https://a/x.png?a=1&b=2"] || !got["https://b/y.png"] || !got["https://c/z.png"] {
		t.Errorf("%v", got)
	}
}

// Wiki targets: the screen flags any tag with an attribute, deny-listed
// tags, unterminated tags, comments/CDATA/PI and HTML-looking text (read
// like a browser: quotes may hide ">"); plain wiki stays unchecked.
func TestNeedsHTMLCheckHardened(t *testing.T) {
	cases := map[string]bool{
		// deny-listed names, also without attributes
		"<details>x</details>": true,
		"<MARQUEE>x</marquee>": true,
		"<xmp>":                true,
		"<plaintext>":          true,
		"<summary>":            true,
		"</script>":            true,
		// any attribute
		"<portal src=x>":                  true,
		"{code}<input type=text>{code}":   true, // whole text screened unless the target is known wiki
		"<input type=text> outside code":  true,
		"<x style=\"color:red\">":         true,
		"<x HREF='https://a'>":            true,
		"<x class=a>":                     true,
		"<x id=1>":                        true,
		"<x name=a>":                      true,
		"<x autofocus>":                   true,
		"<x popover>":                     true,
		"<x srcset=a>":                    true,
		"<x background=a>":                true,
		"<x contenteditable>":             true,
		"<x &#111;nclick=1>":              true,
		"x <a1 b> y":                      true,
		"a Map<K, V>":                     true, // "<K, V>" is element "k," with attribute "v" to a browser
		`<x a=1 b='2'>text</x> and "quo"`: true,
		// CR-26: a ">" inside quotes does not end the tag
		`<x title=">" onmouseover="alert(1)">hover</x>`: true,
		`<x title='>' href="javascript:alert(1)">x</x>`: true,
		`<x title="<" onmouseover=alert(1)>`:            true,
		`<x a=">`:                                       true,
		`<x a='"' onclick=1>`:                           true,
		// never ends
		"<x":   true,
		"<x a": true,
		// comments, CDATA, PI
		"<!-- x -->":    true,
		"<![CDATA[x]]>": true,
		"<?php ?>":      true,
		// HTML-looking (then checked against the contract)
		"<br>":            true,
		"if (a<b && c>d)": true, // "<b " looks like HTML
		// wiki text
		"{code}List<String> x = y;{code}":          false,
		"a <T> b, List<String>":                    false,
		"<online> docs and <onion>":                false,
		"<sourcecode> and <x/> and </x>":           false,
		"compare a < b and see data: below, x > y": false,
		"a <- b, x <= y, 1<2":                      false,
	}
	for in, want := range cases {
		if got := needsHTMLCheck(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

func TestCheckHTMLHardened(t *testing.T) {
	rules := htmlRules{jiraURL: "https://sd.local"}
	tests := []struct {
		name, in string
		ok       bool
	}{
		{"< in a raw attribute value", `<a href="https://sd.local/?q=<x>">x</a>`, false},
		{"&lt; in an attribute value (alt shows as text)", `<img src="https://sd.local/secure/attachment/1/a.png" alt="&lt;b&gt;">`, true},
		{"&lt; decoded in a link fails the URL check", `<a href="https://sd.local/&lt;x">x</a>`, false},
		{"&lt; decoded in an image source", `<img src="https://sd.local/secure/attachment/1/&lt;.png">`, false},
		{"&quot; in a link is fine", `<a href="https://sd.local/a&quot;b">x</a>`, true},
		{"control character in the text", "<p>a\x01b</p>", false},
		{"DEL in the text", "<p>a\x7fb</p>", false},
		{"C1 control in the text", "<p>a\u0085b</p>", false},
		{"C1 control U+009F", "<p>a\u009fb</p>", false},
		{"U+00A0 and U+2028 are text", "<p>a\u00a0b\u2028c</p>", true},
		{"tab and line breaks in the text", "<p>a\tb\r\n</p>", true},
		{"control character in an attribute", "<img src=\"https://sd.local/secure/attachment/1/a.png\" alt=\"a\x02\">", false},
		{"attachment", `<img src="https://sd.local/secure/attachment/10200/a.png">`, true},
		{"thumbnail", `<img src="https://sd.local/secure/thumbnail/10200/_thumb_10200.png">`, true},
		{"REST attachment", `<img src="https://sd.local/rest/api/2/attachment/content/10200">`, true},
		{"JEditor file provider", `<img src="https://sd.local/plugins/servlet/jeditor_file_provider?imgId=1&amp;fileName=a.png">`, true},
		{"user avatar", `<img src="https://sd.local/secure/useravatar?size=small&amp;ownerId=bob">`, true},
		{"project avatar", `<img src="https://sd.local/secure/projectavatar?pid=1">`, true},
		{"other Jira path", `<img src="https://sd.local/rest/api/2/myself">`, false},
		{"browse page", `<img src="https://sd.local/browse/RBI-1">`, false},
		{"dot segments", `<img src="https://sd.local/secure/attachment/../../rest/api/2/myself">`, false},
		{"encoded path", `<img src="https://sd.local/secure/attachment/%2e%2e/x">`, false},
	}
	for _, tc := range tests {
		if err := checkHTML(tc.in, rules); (err == nil) != tc.ok {
			t.Errorf("%s: err %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	// A Jira below a context path: only paths below it.
	ctx := htmlRules{jiraURL: "https://sd.local/jira"}
	if checkHTML(`<img src="https://sd.local/jira/secure/attachment/1/a.png">`, ctx) != nil ||
		checkHTML(`<img src="https://sd.local/secure/attachment/1/a.png">`, ctx) == nil {
		t.Error("context path")
	}
	// An image already in the description may stay wherever it is.
	if checkHTML(`<img src="https://sd.local/browse/x.png">`, htmlRules{jiraURL: "https://sd.local", known: map[string]bool{"https://sd.local/browse/x.png": true}}) != nil {
		t.Error("known image")
	}
}

// The reason echoed in the field error is capped and stripped of control
// characters.
func TestHTMLReasonIsCapped(t *testing.T) {
	errs := &fieldErrs{lang: "en"}
	long := `<a href=\"javascript:` + strings.Repeat("x", 300) + `\u0007\">x</a>`
	_, _, ok := mapValue(FieldMetaDTO{ID: "description", Kind: FKDescription, Max: 32000}, []byte(`"`+long+`"`),
		writeCtx{html: htmlRules{jiraURL: "https://sd.local"}}, errs)
	if ok || len(errs.errs) != 1 {
		t.Fatalf("errs %+v", errs.errs)
	}
	msg := errs.errs[0].Message
	prefix := strings.TrimSuffix(T("en", "fe_html", ""), "")
	if n := len([]rune(strings.TrimPrefix(msg, prefix))); n > 81 || strings.ContainsRune(msg, 7) {
		t.Errorf("reason of %d runes: %q", n, msg)
	}
}

// An image of the description with spaces or entities around its src is
// recognised after the editor trimmed it.
func TestKnownImageTrimmed(t *testing.T) {
	known := imgSources(`<p><img src="  https://cdn.example/a.png?x=1&amp;y=2 "><img src=" "></p>`)
	if len(known) != 1 || !known["https://cdn.example/a.png?x=1&y=2"] {
		t.Fatalf("%v", known)
	}
	r := htmlRules{jiraURL: "https://sd.local", known: known}
	if err := checkHTML(`<img src="https://cdn.example/a.png?x=1&amp;y=2">`, r); err != nil {
		t.Errorf("trimmed chip src: %v", err)
	}
	if checkHTML(`<img src=" https://cdn.example/a.png?x=1&amp;y=2">`, r) == nil {
		t.Error("a submitted src with spaces is still not a clean URL")
	}
}

// Property: a tag with a dangerous attribute among benign ones is flagged,
// wherever it sits. The screen flags every tag that has any attribute, so
// the quoting of the values does not change the outcome here; quoting
// matters in checkHTML (TestCheckHTML, the QA property tests), which then
// decides what may be written.
func TestNeedsHTMLCheckProperty(t *testing.T) {
	rnd := rand.New(rand.NewSource(26))
	quote := func(v string) string {
		switch q := rnd.Intn(3); {
		case q == 0 && !strings.ContainsAny(v, " >\"'"):
			return v
		case q == 1 && !strings.Contains(v, "'"):
			return "'" + v + "'"
		case !strings.Contains(v, `"`):
			return `"` + v + `"`
		}
		return "'" + v + "'"
	}
	benign := []string{">", "<", "a > b", "x", `say "hi"`, "it's", "data-ok", "=", "/"}
	danger := [][2]string{{"onmouseover", "alert(1)"}, {"OnClick", "x()"}, {"href", "javascript:alert(1)"},
		{"src", "x"}, {"style", "x:y"}, {"formaction", "x"}, {"xlink:href", "#x"}, {"a", "JavaScript:x"},
		{"b", "&#106;avascript:x"}, {"c", "vbscript:x"}, {"d", "data:text/html,x"}, {"&#111;nload", "x"}}
	tags := []string{"x", "div", "span", "custom-el", "a1", "p", "b"}
	for n := 0; n < 3000; n++ {
		var attrs []string
		for k := rnd.Intn(4); k > 0; k-- {
			attrs = append(attrs, fmt.Sprintf("t%d=%s", k, quote(benign[rnd.Intn(len(benign))])))
		}
		d := danger[rnd.Intn(len(danger))]
		pos := rnd.Intn(len(attrs) + 1)
		attrs = append(attrs[:pos], append([]string{d[0] + "=" + quote(d[1])}, attrs[pos:]...)...)
		// Separators: whitespace, or "/" after a quoted value (after an
		// unquoted one a browser reads "/" as part of the value).
		var b strings.Builder
		for k, at := range attrs {
			if k > 0 {
				prev := attrs[k-1]
				if last := prev[len(prev)-1]; (last == '"' || last == '\'') && rnd.Intn(4) == 0 {
					b.WriteString("/")
				} else {
					b.WriteString([]string{" ", "  ", "\t", "\n"}[rnd.Intn(4)])
				}
			}
			b.WriteString(at)
		}
		in := "text <" + tags[rnd.Intn(len(tags))] + " " + b.String() + ">x</x> more"
		if !needsHTMLCheck(in) {
			t.Fatalf("not flagged: %q", in)
		}
	}
}

// Invalid UTF-8 never panics the description scans (QA fuzz finding).
func TestScansSurviveInvalidUTF8(t *testing.T) {
	for _, in := range []string{"\xc70\x9b<img", "\xff<IMG src=\"https://a/x\">", "<img\xc7", "\xc3<", "<"} {
		_ = imgSources(in)
		_ = needsHTMLCheck(in)
		_ = checkHTML(in, htmlRules{jiraURL: "https://sd.local"})
	}
	if got := imgSources("\xc7\xc7<IMG src=\"https://a/x.png\">"); !got["https://a/x.png"] {
		t.Errorf("%v", got)
	}
}

// Code and monospace regions of wiki text are shown literally by Jira and
// not screened; the rest of the text is.
func TestNeedsHTMLCheckSkipsWikiCode(t *testing.T) {
	cases := map[string]bool{
		"{code:java}Map<String, Integer> m;{code}":                    false,
		"{code:java|title=Ex}\nMap<String, Integer> m;\n{code} after": false,
		"use {{Map<String, Integer>}} here":                           false,
		"{noformat}<x a=1>{noformat}":                                 false,
		"{NoFormat:nopanel=true}<x a=1>{noformat}":                    false,
		"{code}<img src=x onerror=alert(1)>{code}":                    false,
		"{code}<script>alert(1)</script>{code}":                       false,
		"{code:sql}select 1 where a<b{code} and the rest":             false,
		"text {code}unclosed <x a=1> runs to the end":                 false,
		"Map<String, Integer> outside":                                true,
		"{code}ok{code} then <x a=1>":                                 true,
		"{{ok}} then Map<K, V>":                                       true,
		"{{not closed on this line\n<x a=1>}}":                        true,
		"{codex}<x a=1>{codex}":                                       true, // not the code macro
		"{code:java<x a=1>":                                           true, // an opening macro must end with }
		"{code:java\n}<x a=1>{code}":                                  true, // … on the same line
		"{code:" + strings.Repeat("x", 300) + "}<x a=1>{code}":        true, // … within 256 characters
		"{{<x a=1>}}{code}<y b=2>{code}<z c=3>":                       true,
	}
	for in, want := range cases {
		if got := needsHTMLCheckWiki(in, true); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

// linearInput builds an adversarial input of n units; it must reach the
// function under test (contain "<" and, for masking, "{"; not be flagged
// early unless noted).
type linearInput struct {
	name string
	gen  func(n int) string
}

// The wiki-code masking is linear: every input contains "<" and "{" (so
// needsHTMLCheckWiki reaches maskWikiCode) and is built for n and 4n units.
// A missing forward cache of "}}" / line-break positions, or a missing
// 256-character window for "{code:…}", makes these quadratic.
func TestMaskWikiCodeIsLinear(t *testing.T) {
	for _, in := range []linearInput{
		{"{{ closed only after the line", func(n int) string { return strings.Repeat("{{", n) + "\n}}<" }},
		{"{{ with the closer far away", func(n int) string { return strings.Repeat("{{", n) + "}}\n<" }},
		{"{{ lines", func(n int) string { return strings.Repeat("{{x\n", n) + "}}<" }},
		{"}} far away", func(n int) string { return strings.Repeat("{{", n) + "\n" + strings.Repeat("}}", n) + "<" }},
		{"{code: without }", func(n int) string { return strings.Repeat("{code:", n) + "<" }},
		{"{noformat: without }", func(n int) string { return strings.Repeat("{noformat:", n) + "<" }},
		{"{code: with } after the line", func(n int) string { return strings.Repeat("{code:", n) + "\n}<" }},
		{"{code} unclosed tags", func(n int) string { return "{code}" + strings.Repeat("<x a ", n) }},
		{"closed code + bare tags", func(n int) string { return strings.Repeat("{code}<x a=1>{code}<T> ", n) }},
	} {
		if s := in.gen(10); !strings.Contains(s, "<") || !strings.Contains(s, "{") {
			t.Fatalf("%s does not reach maskWikiCode", in.name)
		}
		// 16000 units: large enough that a quadratic scan stands out even
		// though each search is a fast SIMD byte scan.
		assertLinear(t, in.name, in.gen, 16000, func(s string) { needsHTMLCheckWiki(s, true) })
	}
}

// The screen is linear. The inputs are never flagged (except where noted),
// so the whole text is scanned, not only up to the first tag.
func TestNeedsHTMLCheckIsLinear(t *testing.T) {
	for _, in := range []struct {
		linearInput
		flagged bool
	}{
		{linearInput{"bare tags", func(n int) string { return strings.Repeat("<T> ", n) }}, false},
		{linearInput{"bare end tags", func(n int) string { return strings.Repeat("</T> ", n) }}, false},
		{linearInput{"generics", func(n int) string { return strings.Repeat("List<String> x ", n) }}, false},
		{linearInput{"lone <", func(n int) string { return strings.Repeat("< ", n) }}, false},
		{linearInput{"less-than digits", func(n int) string { return strings.Repeat("a<3 ", n) }}, false},
		{linearInput{"marker names without >", func(n int) string { return strings.Repeat("<p ", n) }}, true}, // looksLikeHTML scans it all
		{linearInput{"marker tags, > only at the end", func(n int) string { return strings.Repeat("<pre", n) + ">" }}, true},
	} {
		if got := needsHTMLCheck(in.gen(50)); got != in.flagged {
			t.Fatalf("%s: flagged %v, want %v", in.name, got, in.flagged)
		}
		assertLinear(t, in.name, in.gen, 4000, func(s string) {
			needsHTMLCheck(s)
			needsHTMLCheckWiki(s, true)
			looksLikeHTML(s)
		})
	}
}

// imgSources reads each tag once: linear also for many unterminated or
// unquoted image tags.
func TestImgSourcesIsLinear(t *testing.T) {
	for _, in := range []linearInput{
		{"<img ", func(n int) string { return strings.Repeat("<img ", n) }},
		{"<img a=\"", func(n int) string { return strings.Repeat(`<img a="`, n) }},
		{"<img a=b ", func(n int) string { return strings.Repeat("<img a=b ", n) }},
	} {
		assertLinear(t, in.name, in.gen, 4000, func(s string) { imgSources(s) })
	}
}

// assertLinear checks that check(gen(n)) takes well under 100 ms and that
// gen(4n) takes far less than the 16× a quadratic scan would (best of 5
// runs each; robust to slow machines and -race).
func assertLinear(t *testing.T, name string, gen func(int) string, n int, check func(string)) {
	t.Helper()
	timeOf := func(s string) time.Duration {
		best := time.Duration(1 << 62)
		for k := 0; k < 5; k++ {
			start := time.Now()
			check(s)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	d1 := timeOf(gen(n))
	d4 := timeOf(gen(4 * n))
	if d1 > 100*time.Millisecond {
		t.Errorf("%s: %v for %d units", name, d1, n)
	}
	if d4 > 10*d1+2*time.Millisecond {
		t.Errorf("%s: %v for 4n units vs %v for n (not linear)", name, d4, d1)
	}
}

// Property (known wiki): a flagged tag outside any code region is flagged
// wherever closed {code}, {noformat} and {{…}} regions (which may contain
// tags of their own) sit around it.
func TestNeedsHTMLCheckWikiCodeProperty(t *testing.T) {
	rnd := rand.New(rand.NewSource(77))
	inside := []string{"<x a=1>", "Map<K, V>", "<script>x</script>", "a < b", "plain", "<img src=x onerror=y>"}
	region := func() string {
		c := inside[rnd.Intn(len(inside))]
		switch rnd.Intn(4) {
		case 0:
			return "{code}" + c + "{code}"
		case 1:
			return "{code:java|title=T}\n" + c + "\n{code}"
		case 2:
			return "{noformat}" + c + "{noformat}"
		}
		return "{{" + c + "}}"
	}
	plain := []string{"text ", "line\n", "h1. Title\n", "* item\n", "List<String> ", "<T> "}
	flagged := []string{"<x a=1>", "<img src=x>", "<script>", "<!-- c -->", `<a href="javascript:x">`, "<p onclick=x>"}
	for n := 0; n < 3000; n++ {
		var parts []string
		for k := rnd.Intn(6); k > 0; k-- {
			if rnd.Intn(2) == 0 {
				parts = append(parts, region())
			} else {
				parts = append(parts, plain[rnd.Intn(len(plain))])
			}
		}
		pos := rnd.Intn(len(parts) + 1)
		tag := flagged[rnd.Intn(len(flagged))]
		parts = append(parts[:pos], append([]string{" " + tag + " "}, parts[pos:]...)...)
		in := strings.Join(parts, "")
		if !needsHTMLCheckWiki(in, true) {
			t.Fatalf("not flagged: %q", in)
		}
	}
}

// Masking needs a positively known wiki target; macros that render raw
// markup disable it.
func TestWikiCodeMaskingNeedsKnownWiki(t *testing.T) {
	code := "{code:java}Map<String, Integer> m;{code}"
	if needsHTMLCheckWiki(code, true) || !needsHTMLCheck(code) {
		t.Errorf("%q: known wiki must exempt it, a guess must not", code)
	}
	for _, in := range []string{
		"{html}{code}<script>alert(1)</script>{code}{html}",
		"{HTML}x{html} {code}<x a=1>{code}",
		"{iframe:src=x} {code}<x a=1>{code}",
		"{script}{code}<x a=1>{code}",
		"{javascript} {{<x a=1>}}",
	} {
		if !needsHTMLCheckWiki(in, true) {
			t.Errorf("%q not flagged", in)
		}
	}
}
