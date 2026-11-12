package app

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"jiraffe/internal/jira"
)

// --- plain-text snippets of HTML bodies (SPEC v0.4 §1) ------------------------

func TestPlainTextHTML(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"JEditor panel", `<div class="userTemplate"><div class="jePanel_blue" style="background:#eef"><div class="jePanelHeader">BRIEF:</div>` +
			`<div class="jePanelContent"><p dir="auto">Load&nbsp;the <b>data</b> &amp; check</p></div></div></div>`,
			"BRIEF: Load the data & check"},
		{"br and block tags become spaces", "line1<br>line2<BR/>line3<p>para</p><li>item</li>", "line1 line2 line3 para item"},
		{"inline tags join words", "a<b>b</b><i>c</i><span style='x'>d</span><code>e</code>", "abcde"},
		{"entities decoded after stripping", "<p>&lt;b&gt; is &quot;bold&quot; &#39;x&#39; &euro;</p>", `<b> is "bold" 'x' €`},
		{"script and style content dropped", `<p>keep</p><script>alert("x")</script><STYLE>p{}</STYLE ><p>too</p>`, "keep too"},
		{"unclosed script drops the rest", "<p>ok</p><script>evil()", "ok"},
		{"comments dropped", "<p>a<!-- hidden -->b</p>", "ab"},
		{"quoted > in an attribute", `<p title="a>b">text</p>`, "text"},
		{"image without text", `<p>see <span class="image-wrap"><img src="https://sd.local/x?a=1&amp;b=2" onerror="x()"></span> here</p>`, "see here"},
		{"less-than that is not a tag", "<p>1 < 2 and 3<4</p>", "1 < 2 and 3<4"},
		{"mention inside HTML", "<p>hi [~bob],</p><p>done</p>", "hi @bob, done"},
		{"unknown tags are separators", "<p>a</p><section>b</section><custom-tag>c</custom-tag>", "a b c"},
		{"wiki markup untouched", "*bold* {code}x < y{code} &amp;", "*bold* {code}x < y{code} &amp;"},
		{"angle text that is not a known tag", "use <T> generics &amp; more", "use <T> generics &amp; more"},
		{"known name without a word boundary", "a <bold> <b_x> <p2> claim &amp; x", "a <bold> <b_x> <p2> claim &amp; x"},
		{"known tag without a closing >", "x <p and &amp; y", "x <p and &amp; y"},
		{"a lone end tag is HTML", "text</P>&amp;more", "text &more"},
		{"tag split over lines", "<p\nclass=\"x\">a</p>", "a"},
		{"non-ASCII text kept", "<p>Привіт, <b>світ</b>!</p><br>Ünïcode", "Привіт, світ! Ünïcode"},
	}
	for _, tc := range tests {
		if got := PlainText(tc.in, 300); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// The cap applies to the text, not to the markup.
	long := "<div>" + strings.Repeat("<p>слово</p>", 200) + "</div>"
	got := PlainText(long, 300)
	if n := utf8.RuneCountInString(got); n != 300 || !strings.HasSuffix(got, "…") || strings.Contains(got, "<") {
		t.Errorf("long HTML: %d runes %q", n, got)
	}
}

// --- setWindowTheme (SPEC v0.4 §2) -------------------------------------------

