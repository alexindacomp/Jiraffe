package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// --- fixtures -----------------------------------------------------------------------------

// v6Fields are the real Jira's v0.6 fields on top of the v0.4 ones.
var v6Fields = strings.TrimSuffix(v4Fields, "]") + `,
 {"id":"customfield_13619","name":"Merge_requests","custom":true,"clauseNames":["cf[13619]","Merge_requests"],"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"}},
 {"id":"customfield_13702","name":"Branches","custom":true,"clauseNames":["cf[13702]","Branches"],"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"}},
 {"id":"customfield_10400","name":"Developer","custom":true,"clauseNames":["cf[10400]","Developer"],"schema":{"type":"array","items":"user","custom":"com.atlassian.jira.plugin.system.customfieldtypes:multiuserpicker"}},
 {"id":"customfield_10402","name":"Reviewer","custom":true,"clauseNames":["cf[10402]","Reviewer"],"schema":{"type":"array","items":"user","custom":"com.atlassian.jira.plugin.system.customfieldtypes:multiuserpicker"}},
 {"id":"customfield_10401","name":"QA Engineer","custom":true,"clauseNames":["cf[10401]","QA Engineer"],"schema":{"type":"array","items":"user","custom":"com.atlassian.jira.plugin.system.customfieldtypes:multiuserpicker"}}
]`

// link is a stored issue link: source <outward> destination.
type link struct {
	id, typeID, source, destination string
}

var v6LinkTypes = map[string][3]string{ // id → name, inward, outward
	"10000": {"Blocks", "is blocked by", "blocks"},
	"10003": {"Relates", "relates to", "relates to"},
}

// v6Jira serves the v0.6 endpoints and issues whose issuelinks are derived
// from stored links exactly as Jira's GET does: on the source an entry with
// "outwardIssue": destination, on the destination one with "inwardIssue":
// source.
type v6Jira struct {
	v5        *v5Jira
	links     []link
	nextID    int
	comments  map[string][]string // issue key → comment JSON objects
	writes    []write
	resp      map[string]*jira.Response // "METHOD path" → forced answer
	users     string
	linkCalls int
	fields    map[string]string // issue key → extra fields JSON members
}

func (v *v6Jira) issueJSON(key string) string {
	var ls []string
	for _, l := range v.links {
		t := v6LinkTypes[l.typeID]
		typ := fmt.Sprintf(`{"id":%q,"name":%q,"inward":%q,"outward":%q}`, l.typeID, t[0], t[1], t[2])
		switch key {
		case l.source:
			ls = append(ls, fmt.Sprintf(`{"id":%q,"type":%s,"outwardIssue":%s}`, l.id, typ, embedded(l.destination, "task", "new")))
		case l.destination:
			ls = append(ls, fmt.Sprintf(`{"id":%q,"type":%s,"inwardIssue":%s}`, l.id, typ, embedded(l.source, "task", "new")))
		}
	}
	cs := v.comments[key]
	extra := ""
	if f := v.fields[key]; f != "" {
		extra = "," + f
	}
	return fmt.Sprintf(`{"id":"1","key":%q,"fields":{"summary":"S %s","status":%s,"issuetype":%s,"project":{"key":"RBI"},
	 "updated":"2026-11-12T10:00:00.000+0200","created":"2026-11-01T10:00:00.000+0200","issuelinks":[%s],
	 "comment":{"total":%d,"comments":[%s]}%s},"changelog":{"histories":[]}}`,
		key, key, fxStatus["new"], fxTypes["task"], strings.Join(ls, ","), len(cs), strings.Join(cs, ","), extra)
}

