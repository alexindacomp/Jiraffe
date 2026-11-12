package app

import (
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// Issue type and status fixtures as on the real Jira: localized names,
// classification by id.
var (
	fxTypes = map[string]string{
		"epic":  `{"id":"10000","name":"Epic","subtask":false}`,
		"story": `{"id":"10001","name":"История","subtask":false}`,
		"task":  `{"id":"3","name":"Задача","subtask":false}`,
		"sub":   `{"id":"5","name":"Подзадача","subtask":true}`,
	}
	fxStatus = map[string]string{
		"new":           `{"id":"1","name":"ToDo","statusCategory":{"key":"new"}}`,
		"indeterminate": `{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}`,
		"done":          `{"id":"10","name":"Готово","statusCategory":{"key":"done"}}`,
	}
	// Learned like on the real Jira: /issuetype only names "Epic" in English,
	// the story type id comes from the "issuetype = Story" query.
	fxIssueTypes = `[` + fxTypes["epic"] + `,` + fxTypes["story"] + `,` + fxTypes["task"] + `,` + fxTypes["sub"] + `]`
	fxLearnStory = `[{"key":"RBI-999","fields":{"issuetype":` + fxTypes["story"] + `}}]`
)

// fx describes an issue fixture.
type fx struct {
	key, typ, cat, assignee string
	epic                    string   // Epic Link
	sp                      float64  // story points (0 = none)
	name, color             string   // epic name / colour
	links                   []string // "out|in:relation:KEY:typ:cat"
	subtasks                []string // "KEY:cat"
	spent, estimate         int64    // own timespent / timeoriginalestimate in seconds (0 = null)
	aggSpent, aggEstimate   int64    // aggregate (with sub-tasks) values; 0 = same as the own value
	parent                  string   // parent issue key (sub-tasks)
}

func embedded(key, typ, cat string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"S %s","status":%s,"issuetype":%s}}`, key, key, fxStatus[cat], fxTypes[typ])
}

func (x fx) json() string {
	if x.cat == "" {
		x.cat = "new"
	}
	if x.typ == "" {
		x.typ = "task"
	}
	f := []string{
		fmt.Sprintf(`"summary":"S %s"`, x.key),
		`"status":` + fxStatus[x.cat],
		`"issuetype":` + fxTypes[x.typ],
		fmt.Sprintf(`"project":{"key":%q}`, x.key[:strings.LastIndexByte(x.key, '-')]),
		`"updated":"2026-11-11T09:00:00.000+0200"`,
		`"priority":{"name":"Major"}`,
	}
	if x.assignee != "" {
		f = append(f, fmt.Sprintf(`"assignee":{"name":%q,"key":%q,"displayName":%q}`, x.assignee, x.assignee, strings.ToUpper(x.assignee[:1])+x.assignee[1:]))
	} else {
		f = append(f, `"assignee":null`)
	}
	if x.epic != "" {
		f = append(f, fmt.Sprintf(`"customfield_10103":%q`, x.epic))
	} else {
		f = append(f, `"customfield_10103":null`)
	}
	if x.sp != 0 {
		f = append(f, fmt.Sprintf(`"customfield_10130":%v`, x.sp))
	}
	orOwn := func(agg, own int64) int64 {
		if agg != 0 {
			return agg
		}
		return own
	}
	for _, t := range []struct {
		name string
		v    int64
	}{{"timespent", x.spent}, {"timeoriginalestimate", x.estimate},
		{"aggregatetimespent", orOwn(x.aggSpent, x.spent)}, {"aggregatetimeoriginalestimate", orOwn(x.aggEstimate, x.estimate)}} {
		if t.v != 0 {
			f = append(f, fmt.Sprintf(`%q:%d`, t.name, t.v))
		} else {
			f = append(f, fmt.Sprintf(`%q:null`, t.name))
		}
	}
	if x.name != "" {
		f = append(f, fmt.Sprintf(`"customfield_10105":%q,"customfield_10106":%q`, x.name, x.color))
	}
	var links []string
	for i, l := range x.links {
		p := strings.Split(l, ":")
		side := "outwardIssue"
		if p[0] == "in" {
			side = "inwardIssue"
		}
		links = append(links, fmt.Sprintf(`{"id":"%d","type":{"name":"T","inward":"is %s by","outward":%q},%q:%s}`,
			i+1, p[1], p[1], side, embedded(p[2], p[3], p[4])))
	}
	f = append(f, `"issuelinks":[`+strings.Join(links, ",")+`]`)
	var subs []string
	for _, s := range x.subtasks {
		p := strings.Split(s, ":")
		subs = append(subs, embedded(p[0], "sub", p[1]))
	}
	f = append(f, `"subtasks":[`+strings.Join(subs, ",")+`]`)
	if x.parent != "" {
		f = append(f, `"parent":`+embedded(x.parent, "task", "new"))
	}
	return fmt.Sprintf(`{"id":"%d","key":%q,"fields":{%s}}`, fxID(x.key), x.key, strings.Join(f, ","))
}

// fxID is a stable, practically unique numeric issue id per key (FNV-1a).
func fxID(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32()
}

// specAllJQL is the "all" board query for the given tracked keys (already
// normalised: upper case, sorted), written out from SPEC.md v0.2.
func specAllJQL(keys ...string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = `"` + k + `"`
	}
	return "project in (" + strings.Join(q, ",") + ") AND statusCategory != Done ORDER BY updated DESC"
}

func fxList(items ...fx) string {
	var out []string
	for _, x := range items {
		out = append(out, x.json())
	}
	return "[" + strings.Join(out, ",") + "]"
}

// fxPool registers issues for "issuekey in (…)" lookups.
func fxPool(items ...fx) map[string]string {
	m := map[string]string{}
	for _, x := range items {
		m[x.key] = x.json()
	}
	return m
}

func strp(s string) *string { return &s }

// v2App opens an app on the fake with the given tracked projects (nil =
// not initialised).
func v2App(t *testing.T, fj *fakeJira, tracked []string) *App {
	t.Helper()
	if fj.worklogs == nil {
		fj.worklogs = map[string][]jira.Worklog{}
	}
	if fj.typesJSON == "" {
		fj.typesJSON = fxIssueTypes
	}
	if fj.routes == nil {
		fj.routes = map[string]string{}
	}
	if fj.projectsJSON == "" && len(tracked) > 0 { // the tracked projects exist in Jira
		var ps []string
		for _, k := range tracked {
			ps = append(ps, fmt.Sprintf(`{"key":%q,"name":"Project %s"}`, k, k))
		}
		fj.projectsJSON = "[" + strings.Join(ps, ",") + "]"
	}
	if _, ok := fj.routes[learnStoryJQL]; !ok {
		fj.routes[learnStoryJQL] = fxLearnStory
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)}, nil)
	if tracked != nil {
		if _, err := a.cfg.UpdateTrackedProjects("https://sd.local", func([]string) []string { return tracked }); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func countJQL(fj *fakeJira, jql string) int {
	n := 0
	for _, q := range fj.searched() {
		if q == jql {
			n++
		}
	}
	return n
}