func TestSetWindowTheme(t *testing.T) {
	tests := []struct {
		name   string
		params string
		want   *WindowTheme // nil: bad_params, the hook is not called
	}{
		{"dark with caption", `{"dark":true,"caption":"#1e1F22"}`, &WindowTheme{Dark: true, Caption: &RGB{0x1e, 0x1f, 0x22}}},
		{"light without caption", `{"dark":false}`, &WindowTheme{}},
		{"null caption is no caption", `{"dark":true,"caption":null}`, &WindowTheme{Dark: true}},
		{"params as a JSON string", `"{\"dark\":true,\"caption\":\"#FFFFFF\"}"`, &WindowTheme{Dark: true, Caption: &RGB{0xff, 0xff, 0xff}}},
		{"dark missing", `{}`, nil},
		{"no params", ``, nil},
		{"dark not a bool", `{"dark":"yes"}`, nil},
		{"dark null", `{"dark":null}`, nil},
		{"caption without #", `{"dark":true,"caption":"1e1f22"}`, nil},
		{"short caption", `{"dark":true,"caption":"#fff"}`, nil},
		{"caption with alpha", `{"dark":true,"caption":"#1e1f22ff"}`, nil},
		{"caption not hex", `{"dark":true,"caption":"#1e1g22"}`, nil},
		{"css rgb()", `{"dark":true,"caption":"rgb(30, 31, 34)"}`, nil},
		{"empty caption", `{"dark":true,"caption":""}`, nil},
		{"caption not a string", `{"dark":true,"caption":123}`, nil},
		{"params not an object", `[true]`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, plat, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
			res, err := a.Call(context.Background(), "setWindowTheme", json.RawMessage(tc.params))
			if res != nil {
				t.Errorf("result %v, want null", res)
			}
			if tc.want == nil {
				if e := ToError(err); err == nil || e.Code != CodeBadParams {
					t.Errorf("err %v, want bad_params", err)
				}
				if len(plat.themes) != 0 {
					t.Errorf("hook called with %+v", plat.themes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plat.themes) != 1 {
				t.Fatalf("hook calls %+v", plat.themes)
			}
			got := plat.themes[0]
			if got.Dark != tc.want.Dark || (got.Caption == nil) != (tc.want.Caption == nil) ||
				got.Caption != nil && *got.Caption != *tc.want.Caption {
				t.Errorf("hook got %+v (caption %v), want %+v (caption %v)", got, got.Caption, *tc.want, tc.want.Caption)
			}
		})
	}
}

// setWindowTheme needs no Jira connection (it is called at startup, also
// before onboarding).
func TestSetWindowThemeWithoutConfiguration(t *testing.T) {
	plat := &fakePlatform{}
	a := newRegApp(t, plat)
	if _, err := a.Call(context.Background(), "setWindowTheme", json.RawMessage(`{"dark":true}`)); err != nil || len(plat.themes) != 1 {
		t.Errorf("err %v, calls %+v", err, plat.themes)
	}
}

// --- epic time (SPEC v0.4 §3) -------------------------------------------------

// timeFx are the issues of treeJira with logged time / estimates. Epic time
// uses aggregate values (own + sub-tasks) and skips counted issues whose
// parent is counted too. own = timespent / timeoriginalestimate,
// agg = aggregatetimespent / aggregatetimeoriginalestimate:
//
//	RBI-1 epic         own 3600 / 7200
//	  RBI-10 story     own 1800 / –    agg 2400 / 900  (incl. its sub-task RBI-11)
//	    RBI-11 sub-task own 600 / 900  parent RBI-10 is counted ⇒ skipped (embedded only in RBI-10)
//	  RBI-12 task      own 1000 / 2000 (under RBI-10, RBI-20 and the epic: counted once)
//	  RBI-13 task      own – / 500     agg 700 / 2000  (sub-tasks RBI-15, RBI-16: not in the tree)
//	  RBI-14 task      –
//	  RBI-20 story     own 200 / –, linked to sub-task RBI-17
//	    RBI-17 sub-task own 300 / 100  parent RBI-50 not counted ⇒ its own aggregate counts
//	RBI-2 epic (no children, no time)
//	RBI-30 orphan story with RBI-31 (no epic: not counted anywhere)
type timeFx struct{ e1, s10, t11, t12, t13, s20, t17, t31 fx }

func newTimeFx() timeFx {
	x := timeFx{e1: fxRBI1, s10: fxRBI10, t11: fxRBI11, t12: fxRBI12, t13: fxRBI13, s20: fxRBI20, t31: fxRBI31}
	x.e1.spent, x.e1.estimate = 3600, 7200
	x.s10.spent, x.s10.aggSpent, x.s10.aggEstimate = 1800, 2400, 900
	x.t11.spent, x.t11.estimate, x.t11.parent = 600, 900, "RBI-10"
	x.t12.spent, x.t12.estimate = 1000, 2000
	x.t13.estimate, x.t13.aggSpent, x.t13.aggEstimate = 500, 700, 2000
	x.t13.subtasks = []string{"RBI-15:new", "RBI-16:done"}
	x.s20.spent = 200
	x.s20.links = append(append([]string{}, fxRBI20.links...), "out:relates:RBI-17:sub:new")
	x.t17 = fx{key: "RBI-17", typ: "sub", cat: "new", assignee: "bob", spent: 300, estimate: 100, parent: "RBI-50"}
	x.t31.spent = 99999
	return x
}

func timeTreeJira() *fakeJira {
	x := newTimeFx()
	return &fakeJira{
		routes: map[string]string{
			treeEpicsJQL:  fxList(fxRBI2, x.e1),
			treeOrphanJQL: fxList(fxRBI30),
			treeChildJQL:  fxList(x.s20, fxRBI14, x.t13, x.t12, x.s10),
		},
		byKey: fxPool(x.e1, x.t11, x.t12, x.t17, x.t31),
	}
}

const timeOnlyFields = "aggregatetimespent,aggregatetimeoriginalestimate,parent"

func TestEpicTime(t *testing.T) {
	// failRelated makes the tree's own key search (with summary etc.) fail,
	// so RBI-11 and RBI-17 stay embedded copies; failTime fails the
	// time-only search.
	isTimeOnly := func(r *jira.Request) (bool, bool) {
		u, _ := url.Parse(r.URL)
		q := u.Query()
		if u.Path != "/rest/api/2/search" || !specKeysJQL.MatchString(q.Get("jql")) {
			return false, false
		}
		return true, q.Get("fields") == timeOnlyFields
	}
	// RBI-1 + RBI-10 (agg) + RBI-12 + RBI-13 (agg) + RBI-20 + RBI-17; RBI-11 is in RBI-10's aggregate.
	full := EpicTime{SpentSeconds: 3600 + 2400 + 1000 + 700 + 200 + 300, EstimateSeconds: 7200 + 900 + 2000 + 2000 + 100, Issues: 8}
	// RBI-17 unreadable; RBI-11 unreadable too, but its time is in RBI-10's aggregate.
	withoutSub := EpicTime{SpentSeconds: full.SpentSeconds - 300, EstimateSeconds: full.EstimateSeconds - 100, Issues: 8}
	tests := []struct {
		name                  string
		failRelated, failTime bool
		want                  EpicTime
		partial               bool
		timeSearch            string // sorted keys of the time-only search, "" = none
	}{
		{"all read by the tree searches", false, false, full, false, ""},
		// RBI-11 is embedded in RBI-10's sub-tasks: in its aggregate, never fetched.
		{"embedded-only sub-tasks fetched for their time", true, false, full, false, "RBI-17"},
		{"embedded-only sub-tasks unreadable", true, true, withoutSub, true, "RBI-17"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := timeTreeJira()
			var timeOnly []string // keys of the time-only searches (also failed ones)
			fj.status = func(r *jira.Request) int {
				keyQuery, isTime := isTimeOnly(r)
				if isTime {
					u, _ := url.Parse(r.URL)
					keys := strings.Split(strings.ReplaceAll(specKeysJQL.FindStringSubmatch(u.Query().Get("jql"))[1], `"`, ""), ",")
					sort.Strings(keys)
					timeOnly = append(timeOnly, strings.Join(keys, ","))
				}
				if keyQuery && (isTime && tc.failTime || !isTime && tc.failRelated) {
					return 500
				}
				return 0
			}
			a := v2App(t, fj, []string{"RBI"})
			r := getEpicsT(t, a, `{}`)
			if len(r.Epics) != 2 || r.Epics[0].Epic.Key != "RBI-1" {
				t.Fatalf("epics %+v", r.Epics)
			}
			if got := r.Epics[0].Time; got != tc.want {
				t.Errorf("RBI-1 time %+v, want %+v", got, tc.want)
			}
			if got := r.Epics[1].Time; got != (EpicTime{Issues: 1}) {
				t.Errorf("RBI-2 time %+v (the epic alone)", got)
			}
			if r.TimePartial != tc.partial {
				t.Errorf("timePartial %v, want %v", r.TimePartial, tc.partial)
			}
			if r.Epics[0].Progress.Total+1 != r.Epics[0].Time.Issues {
				t.Errorf("counted set (%d) is not the epic + its progress set (%d)", r.Epics[0].Time.Issues, r.Epics[0].Progress.Total)
			}
			if got := strings.Join(timeOnly, "|"); got != tc.timeSearch {
				t.Errorf("time-only searches %q, want %q", got, tc.timeSearch)
			}

			js, _ := json.Marshal(r)
			for _, want := range []string{
				`"time":{"spentSeconds":` + itoa(tc.want.SpentSeconds) + `,"estimateSeconds":` + itoa(tc.want.EstimateSeconds) + `,"issues":8}`,
				`"time":{"spentSeconds":0,"estimateSeconds":0,"issues":1}`,
				`"timePartial":` + map[bool]string{true: "true", false: "false"}[tc.partial],
			} {
				if !strings.Contains(string(js), want) {
					t.Errorf("JSON lacks %s", want)
				}
			}

			// Cached with the tree; force recomputes.
			before := fj.searches
			if c := getEpicsT(t, a, `{}`); fj.searches != before || c.Epics[0].Time != tc.want || c.TimePartial != tc.partial {
				t.Errorf("cached: %d new searches, time %+v partial %v", fj.searches-before, c.Epics[0].Time, c.TimePartial)
			}
			fj.status = nil
			if f := getEpicsT(t, a, `{"force":true}`); f.Epics[0].Time != full || f.TimePartial {
				t.Errorf("forced: time %+v partial %v", f.Epics[0].Time, f.TimePartial)
			}
		})
	}
}