func (v *v6Jira) handle(r *jira.Request, u *url.URL) (*jira.Response, bool) {
	p, q := u.Path, u.Query()
	if forced := v.resp[r.Method+" "+p]; forced != nil {
		if r.Method != "GET" {
			v.writes = append(v.writes, write{r.Method, p, string(r.Body)})
		}
		return forced, true
	}
	ok := func(body string) (*jira.Response, bool) {
		return &jira.Response{StatusCode: 200, Body: []byte(body)}, true
	}
	switch {
	case p == "/rest/api/2/issueLinkType":
		v.linkCalls++
		var ts []string
		for _, id := range []string{"10000", "10003"} {
			t := v6LinkTypes[id]
			ts = append(ts, fmt.Sprintf(`{"id":%q,"name":%q,"inward":%q,"outward":%q,"self":"x"}`, id, t[0], t[1], t[2]))
		}
		return ok(`{"issueLinkTypes":[` + strings.Join(ts, ",") + `]}`)
	case p == "/rest/api/2/issueLink" && r.Method == "POST":
		v.writes = append(v.writes, write{r.Method, p, string(r.Body)})
		var b struct {
			Type         struct{ ID string }
			InwardIssue  struct{ Key string }
			OutwardIssue struct{ Key string }
		}
		_ = json.Unmarshal(r.Body, &b)
		v.nextID++
		// Jira: the link source is "inwardIssue", its destination "outwardIssue".
		v.links = append(v.links, link{id: fmt.Sprint(500 + v.nextID), typeID: b.Type.ID, source: b.InwardIssue.Key, destination: b.OutwardIssue.Key})
		return &jira.Response{StatusCode: 201}, true
	case strings.HasPrefix(p, "/rest/api/2/issueLink/") && r.Method == "DELETE":
		v.writes = append(v.writes, write{r.Method, p, ""})
		id := strings.TrimPrefix(p, "/rest/api/2/issueLink/")
		for i, l := range v.links {
			if l.id == id {
				v.links = append(v.links[:i], v.links[i+1:]...)
				break
			}
		}
		return &jira.Response{StatusCode: 204}, true
	case p == "/rest/api/2/user/search":
		return ok(v.users)
	case strings.HasSuffix(p, "/comment") && strings.HasPrefix(p, "/rest/api/2/issue/"):
		key := strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/2/issue/"), "/comment")
		if r.Method == "POST" {
			v.writes = append(v.writes, write{r.Method, p, string(r.Body)})
			var b struct{ Body string }
			_ = json.Unmarshal(r.Body, &b)
			bs, _ := json.Marshal(b.Body)
			c := fmt.Sprintf(`{"id":"9%d","author":{"name":"alex","key":"alex","displayName":"Alex"},"body":%s,"created":"2026-11-12T11:00:00.000+0200","updated":"2026-11-12T11:00:00.000+0200"}`,
				len(v.comments[key]), bs)
			v.comments[key] = append(v.comments[key], c)
			return &jira.Response{StatusCode: 201, Body: []byte(c)}, true
		}
		cs := v.comments[key]
		return ok(fmt.Sprintf(`{"startAt":0,"maxResults":%d,"total":%d,"comments":[%s]}`, len(cs), len(cs), strings.Join(cs, ",")))
	case strings.HasPrefix(p, "/rest/api/2/issue/RBI-") && r.Method == "GET" && !strings.Contains(p[len("/rest/api/2/issue/"):], "/"):
		key := strings.TrimPrefix(p, "/rest/api/2/issue/")
		if _, isV5 := v.v5.issue[key]; isV5 && q.Get("expand") == "" {
			return v.v5.handle(r, u) // the v0.5 edit fixtures
		}
		return ok(string(projectFields(json.RawMessage(v.issueJSON(key)), q.Get("fields"))))
	}
	return v.v5.handle(r, u)
}

func v6App(t *testing.T) (*App, *fakeJira, *v6Jira, *recorder) {
	t.Helper()
	a, fj, v5, rec := v5App(t)
	fj.fieldsJSON = v6Fields
	v := &v6Jira{v5: v5, comments: map[string][]string{}, resp: map[string]*jira.Response{}, fields: map[string]string{},
		users: `[{"name":"bob","displayName":"Bob"},{"name":"none","displayName":"Неназначено"}]`}
	fj.handle = v.handle
	return a, fj, v, rec
}

func getIssueV6(t *testing.T, a *App, key string) *IssueDetail {
	t.Helper()
	res, err := call(t, a, "getIssue", `{"key":"`+key+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	return res.(*IssueDetail)
}

func linkSummary(ls []LinkDTO) string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Relation+" "+l.Issue.Key)
	}
	return strings.Join(out, "; ")
}

// --- issue detail ---------------------------------------------------------------------------