// The parent rule, one case per row: a counted issue is skipped only when
// its parent is counted for the same epic.
func TestEpicTimeParentRule(t *testing.T) {
	tests := []struct {
		name  string
		epic  fx
		child []fx // Epic Link children of the epic
		pool  []fx // issues served by key
		want  EpicTime
	}{
		{"task with two sub-tasks outside the tree: its aggregate counts",
			fx{key: "RBI-1", typ: "epic", spent: 10},
			[]fx{{key: "RBI-13", epic: "RBI-1", spent: 100, estimate: 50, aggSpent: 400, aggEstimate: 350, subtasks: []string{"RBI-15:new", "RBI-16:done"}}},
			nil,
			EpicTime{SpentSeconds: 410, EstimateSeconds: 350, Issues: 2}},
		{"sub-task whose parent story is in the tree: not counted twice",
			fx{key: "RBI-1", typ: "epic"},
			[]fx{{key: "RBI-10", typ: "story", epic: "RBI-1", spent: 100, aggSpent: 160, aggEstimate: 70, subtasks: []string{"RBI-11:new"}}},
			[]fx{{key: "RBI-11", typ: "sub", spent: 60, estimate: 70, parent: "RBI-10"}},
			EpicTime{SpentSeconds: 160, EstimateSeconds: 70, Issues: 3}},
		{"linked sub-task whose parent is outside the tree: its own aggregate counts",
			fx{key: "RBI-1", typ: "epic"},
			[]fx{{key: "RBI-10", typ: "story", epic: "RBI-1", spent: 100, links: []string{"out:relates:RBI-17:sub:new"}}},
			[]fx{{key: "RBI-17", typ: "sub", spent: 300, estimate: 30, parent: "RBI-50"}},
			EpicTime{SpentSeconds: 400, EstimateSeconds: 30, Issues: 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJira{
				routes: map[string]string{
					`issuetype = Epic AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`: fxList(tc.epic),
					treeOrphanJQL: "[]",
					`cf[10103] in ("RBI-1") ORDER BY key DESC`: fxList(tc.child...),
				},
				byKey: fxPool(tc.pool...),
			}
			a := v2App(t, fj, []string{"RBI"})
			r := getEpicsT(t, a, `{}`)
			if len(r.Epics) != 1 || r.Epics[0].Time != tc.want || r.TimePartial {
				t.Errorf("epics %+v partial %v, want time %+v", r.Epics, r.TimePartial, tc.want)
			}
		})
	}
}