func TestIssueDetailV6Fields(t *testing.T) {
	a, _, v, _ := v6App(t)
	v.fields["RBI-40"] = `"customfield_13619":"https://gitlab.example/front/-/merge_requests/6224\r\nfront: MF-8026/chips\r\n",
	 "customfield_13702":" Не заполнено ",
	 "customfield_10400":[{"name":"bob","displayName":"Bob"},{"name":"nobody","displayName":"неназначено"}],
	 "customfield_10402":[{"name":"x","displayName":"Не назначено"}],
	 "customfield_10401":null`
	d := getIssueV6(t, a, "RBI-40")
	if d.MergeRequests == nil || *d.MergeRequests != "https://gitlab.example/front/-/merge_requests/6224\nfront: MF-8026/chips" ||
		d.Branches == nil || *d.Branches != "" {
		t.Errorf("MR %v branches %v", d.MergeRequests, d.Branches)
	}
	if d.Developer == nil || !reflect.DeepEqual(*d.Developer, []UserDTO{{Name: "bob", DisplayName: "Bob"}}) ||
		d.Reviewer == nil || len(*d.Reviewer) != 0 || d.QA == nil || len(*d.QA) != 0 {
		t.Errorf("people %v %v %v", d.Developer, d.Reviewer, d.QA)
	}
	js, _ := json.Marshal(d)
	for _, want := range []string{`"developer":[{"name":"bob","displayName":"Bob"}]`, `"reviewer":[]`, `"qa":[]`,
		`"mergeRequests":"https://gitlab.example/front/-/merge_requests/6224\nfront: MF-8026/chips"`, `"branches":""`, `"commentFormat":"html"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
	// Fields this Jira does not have are absent.
	b, fj2, v2, _ := v6App(t)
	fj2.fieldsJSON = realFields
	v2.fields["RBI-40"] = `"customfield_13619":"x"`
	js, _ = json.Marshal(getIssueV6(t, b, "RBI-40"))
	for _, absent := range []string{`"developer"`, `"reviewer"`, `"qa"`, `"mergeRequests"`, `"branches"`} {
		if strings.Contains(string(js), absent) {
			t.Errorf("JSON has %s without the field", absent)
		}
	}
}

func TestCommentFormat(t *testing.T) {
	cm := func(body string) jira.Comment { return jira.Comment{Body: body} }
	many := []jira.Comment{}
	for i := 0; i < 11; i++ {
		many = append(many, cm("*wiki*"))
	}
	tests := []struct {
		name     string
		comments []jira.Comment
		want     string
	}{
		{"none", nil, ""},
		{"wiki", []jira.Comment{cm("*a*"), cm("plain")}, DescWiki},
		{"mostly html", []jira.Comment{cm("<p>a</p>"), cm(`<div class="x">b</div>`), cm("plain")}, DescHTML},
		{"tie is wiki", []jira.Comment{cm("<p>a</p>"), cm("plain")}, DescWiki},
		{"empty bodies ignored", []jira.Comment{cm(" "), cm("<p>a</p>")}, DescHTML},
		{"only the latest 10", append(append([]jira.Comment{}, many[:1]...), append(make([]jira.Comment, 0), cmRepeat("<p>x</p>", 10)...)...), DescHTML},
		{"older html does not count", append(cmRepeat("<p>x</p>", 5), many...), DescWiki},
	}
	for _, tc := range tests {
		if got := commentFormat(tc.comments); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// In the detail: the comments decide; none ⇒ the project's format.
	a, fj, v, _ := v6App(t)
	if d := getIssueV6(t, a, "RBI-41"); d.CommentFormat != DescHTML { // RBI sample: mostly HTML
		t.Errorf("no comments, HTML project: %s", d.CommentFormat)
	}
	v.comments["RBI-41"] = []string{`{"id":"1","body":"*wiki*","author":{"name":"bob"}}`}
	if d := getIssueV6(t, a, "RBI-41"); d.CommentFormat != DescWiki {
		t.Errorf("wiki comment: %s", d.CommentFormat)
	}
	_ = fj
}

func cmRepeat(body string, n int) []jira.Comment {
	out := make([]jira.Comment, n)
	for i := range out {
		out[i] = jira.Comment{Body: body}
	}
	return out
}

// --- inline editing ---------------------------------------------------------------------------

const rbi50EditMeta = `{"fields":{
 "summary":{"required":true,"schema":{"type":"string","system":"summary"},"name":"Summary"},
 "assignee":{"required":false,"schema":{"type":"user","system":"assignee"},"name":"Assignee"},
 "reporter":{"required":true,"schema":{"type":"user","system":"reporter"},"name":"Reporter"},
 "customfield_10400":{"required":false,"schema":{"type":"array","items":"user"},"name":"Developer"},
 "customfield_10402":{"required":false,"schema":{"type":"array","items":"user"},"name":"Reviewer"},
 "customfield_10401":{"required":false,"schema":{"type":"array","items":"user"},"name":"QA Engineer"},
 "customfield_13619":{"required":false,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"},"name":"Merge_requests"},
 "customfield_13702":{"required":false,"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:textarea"},"name":"Branches"},
 "customfield_10130":{"required":false,"schema":{"type":"number"},"name":"Story Points"},
 "labels":{"required":false,"schema":{"type":"array","items":"string","system":"labels"},"name":"Labels"}
}}`

func rbi50Fields(updated string) string {
	return fmt.Sprintf(`{"updated":%q,"issuetype":%s,"project":{"key":"RBI","name":"Project RBI"},"summary":"S","description":"*wiki*",
	 "assignee":null,"reporter":{"name":"bob","displayName":"Bob"},
	 "customfield_10400":[{"name":"x","displayName":"неназначено"}],"customfield_10402":[{"name":"carol","displayName":"Carol"}],
	 "customfield_10401":null,"customfield_13619":"Не заполнено","customfield_13702":"front: a\r\nback:b","customfield_10130":3,"labels":[]}`,
		updated, fxTypes["task"])
}

func TestEditMetaV6Fields(t *testing.T) {
	a, _, v, _ := v6App(t)
	v.v5.editMeta["RBI-50"] = rbi50EditMeta
	v.v5.issue["RBI-50"] = rbi50Fields(v5Updated)
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-50"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(*EditMetaDTO)
	// The extras follow the main fields, in their order, as main fields.
	if got := fieldSummary(m.Fields); got != "summary:summary! assignee:user customfield_10107:sprint customfield_10130:storyPoints labels:labels "+
		"reporter:user! customfield_10400:users customfield_10402:users customfield_10401:users customfield_13619:textarea customfield_13702:textarea" {
		t.Errorf("fields %s", got)
	}
	js, _ := json.Marshal(m.Values)
	want := `{"customfield_10130":3,"customfield_10400":[],"customfield_10402":[{"name":"carol","displayName":"Carol"}],
	 "customfield_13619":"","customfield_13702":"front: a\nback:b","labels":[],"reporter":{"name":"bob","displayName":"Bob"},"summary":"S"}`
	if canon(t, string(js)) != canon(t, want) {
		t.Errorf("values\n%s\nwant\n%s", js, canon(t, want))
	}
	// Single-field updates of every new field.
	for _, tc := range []struct{ values, body string }{
		{`{"reporter":{"name":"carol"}}`, `{"fields":{"reporter":{"name":"carol"}}}`},
		{`{"customfield_10400":[{"name":"bob"},"carol"]}`, `{"fields":{"customfield_10400":[{"name":"bob"},{"name":"carol"}]}}`},
		{`{"customfield_10402":[]}`, `{"fields":{"customfield_10402":[]}}`},
		{`{"customfield_10401":["alex"]}`, `{"fields":{"customfield_10401":[{"name":"alex"}]}}`},
		{`{"customfield_13619":"https://gitlab.example/x/-/merge_requests/1\nhttps://gitlab.example/y"}`,
			`{"fields":{"customfield_13619":"https://gitlab.example/x/-/merge_requests/1\nhttps://gitlab.example/y"}}`},
		{`{"customfield_13702":""}`, `{"fields":{"customfield_13702":null}}`},
		{`{"customfield_10130":5}`, `{"fields":{"customfield_10130":5}}`},
	} {
		v.v5.writes = nil
		if _, err := call(t, a, "updateIssue", `{"key":"RBI-50","values":`+tc.values+`,"expectedUpdated":"`+v5Updated+`"}`); err != nil {
			t.Errorf("%s: %v", tc.values, err)
			continue
		}
		if len(v.v5.writes) != 1 || canon(t, v.v5.writes[0].body) != canon(t, tc.body) {
			t.Errorf("%s: writes %+v", tc.values, v.v5.writes)
		}
	}
	// The reporter is required: it cannot be emptied; a tag in a textarea is screened.
	for values, code := range map[string]string{
		`{"reporter":null}`:                           CodeValidation,
		`{"customfield_13702":"{code}<x a=1>{code}"}`: CodeValidation,
		`{"customfield_10400":[{"name":""}]}`:         CodeValidation,
		`{"customfield_99":1}`:                        CodeBadParams,
	} {
		if _, err := call(t, a, "updateIssue", `{"key":"RBI-50","values":`+values+`,"expectedUpdated":"`+v5Updated+`"}`); ToError(err).Code != code {
			t.Errorf("%s: %v, want %s", values, err, code)
		}
	}
}

// --- people search, field links ---------------------------------------------------------------

func TestSearchUsers(t *testing.T) {
	a, fj, _, _ := v6App(t)
	res, err := call(t, a, "searchUsers", `{"query":" bo "}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.([]UserDTO); !reflect.DeepEqual(got, []UserDTO{{Name: "bob", DisplayName: "Bob"}}) {
		t.Errorf("users %+v (placeholder dropped)", got)
	}
	if res, err := call(t, a, "searchUsers", `{"query":"  "}`); err != nil || len(res.([]UserDTO)) != 0 {
		t.Errorf("empty query: %v %v", res, err)
	}
	if reqs := fj.requests("/rest/api/2/user/search"); len(reqs) != 1 || reqs[0] != "/rest/api/2/user/search?maxResults=20&username=bo" {
		t.Errorf("requests %v", reqs)
	}
	if _, err := call(t, a, "searchUsers", `{"query":"`+strings.Repeat("x", 101)+`"}`); ToError(err).Code != CodeBadParams {
		t.Errorf("long query: %v", err)
	}
}

func TestOpenFieldLink(t *testing.T) {
	const mr = "https://gitlab.example/front/-/merge_requests/6224"
	tests := []struct {
		name, params string
		opened       bool
	}{
		{"a token of the MR field", `{"key":"RBI-40","field":"mergeRequests","url":"` + mr + `"}`, true},
		{"a token of the branches field", `{"key":"RBI-40","field":"branches","url":"https://git.example/b"}`, true},
		{"in the other field only", `{"key":"RBI-40","field":"branches","url":"` + mr + `"}`, false},
		{"a prefix of a token", `{"key":"RBI-40","field":"mergeRequests","url":"https://gitlab.example/front"}`, false},
		{"not in the field", `{"key":"RBI-40","field":"mergeRequests","url":"https://evil.example/"}`, false},
		{"javascript (in the field)", `{"key":"RBI-40","field":"mergeRequests","url":"javascript:alert(1)"}`, false},
		{"user info (in the field)", `{"key":"RBI-40","field":"mergeRequests","url":"https://u:p@gitlab.example/x"}`, false},
		{"unknown field", `{"key":"RBI-40","field":"description","url":"` + mr + `"}`, false},
		{"bad key", `{"key":"RBI 40","field":"mergeRequests","url":"` + mr + `"}`, false},
	}
	for _, tc := range tests {
		a, _, v, _ := v6App(t)
		v.fields["RBI-40"] = `"customfield_13619":"see ` + mr + `\r\njavascript:alert(1) https://u:p@gitlab.example/x",
		 "customfield_13702":"repo: https://git.example/b"`
		_, err := call(t, a, "openFieldLink", tc.params)
		plat := a.plat.(*fakePlatform)
		if tc.opened {
			if err != nil || len(plat.opened) != 1 || !strings.Contains(tc.params, plat.opened[0]) {
				t.Errorf("%s: %v opened %v", tc.name, err, plat.opened)
			}
		} else if ToError(err).Code != CodeBadParams || len(plat.opened) != 0 {
			t.Errorf("%s: %v opened %v", tc.name, err, plat.opened)
		}
	}
	// A Jira without the fields: bad_params.
	b, fj, v, _ := v6App(t)
	fj.fieldsJSON = realFields
	v.fields["RBI-40"] = `"customfield_13619":"` + mr + `"`
	if _, err := call(t, b, "openFieldLink", `{"key":"RBI-40","field":"mergeRequests","url":"`+mr+`"}`); ToError(err).Code != CodeBadParams {
		t.Errorf("no field: %v", err)
	}
}

// --- links ---------------------------------------------------------------------------------------

func TestLinkTypesCached(t *testing.T) {
	a, _, v, _ := v6App(t)
	clock := a.clock.(*fakeClock)
	for i, step := range []struct {
		after time.Duration
		calls int
	}{{0, 1}, {59 * time.Minute, 1}, {2 * time.Minute, 2}} {
		clock.t = clock.t.Add(step.after)
		res, err := call(t, a, "getLinkTypes", ``)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.([]LinkTypeDTO); len(got) != 2 || got[0] != (LinkTypeDTO{ID: "10000", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}) {
			t.Errorf("types %+v", got)
		}
		if v.linkCalls != step.calls {
			t.Errorf("step %d: %d requests, want %d", i, v.linkCalls, step.calls)
		}
	}
}

// addIssueLink(key, type, direction, target) reads back as "key <phrase>
// target" on key and as the opposite phrase on target, with a fake that
// stores links the way Jira's POST does and lists them the way Jira's GET
// does.
func TestAddIssueLinkDirection(t *testing.T) {
	a, _, v, rec := v6App(t)
	if _, err := call(t, a, "addIssueLink", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"rbi-61"}`); err != nil {
		t.Fatal(err)
	}
	if w := v.writes[0]; canon(t, w.body) != `{"inwardIssue":{"key":"RBI-60"},"outwardIssue":{"key":"RBI-61"},"type":{"id":"10000"}}` {
		t.Errorf("POST %s", w.body)
	}
	if got := linkSummary(getIssueV6(t, a, "RBI-60").Links); got != "blocks RBI-61" {
		t.Errorf("RBI-60: %s", got)
	}
	if got := linkSummary(getIssueV6(t, a, "RBI-61").Links); got != "is blocked by RBI-60" {
		t.Errorf("RBI-61: %s", got)
	}
	if _, err := call(t, a, "addIssueLink", `{"key":"RBI-60","typeId":"10000","direction":"inward","target":"RBI-62"}`); err != nil {
		t.Fatal(err)
	}
	if got := linkSummary(getIssueV6(t, a, "RBI-60").Links); got != "blocks RBI-61; is blocked by RBI-62" {
		t.Errorf("RBI-60 after an inward link: %s", got)
	}
	if got := linkSummary(getIssueV6(t, a, "RBI-62").Links); got != "blocks RBI-60" {
		t.Errorf("RBI-62: %s", got)
	}
	if len(rec.of("board")) != 2 {
		t.Errorf("board events %d", len(rec.of("board")))
	}
	// Link ids are in the detail.
	if ls := getIssueV6(t, a, "RBI-60").Links; ls[0].ID != "501" || ls[1].ID != "502" {
		t.Errorf("ids %+v", ls)
	}
}

func TestAddIssueLinkErrors(t *testing.T) {
	for _, tc := range []struct {
		name, params string
		forced       *jira.Response
		code         string
	}{
		{"unknown type", `{"key":"RBI-60","typeId":"999","direction":"outward","target":"RBI-61"}`, nil, CodeBadParams},
		{"type not numeric", `{"key":"RBI-60","typeId":"Blocks","direction":"outward","target":"RBI-61"}`, nil, CodeBadParams},
		{"bad direction", `{"key":"RBI-60","typeId":"10000","direction":"both","target":"RBI-61"}`, nil, CodeBadParams},
		{"itself", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"rbi-60"}`, nil, CodeBadParams},
		{"bad target", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"RBI 61"}`, nil, CodeBadParams},
		{"no permission", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"RBI-61"}`,
			&jira.Response{StatusCode: 403, Body: []byte(`{}`)}, CodeForbiddenEdit},
		{"Jira rejects", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"RBI-61"}`,
			&jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["Issue linking is disabled"]}`)}, CodeValidation},
		{"target not found", `{"key":"RBI-60","typeId":"10000","direction":"outward","target":"RBI-61"}`,
			&jira.Response{StatusCode: 404, Body: []byte(`{"errorMessages":["No issue"]}`)}, jira.CodeNotFound},
	} {
		a, _, v, rec := v6App(t)
		if tc.forced != nil {
			v.resp["POST /rest/api/2/issueLink"] = tc.forced
		}
		_, err := call(t, a, "addIssueLink", tc.params)
		if ToError(err).Code != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
		if tc.forced == nil && len(v.writes) != 0 || len(rec.of("board")) != 0 {
			t.Errorf("%s: writes %+v events %+v", tc.name, v.writes, rec.events)
		}
	}
}