// An issue in the trees of two epics counts for both; "Only mine" keeps the
// whole epic's time and drops epics that are not mine.
func TestEpicTimeSharedIssueAndMine(t *testing.T) {
	x := newTimeFx()
	e3 := fx{key: "RBI-3", typ: "epic", cat: "new", assignee: "bob", spent: 60}
	s40 := fx{key: "RBI-40", typ: "story", cat: "new", assignee: "bob", epic: "RBI-3",
		links: []string{"out:relates:RBI-12:task:indeterminate"}}
	fj := timeTreeJira()
	fj.routes[treeEpicsJQL] = fxList(e3, fxRBI2, x.e1)
	fj.routes[`cf[10103] in ("RBI-3","RBI-2","RBI-1") ORDER BY key DESC`] = fxList(s40, x.s20, fxRBI14, x.t13, x.t12, x.s10)
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{"scope":"mine"}`)
	byKey := map[string]EpicTime{}
	for _, e := range r.Epics {
		byKey[e.Epic.Key] = e.Time
	}
	// RBI-3: itself 60 + RBI-40 (none) + RBI-12 (1000 / 2000); mine through RBI-12.
	if got := byKey["RBI-3"]; got != (EpicTime{SpentSeconds: 1060, EstimateSeconds: 2000, Issues: 3}) {
		t.Errorf("RBI-3 %+v", got)
	}
	if got := byKey["RBI-1"]; got.SpentSeconds != 8200 || got.EstimateSeconds != 12200 || got.Issues != 8 {
		t.Errorf("RBI-1 %+v (RBI-12 counts for both epics)", got)
	}
	if _, ok := byKey["RBI-2"]; ok || len(r.Epics) != 2 || r.TimePartial {
		t.Errorf("mine: %+v partial %v", byKey, r.TimePartial)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// --- Dev / QA story points (SPEC v0.4 §4) ------------------------------------

// v4Fields adds the real Jira's Dev/QA story points select fields and the
// number fields they must not be confused with.
var v4Fields = strings.TrimSuffix(realFields, "]") + `,
 {"id":"customfield_10200","name":"Dev Points","custom":true,"clauseNames":["cf[10200]","Dev Points"],"schema":{"type":"number"}},
 {"id":"customfield_10201","name":"QA Points","custom":true,"clauseNames":["cf[10201]","QA Points"],"schema":{"type":"number"}},
 {"id":"customfield_10204","name":"Dev Story Points","custom":true,"clauseNames":["cf[10204]","Dev Story Points"],"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"}},
 {"id":"customfield_10205","name":"QA Story Points","custom":true,"clauseNames":["cf[10205]","QA Story Points"],"schema":{"type":"option","custom":"com.atlassian.jira.plugin.system.customfieldtypes:select"}}
]`

func TestIssueDevQAStoryPoints(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name     string
		fields   string // GET /field ("" = v4Fields)
		values   string // extra custom field JSON of the issue
		dev, qa  *float64
		requests bool // the detail request asks for 10204/10205
	}{
		{"select options", "", `"customfield_10204":{"self":"x","value":"8","id":"10601"},"customfield_10205":{"value":"2.5"}`, f(8), f(2.5), true},
		{"numbers and numeric strings", "", `"customfield_10204":3,"customfield_10205":" 5 "`, f(3), f(5), true},
		{"only dev", "", `"customfield_10204":{"value":"13"},"customfield_10205":null`, f(13), nil, true},
		{"none", "", `"customfield_10200":7,"customfield_10201":9`, nil, nil, true},
		{"not numeric", "", `"customfield_10204":{"value":"XL"},"customfield_10205":[{"value":"1"}]`, nil, nil, true},
		{"fields missing on this Jira", realFields, `"customfield_10204":{"value":"8"}`, nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(detailRBI12, `"customfield_10103":"RBI-1",`, `"customfield_10103":"RBI-1",`+tc.values+`,`, 1)
			fj := &fakeJira{issueBodies: map[string]string{"RBI-12": body}, comments2: map[string]int{"RBI-12": 3},
				byKey: fxPool(fxRBI1), fieldsJSON: tc.fields}
			if fj.fieldsJSON == "" {
				fj.fieldsJSON = v4Fields
			}
			a := v2App(t, fj, []string{"RBI"})
			d := getIssueT(t, a, "RBI-12")
			if !samePoints(d.DevStoryPoints, tc.dev) || !samePoints(d.QAStoryPoints, tc.qa) {
				t.Errorf("dev %v qa %v, want %v %v", pv(d.DevStoryPoints), pv(d.QAStoryPoints), pv(tc.dev), pv(tc.qa))
			}
			if d.StoryPoints == nil || *d.StoryPoints != 5 {
				t.Errorf("story points %v", pv(d.StoryPoints))
			}
			var asked string
			for _, p := range fj.requests("/rest/api/2/issue/RBI-12?") {
				u, _ := url.Parse(p)
				asked = u.Query().Get("fields")
			}
			if got := strings.Contains(asked, "customfield_10204") && strings.Contains(asked, "customfield_10205"); got != tc.requests {
				t.Errorf("detail fields %q", asked)
			}
			js, _ := json.Marshal(d)
			for key, v := range map[string]*float64{"devStoryPoints": tc.dev, "qaStoryPoints": tc.qa} {
				want := `"` + key + `":`
				if has := strings.Contains(string(js), want); has != (v != nil) {
					t.Errorf("JSON %s present=%v, want %v", key, has, v != nil)
				}
			}
		})
	}
}

// Dev/QA story points are read only by getIssue: list queries do not ask
// for them.
func TestDevQAStoryPointsOnlyInIssueDetail(t *testing.T) {
	fj := timeTreeJira()
	fj.fieldsJSON = v4Fields
	a := v2App(t, fj, []string{"RBI"})
	getEpicsT(t, a, `{}`)
	for _, p := range fj.requests("/rest/api/2/search?") {
		if strings.Contains(p, "customfield_10204") || strings.Contains(p, "customfield_10205") {
			t.Errorf("search asks for Dev/QA story points: %s", p)
		}
	}
}

func samePoints(a, b *float64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func pv(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// sumTimes on a small pool: the by-key budget, an expired context, and
// embedded sub-tasks of a counted issue (never fetched, never partial).
func TestSumTimesBudget(t *testing.T) {
	e1 := fx{key: "RBI-1", typ: "epic", spent: 100}
	s10 := fx{key: "RBI-10", typ: "story", epic: "RBI-1", spent: 10, aggSpent: 70, subtasks: []string{"RBI-11:new"},
		links: []string{"out:relates:RBI-12:task:new", "out:relates:RBI-13:task:new"}}
	t12 := fx{key: "RBI-12", spent: 1000}
	t13 := fx{key: "RBI-13", spent: 2000}
	full := int64(100 + 70 + 1000 + 2000)
	tests := []struct {
		name     string
		budget   int
		cancel   bool
		other    bool // a second epic counts RBI-11 without its parent
		want     int64
		partial  bool
		searches []string // keys of the time-only searches
	}{
		{"budget for every embedded issue", 2, false, false, full, false, []string{"RBI-12,RBI-13"}},
		{"budget cut", 1, false, false, full - 2000, true, []string{"RBI-12"}},
		{"no budget left", 0, false, false, full - 3000, true, nil},
		{"negative budget", -5, false, false, full - 3000, true, nil},
		{"context expired", 2, true, false, full - 3000, true, nil},
		{"sub-task counted without its parent is fetched", 3, false, true, full, false, []string{"RBI-12,RBI-13,RBI-11"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t11 := fx{key: "RBI-11", typ: "sub", spent: 60, parent: "RBI-10"}
			fj := &fakeJira{byKey: fxPool(t11, t12, t13)}
			a := v2App(t, fj, []string{"RBI"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, id, err := a.client()
			if err != nil {
				t.Fatal(err)
			}
			v, err := a.newView(ctx, c, id)
			if err != nil {
				t.Fatal(err)
			}
			pool := newTreePool(v)
			for _, x := range []fx{e1, s10} {
				var is jira.Issue
				if err := json.Unmarshal([]byte(x.json()), &is); err != nil {
					t.Fatal(err)
				}
				pool.addEmbedded(pool.add(is, true))
			}
			epics := []EpicNode{{counted: []string{"RBI-1", "RBI-10", "RBI-11", "RBI-12", "RBI-13"}}}
			if tc.other {
				epics = append(epics, EpicNode{counted: []string{"RBI-11"}})
			}
			if tc.cancel {
				cancel()
			}
			var keys []string
			before := len(fj.searched())
			partial := pool.sumTimes(ctx, epics, tc.budget)
			for _, q := range fj.searched()[before:] {
				if m := specKeysJQL.FindStringSubmatch(q); m != nil {
					keys = append(keys, strings.ReplaceAll(m[1], `"`, ""))
				}
			}
			if got := epics[0].Time; got.SpentSeconds != tc.want || got.Issues != 5 {
				t.Errorf("time %+v, want spent %d of 5 issues", got, tc.want)
			}
			if partial != tc.partial {
				t.Errorf("partial %v, want %v", partial, tc.partial)
			}
			if strings.Join(keys, "|") != strings.Join(tc.searches, "|") {
				t.Errorf("searches %q, want %q", keys, tc.searches)
			}
			if tc.other {
				if got := epics[1].Time; got != (EpicTime{SpentSeconds: 60, Issues: 1}) {
					t.Errorf("second epic %+v", got)
				}
			}
		})
	}
}