func TestDeleteIssueLink(t *testing.T) {
	a, _, v, rec := v6App(t)
	v.links = []link{{id: "501", typeID: "10000", source: "RBI-60", destination: "RBI-61"},
		{id: "502", typeID: "10003", source: "RBI-70", destination: "RBI-71"}}
	for params, code := range map[string]string{
		`{"key":"RBI-60","linkId":"502"}`: CodeBadParams, // another issue's link
		`{"key":"RBI-60","linkId":"999"}`: CodeBadParams,
		`{"key":"RBI-60","linkId":"5x"}`:  CodeBadParams,
		`{"key":"RBI 60","linkId":"501"}`: CodeBadParams,
	} {
		if _, err := call(t, a, "deleteIssueLink", params); ToError(err).Code != code {
			t.Errorf("%s: %v", params, err)
		}
	}
	if len(v.writes) != 0 {
		t.Fatalf("deleted %+v", v.writes)
	}
	// The destination may remove the link too.
	if _, err := call(t, a, "deleteIssueLink", `{"key":"RBI-61","linkId":"501"}`); err != nil {
		t.Fatal(err)
	}
	if len(v.writes) != 1 || v.writes[0].method != "DELETE" || v.writes[0].path != "/rest/api/2/issueLink/501" || len(rec.of("board")) != 1 {
		t.Errorf("writes %+v events %+v", v.writes, rec.events)
	}
	if got := linkSummary(getIssueV6(t, a, "RBI-60").Links); got != "" {
		t.Errorf("links left %s", got)
	}
	v.links = []link{{id: "503", typeID: "10000", source: "RBI-60", destination: "RBI-61"}}
	v.resp["DELETE /rest/api/2/issueLink/503"] = &jira.Response{StatusCode: 403, Body: []byte(`{}`)}
	if _, err := call(t, a, "deleteIssueLink", `{"key":"RBI-60","linkId":"503"}`); ToError(err).Code != CodeForbiddenEdit {
		t.Errorf("403: %v", err)
	}
}