// The time fields are only requested by getEpics' epic, child and related
// searches; getStories, the orphan-story search and getIssue keep the base
// tree fields.
func TestTimeFieldsOnlyInEpicSearches(t *testing.T) {
	fj := timeTreeJira()
	fj.routes[treeStoriesJQL] = fxList(fxRBI30, fxRBI20, fxRBI10)
	a := v2App(t, fj, []string{"RBI"})
	getEpicsT(t, a, `{}`)
	if _, err := a.Call(context.Background(), "getStories", json.RawMessage(`{"force":true}`)); err != nil {
		t.Fatal(err)
	}
	withTime := map[string]bool{}
	for _, p := range fj.requests("/rest/api/2/search?") {
		u, _ := url.Parse(p)
		q := u.Query()
		jql, fields := q.Get("jql"), q.Get("fields")
		has := strings.Contains(fields, "aggregatetimespent") && strings.Contains(fields, "parent")
		switch {
		case jql == treeEpicsJQL || jql == treeChildJQL:
			if !has {
				t.Errorf("%s: fields %s lack the time fields", jql, fields)
			}
		case jql == treeOrphanJQL || jql == treeStoriesJQL:
			if strings.Contains(fields, "aggregate") || strings.Contains(fields, "parent") {
				t.Errorf("%s: fields %s", jql, fields)
			}
		case specKeysJQL.MatchString(jql):
			withTime[strconv.FormatBool(has)] = true
		}
	}
	// getEpics' related search asks for them, getStories' does not.
	if !withTime["true"] || !withTime["false"] {
		t.Errorf("key searches with/without time fields: %v", withTime)
	}
}