func TestSearchIssuesForLink(t *testing.T) {
	a, fj, _, _ := v6App(t)
	for params, jql := range map[string]string{
		`{"purpose":"link","query":""}`:                       `ORDER BY updated DESC`,
		`{"purpose":"link","query":"Load data"}`:              `(summary ~ "load data*") ORDER BY updated DESC`,
		`{"purpose":"link","project":"RBI","query":"cma-12"}`: `(summary ~ "cma-12*" OR key = "CMA-12") ORDER BY updated DESC`,
	} {
		fj.routes[jql] = fxList(fxRBI12)
		res, err := call(t, a, "searchIssuesFor", params)
		if err != nil || len(res.([]IssueRef)) != 1 {
			t.Errorf("%s: %v %v", params, res, err)
		}
	}
}

// --- comments ----------------------------------------------------------------------------------

func TestAddComment(t *testing.T) {
	long := strings.Repeat("я", 32001)
	tests := []struct {
		name, body, format string
		comments           []string // the issue's comments before
		forced             *jira.Response
		code               string // "" = created
		sent               string
	}{
		{"wiki", "  Fixed in *1.2* \n", "wiki", []string{`{"id":"1","body":"*a*"}`}, nil, "", "Fixed in *1.2*"},
		{"html", `<p dir="auto">ok <strong>done</strong></p>`, "html", []string{`{"id":"1","body":"<p>a</p>"}`}, nil, "", `<p dir="auto">ok <strong>done</strong></p>`},
		{"html with an attachment image", `<p dir="auto"><span class="image-wrap"><img src="https://sd.local/secure/attachment/1/a.png"></span></p>`, "html",
			[]string{`{"id":"1","body":"<p>a</p>"}`}, nil, "", `<p dir="auto"><span class="image-wrap"><img src="https://sd.local/secure/attachment/1/a.png"></span></p>`},
		{"no comments: the project's format (HTML)", `<p dir="auto">x</p>`, "html", nil, nil, "", `<p dir="auto">x</p>`},
		{"format of another kind", "*a*", "html", []string{`{"id":"1","body":"*a*"}`}, nil, CodeBadParams, ""},
		{"unknown format", "a", "markdown", nil, nil, CodeBadParams, ""},
		{"empty", "  \n ", "wiki", []string{`{"id":"1","body":"*a*"}`}, nil, CodeValidation, ""},
		{"too long", long, "wiki", []string{`{"id":"1","body":"*a*"}`}, nil, CodeValidation, ""},
		{"html: foreign image", `<p dir="auto"><img src="https://cdn.example/a.png"></p>`, "html", []string{`{"id":"1","body":"<p>a</p>"}`}, nil, CodeValidation, ""},
		{"html: script", `<p dir="auto">a</p><script>x</script>`, "html", []string{`{"id":"1","body":"<p>a</p>"}`}, nil, CodeValidation, ""},
		{"wiki: script", "see <script>x</script>", "wiki", []string{`{"id":"1","body":"*a*"}`}, nil, CodeValidation, ""},
		{"wiki: code is not exempt", "{code}<x a=1>{code}", "wiki", []string{`{"id":"1","body":"*a*"}`}, nil, CodeValidation, ""},
		{"no permission", "x", "wiki", []string{`{"id":"1","body":"*a*"}`}, &jira.Response{StatusCode: 403, Body: []byte(`{}`)}, CodeForbiddenComment, ""},
		{"Jira rejects", "x", "wiki", []string{`{"id":"1","body":"*a*"}`},
			&jira.Response{StatusCode: 400, Body: []byte(`{"errors":{"comment":"Comment body can not be empty!"}}`)}, CodeValidation, ""},
		{"bad key", "x", "wiki", nil, nil, CodeBadParams, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, _, v, _ := v6App(t)
			v.comments["RBI-80"] = tc.comments
			if tc.forced != nil {
				v.resp["POST /rest/api/2/issue/RBI-80/comment"] = tc.forced
			}
			key := "RBI-80"
			if tc.name == "bad key" {
				key = "RBI 80"
			}
			b, _ := json.Marshal(tc.body)
			res, err := call(t, a, "addComment", `{"key":"`+key+`","body":`+string(b)+`,"format":"`+tc.format+`"}`)
			if tc.code != "" {
				e := ToError(err)
				if e.Code != tc.code {
					t.Fatalf("%v, want %s", err, tc.code)
				}
				if tc.forced == nil && len(v.writes) != 0 {
					t.Errorf("sent %+v", v.writes)
				}
				if tc.code == CodeValidation && (len(e.Fields) != 1 || e.Fields[0].Field != "body") {
					t.Errorf("fields %+v", e.Fields)
				}
				if tc.code == CodeForbiddenComment && e.Message != T("ru", "err_forbidden_comment") {
					t.Errorf("message %q", e.Message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			d := res.(*CommentDTO)
			sent, _ := json.Marshal(map[string]string{"body": tc.sent})
			if len(v.writes) != 1 || canon(t, v.writes[0].body) != canon(t, string(sent)) || d.Body != tc.sent ||
				d.Author == nil || d.Author.Name != "alex" || d.ID == "" || d.Created == "" {
				t.Errorf("comment %+v writes %+v", d, v.writes)
			}
		})
	}
}

// My own new comment never produces an alert (the poller skips my
// comments), while a colleague's comment in the same poll does.
func TestOwnCommentIsNotAlerted(t *testing.T) {
	a, fj, v, _ := v6App(t)
	v.comments["RBI-80"] = []string{`{"id":"1","body":"*a*"}`}
	res, err := call(t, a, "addComment", `{"key":"RBI-80","body":"mine","format":"wiki"}`)
	if err != nil {
		t.Fatal(err)
	}
	mine := res.(*CommentDTO)
	clock := a.clock.(*fakeClock)
	fj.comments = `[]`
	_ = a.PollComments(context.Background()) // the first poll only initialises tracking
	clock.t = clock.t.Add(time.Minute)
	now := jira.FormatTime(clock.t)
	fj.comments = `[{"key":"RBI-80","fields":{"summary":"S","project":{"key":"RBI"},"updated":"` + now + `","comment":{"total":2,"comments":[
	 {"id":"` + mine.ID + `","author":{"name":"alex","key":"alex","displayName":"Alex"},"body":"mine","created":"` + now + `"},
	 {"id":"95","author":{"name":"bob","key":"bob","displayName":"Bob"},"body":"theirs","created":"` + now + `"}]}}}]`
	if err := a.PollComments(context.Background()); err != nil {
		t.Fatal(err)
	}
	ns := a.st.Snapshot().Notifications
	if len(ns) != 1 || ns[0].ID != "95" {
		t.Errorf("notifications %+v (only the colleague's comment)", ns)
	}
}

// CR-29: invisible format characters never pass as a field link URL.
func TestFieldLinkURLRejectsFormatCharacters(t *testing.T) {
	for _, u := range []string{"https://gitlab.example/x", "http://a.b/c?d=e#f", "https://gitlab.example/пример"} {
		if !fieldLinkURL(u) {
			t.Errorf("%q rejected", u)
		}
	}
	for _, r := range []rune{0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069,
		0x200B, 0x200C, 0x200D, 0x200E, 0x200F, 0xFEFF, 0x00AD, 0x0085} {
		if u := "https://gitlab.example/" + string(r) + "x"; fieldLinkURL(u) {
			t.Errorf("U+%04X accepted", r)
		}
	}
	if fieldLinkURL("https://gitlab.example/\xff") {
		t.Error("invalid UTF-8 accepted")
	}
}

// FieldMeta.role names the purpose of the fields the panel edits in place;
// other fields have none (omitted in JSON).
func TestFieldMetaRole(t *testing.T) {
	a, _, v, _ := v6App(t)
	v.v5.editMeta["RBI-50"] = strings.Replace(rbi50EditMeta, `"labels":`, `"description":{"required":false,"schema":{"type":"string","system":"description"},"name":"Description"},
	 "customfield_10204":{"required":false,"schema":{"type":"option"},"name":"Dev Story Points","allowedValues":[{"id":"1","value":"3"}]},
	 "customfield_10205":{"required":false,"schema":{"type":"option"},"name":"QA Story Points","allowedValues":[{"id":"2","value":"5"}]},
	 "labels":`, 1)
	v.v5.issue["RBI-50"] = rbi50Fields(v5Updated)
	res, err := call(t, a, "getEditMeta", `{"key":"RBI-50"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range res.(*EditMetaDTO).Fields {
		got[f.ID] = f.Role
	}
	want := map[string]string{
		"summary": "", "assignee": "assignee", "reporter": "reporter", "description": "description", "labels": "",
		"customfield_10107": "", "customfield_10130": "storyPoints", "customfield_10204": "devStoryPoints",
		"customfield_10205": "qaStoryPoints", "customfield_10400": "developer", "customfield_10402": "reviewer",
		"customfield_10401": "qa", "customfield_13619": "mergeRequests", "customfield_13702": "branches",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("roles %v\nwant %v", got, want)
	}
	js, _ := json.Marshal(res)
	if !strings.Contains(string(js), `"name":"Developer","kind":"users","required":false,"main":true,"role":"developer"}`) ||
		strings.Contains(string(js), `"role":""`) || !strings.Contains(string(js), `{"id":"summary","name":"Summary","kind":"summary","required":true,"main":true,"max":255}`) {
		t.Errorf("JSON %s", js)
	}
}

// DR-37: a URL followed by sentence punctuation in the field opens without
// it; the stripped and the verbatim token both match, nothing else.
func TestOpenFieldLinkTrailingPunctuation(t *testing.T) {
	const base = "https://gitlab.example/front/-/merge_requests/"
	tests := []struct {
		name, url string
		opened    bool
	}{
		{"comma stripped", base + "57", true},
		{"paren and dot stripped", base + "58", true},
		{"verbatim token with punctuation", base + "59),", true},
		{"only after the strip (paren, comma)", base + "59", true},
		{"several trailing characters", base + "60", true},
		{"half stripped is no token", base + "58)", false},
		{"a prefix still does not match", base + "5", false},
		{"inner punctuation is kept", base + "61", false},
		{"a leading paren is not stripped", base + "62", false},
	}
	for _, tc := range tests {
		a, _, v, _ := v6App(t)
		v.fields["RBI-40"] = `"customfield_13619":"see ` + base + `57, then ` + base + `58). Also ` + base + `59), ` +
			base + `60.;:) and ` + base + `61,x (` + base + `62)"`
		_, err := call(t, a, "openFieldLink", `{"key":"RBI-40","field":"mergeRequests","url":"`+tc.url+`"}`)
		opened := a.plat.(*fakePlatform).opened
		if tc.opened {
			if err != nil || len(opened) != 1 || opened[0] != tc.url {
				t.Errorf("%s: %v opened %v", tc.name, err, opened)
			}
		} else if ToError(err).Code != CodeBadParams || len(opened) != 0 {
			t.Errorf("%s: %v opened %v", tc.name, err, opened)
		}
	}
}
