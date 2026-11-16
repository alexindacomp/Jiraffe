package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// v0.8 fixtures: a Jira with the Developer / Reviewer / QA Engineer fields.
const v8Fields = `[
 {"id":"customfield_10300","name":"Developer","custom":true,"clauseNames":["cf[10300]","Developer"],"schema":{"type":"array","items":"user"}},
 {"id":"customfield_10301","name":"Reviewer","custom":true,"clauseNames":["cf[10301]","Reviewer"],"schema":{"type":"array","items":"user"}},
 {"id":"customfield_10302","name":"QA Engineer","custom":true,"clauseNames":["cf[10302]","QA Engineer"],"schema":{"type":"array","items":"user"}}
]`

const (
	alexJSON = `{"name":"alex","key":"alex","displayName":"Alex"}`
	bobJSON  = `{"name":"bob","key":"bob","displayName":"Bob"}`
)

// withFields adds raw fields ("customfield_10302":[…]) to an issue fixture.
func withFields(issue, extra string) string {
	return strings.Replace(issue, `"fields":{`, `"fields":{`+extra+`,`, 1)
}

// createdJSON is a hit of the author search.
func createdJSON(key, created, creator string) string {
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"S %s","status":{"name":"Open"},"created":%q,"creator":%s}}`, key, key, created, creator)
}

// commentsJSON is a hit of the commenter search; comments are "author@created".
func commentsJSON(key string, total int, comments ...string) string {
	var cs []string
	for i, c := range comments {
		who, at, _ := strings.Cut(c, "@")
		cs = append(cs, fmt.Sprintf(`{"id":"%d","author":{"name":%q,"key":%q},"body":"b","created":%q}`, i+1, who, who, at))
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"S %s","status":{"name":"Open"},"comment":{"total":%d,"maxResults":%d,"comments":[%s]}}}`,
		key, key, total, len(cs), strings.Join(cs, ","))
}

func runItems(run worklog.Run) string {
	var out []string
	for _, it := range run.Items {
		out = append(out, fmt.Sprintf("%s %v todo=%d share=%d fixed=%d", it.Key, it.Roles, it.TodoSeconds, it.ShareSeconds, it.FixedSeconds))
	}
	return strings.Join(out, " | ")
}

// Acceptance 1: one In Progress issue, one issue I created and 2 comments on
// another log status share + 15m + 2×5m within the cap, fixed time first;
// a re-run logs nothing new.
func TestV8DefaultRolesDay(t *testing.T) {
	fj := &fakeJira{
		issues:  issuesJSON(allDayA1),
		created: issuesJSON(createdJSON("N-2", "2026-11-11T11:00:00.000+0200", alexJSON), createdJSON("OLD-5", "2026-11-10T23:30:00.000+0200", alexJSON)),
		commented: issuesJSON(commentsJSON("C-3", 4,
			"alex@2026-11-11T16:20:00.000+0200", "bob@2026-11-11T10:00:00.000+0200",
			"alex@2026-11-11T10:15:00.000+0200", "alex@2026-11-10T17:00:00.000+0200"),
			commentsJSON("Q-9", 1, "bob@2026-11-11T12:00:00.000+0200")),
		worklogs: map[string][]jira.Worklog{},
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	if got, want := runItems(run), "A-1 [dev] todo=27300 share=28800 fixed=0 | N-2 [author] todo=900 share=0 fixed=900 | C-3 [commenter] todo=600 share=0 fixed=600"; got != want {
		t.Fatalf("items\n got  %s\n want %s", got, want)
	}
	if run.TotalSeconds != 8*3600 {
		t.Errorf("total %d", run.TotalSeconds)
	}
	items := itemsByKey(run)
	if n := items["N-2"]; n.CreateSeconds != 900 || n.CreateSource != worklog.SourceDefault || n.CommentSource != "" || n.Comments != 0 {
		t.Errorf("N-2 %+v", n)
	}
	if c := items["C-3"]; c.Comments != 2 || c.CommentSeconds != 600 || c.CommentSource != worklog.SourceDefault || c.CreateSource != "" {
		t.Errorf("C-3 %+v", c)
	}
	b, _ := json.Marshal(items["A-1"])
	if !strings.Contains(string(b), `"roles":["dev"],"comments":0,"createSeconds":0,"commentSeconds":0,"fixedSeconds":0,"shareSeconds":28800`) ||
		strings.Contains(string(b), "Source") {
		t.Errorf("item json %s", b)
	}

	if _, err := a.Call(context.Background(), "worklogRun", nil); err != nil {
		t.Fatal(err)
	}
	want := "A-1 27300 2026-11-11T09:00:00.000+0200|N-2 900 2026-11-11T11:00:00.000+0200|C-3 600 2026-11-11T10:15:00.000+0200"
	if got := strings.Join(fj.posted, "|"); got != want {
		t.Errorf("posted\n got  %s\n want %s", got, want)
	}
	// Re-running the day logs nothing new.
	fj.posted = nil
	if _, err := a.Call(context.Background(), "worklogRun", nil); err != nil {
		t.Fatal(err)
	}
	if len(fj.posted) != 0 {
		t.Errorf("re-run posted %v", fj.posted)
	}
}

// Acceptance 2: QA / Reviewer issues are found by their fields and counted
// only while in their role's statuses.
func TestV8QAAndReviewerRoles(t *testing.T) {
	qa := withFields(issueJSON("Q-1", "Done", "2026-11-09T10:00:00.000+0200",
		"2026-11-10T10:00:00.000+0200 ToDo>In Progress",
		"2026-11-11T11:00:00.000+0200 In Progress>InTesting",
		"2026-11-11T15:00:00.000+0200 InTesting>Done"), `"assignee":`+bobJSON+`,"customfield_10302":[`+alexJSON+`]`)
	rev := withFields(issueJSON("R-2", "Done", "2026-11-09T10:00:00.000+0200",
		"2026-11-11T14:00:00.000+0200 In Progress>InReview",
		"2026-11-11T16:00:00.000+0200 InReview>Done"), `"customfield_10301":[`+alexJSON+`]`)
	// Assignee and QA: the In Progress time is the developer's, not mine.
	both := withFields(allDayA1, `"assignee":`+alexJSON+`,"customfield_10302":[`+bobJSON+`,`+alexJSON+`]`)
	// The Developer field and QA: both roles, so In Progress counts.
	devQA := withFields(issueJSON("D-4", "In Progress", "2026-11-09T10:00:00.000+0200",
		"2026-11-11T17:00:00.000+0200 ToDo>In Progress"), `"customfield_10300":[`+alexJSON+`],"customfield_10302":`+alexJSON)
	fj := &fakeJira{issues: issuesJSON(qa, rev, both, devQA), fieldsJSON: v8Fields, worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	// Q-1 11–13 + 14–15, R-2 14–16, D-4 17–18; 14–15 is split between Q-1 and R-2.
	want := "Q-1 [qa] todo=9000 share=9000 fixed=0 | R-2 [reviewer] todo=5400 share=5400 fixed=0 | D-4 [dev qa] todo=3600 share=3600 fixed=0"
	if got := runItems(run); got != want {
		t.Errorf("items\n got  %s\n want %s", got, want)
	}
	var roleQ string
	for _, q := range fj.searched() {
		if strings.HasPrefix(q, "(assignee = currentUser() OR cf[") {
			roleQ = q
		}
	}
	wantQ := `(assignee = currentUser() OR cf[10300] = currentUser() OR cf[10302] = currentUser() OR cf[10301] = currentUser() OR status changed BY currentUser() DURING (startOfDay(), now())) AND ` +
		`(status changed DURING (startOfDay(), now()) OR statusCategory = "In Progress" OR status IN ("In Progress", "В работе", "В роботі", "ForTesting", "InTesting", "ForBuild", "ReleaseTesting", "ForReview", "InReview"))`
	if roleQ != wantQ {
		t.Errorf("status-roles query\n got  %s\n want %s", roleQ, wantQ)
	}

	// QA statuses empty: the QA role is off, Q-1 is mine as a developer only
	// through the query (In Progress 9–11) and A-1 is plain dev work again.
	fj2 := &fakeJira{issues: issuesJSON(qa, both), fieldsJSON: v8Fields, worklogs: map[string][]jira.Worklog{}}
	b := openApp(t, t.TempDir(), fj2, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, func(s *config.Settings) { s.QAStatuses = []string{} })
	if got, want := runItems(preview(t, b)), "Q-1 [dev] todo=3600 share=3600 fixed=0 | A-1 [dev] todo=25200 share=25200 fixed=0"; got != want {
		t.Errorf("QA off\n got  %s\n want %s", got, want)
	}
	for _, q := range fj2.searched() {
		if strings.Contains(q, "cf[10302]") && strings.HasPrefix(q, "(assignee") {
			t.Errorf("QA field queried while the role is off: %s", q)
		}
	}
}

func TestV8StatusRolesDetection(t *testing.T) {
	q := RoleQuery{DevField: "customfield_1", QAField: "customfield_2", ReviewerField: "customfield_3"}
	field := func(id, users string) map[string]json.RawMessage {
		return map[string]json.RawMessage{id: json.RawMessage(users)}
	}
	merge := func(ms ...map[string]json.RawMessage) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		for _, m := range ms {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	tests := []struct {
		name   string
		custom map[string]json.RawMessage
		q      RoleQuery
		want   []string
	}{
		{"no fields: dev (found as assignee / mover)", nil, q, []string{"dev"}},
		{"QA only", field("customfield_2", `[`+alexJSON+`]`), q, []string{"qa"}},
		{"QA as a single user", field("customfield_2", alexJSON), q, []string{"qa"}},
		{"someone else's QA", field("customfield_2", `[`+bobJSON+`]`), q, []string{"dev"}},
		{"reviewer", field("customfield_3", `[`+bobJSON+`,`+alexJSON+`]`), q, []string{"reviewer"}},
		{"developer and reviewer", merge(field("customfield_1", `[`+alexJSON+`]`), field("customfield_3", `[`+alexJSON+`]`)), q, []string{"dev", "reviewer"}},
		{"all three", merge(field("customfield_1", `[`+alexJSON+`]`), field("customfield_2", `[`+alexJSON+`]`), field("customfield_3", `[`+alexJSON+`]`)), q, []string{"dev", "qa", "reviewer"}},
		{"QA role off", field("customfield_2", `[`+alexJSON+`]`), RoleQuery{DevField: "customfield_1"}, []string{"dev"}},
		{"null field", field("customfield_2", `null`), q, []string{"dev"}},
	}
	for _, tc := range tests {
		f := &jira.Fields{Custom: tc.custom}
		if got := StatusRoles(f, me, tc.q); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestV8RoleQueries(t *testing.T) {
	s := config.Defaults()
	all := roleQueryOf(s, jira.FieldIDs{Developer: "customfield_10300", QA: "customfield_10302", Reviewer: "customfield_10301"})
	none := roleQueryOf(s, jira.FieldIDs{})
	if none.DevField != "" || none.QAField != "" || none.ReviewerField != "" || !reflect.DeepEqual(none.Statuses, s.InProgressStatuses) {
		t.Errorf("no fields: %+v", none)
	}
	s.ReviewStatuses = nil
	s.QAStatuses = []string{"testing", "In Progress"} // overlaps: de-duplicated
	part := roleQueryOf(s, jira.FieldIDs{QA: "customfield_10302", Reviewer: "customfield_10301"})
	if part.ReviewerField != "" || part.QAField != "customfield_10302" ||
		!reflect.DeepEqual(part.Statuses, []string{"In Progress", "В работе", "В роботі", "testing"}) {
		t.Errorf("partial: %+v", part)
	}
	if got := cfClause("customfield_10300"); got != "cf[10300]" {
		t.Errorf("cfClause %q", got)
	}
	for _, id := range []string{"", "summary", "customfield_", "customfield_12a"} {
		if cfClause(id) != "" {
			t.Errorf("cfClause(%q) must be empty", id)
		}
	}
	// A field id that is not customfield_N: the role still works, the clause is omitted.
	if got := peopleJQL(RoleQuery{DevField: "developer", QAField: "customfield_7"}); got != " OR cf[7] = currentUser()" {
		t.Errorf("peopleJQL %q", got)
	}

	date := "2026-11-12"
	during := `DURING ("2026-11-12 00:00", "2026-11-12 23:59")`
	tests := []struct{ name, got, want string }{
		{"past, no fields", PastWorklogJQL(date, none), specPastJQL(date)},
		{"past, all fields", PastWorklogJQL(date, all),
			`status WAS IN ("In Progress", "В работе", "В роботі", "ForTesting", "InTesting", "ForBuild", "ReleaseTesting", "ForReview", "InReview") ` + during +
				` AND (assignee WAS currentUser() ` + during + ` OR cf[10300] = currentUser() OR cf[10302] = currentUser() OR cf[10301] = currentUser() OR status CHANGED BY currentUser() ` + during + `)`},
		{"today, no fields", WorklogJQLFor(time.Date(2026, 11, 11, 18, 0, 0, 0, zone), time.Date(2026, 11, 11, 18, 0, 0, 0, zone), none), specWorklogJQL},
		{"author", AuthorJQL(date), `creator = currentUser() AND created >= "2026-11-12 00:00" AND created <= "2026-11-12 23:59"`},
		{"commenter, no fields", CommenterJQL(date, none),
			`updated >= "2026-11-12 00:00" AND (watcher = currentUser() OR assignee = currentUser() OR creator = currentUser() OR reporter = currentUser())`},
		{"commenter, all fields", CommenterJQL(date, all),
			`updated >= "2026-11-12 00:00" AND (watcher = currentUser() OR assignee = currentUser() OR creator = currentUser() OR reporter = currentUser() OR cf[10300] = currentUser() OR cf[10302] = currentUser() OR cf[10301] = currentUser())`},
		{"related, no fields", RelatedJQL(date, none),
			`(assignee = currentUser() OR creator = currentUser()) AND (resolution = Unresolved OR resolved >= "2026-11-12 00:00")`},
		{"related, QA only", RelatedJQL(date, part),
			`(assignee = currentUser() OR creator = currentUser() OR cf[10302] = currentUser()) AND (resolution = Unresolved OR resolved >= "2026-11-12 00:00")`},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, tc.got, tc.want)
		}
	}
	if PastWorklogJQL(date, RoleQuery{}) != "" {
		t.Error("no statuses must give no query")
	}
}

// The embedded comment list is incomplete: all comments of the issue are read.
func TestV8IncompleteCommentsAreFetched(t *testing.T) {
	fj := &fakeJira{commented: issuesJSON(commentsJSON("C-3", 30, "bob@2026-11-11T10:00:00.000+0200")), worklogs: map[string][]jira.Worklog{}}
	fetched := 0
	fj.handle = func(r *jira.Request, u *url.URL) (*jira.Response, bool) {
		if u.Path != "/rest/api/2/issue/C-3/comment" {
			return nil, false
		}
		fetched++
		var cs []string
		for i := 0; i < 30; i++ {
			who, at := "bob", "2026-11-11T09:30:00.000+0200"
			if i%10 == 0 {
				who, at = "alex", fmt.Sprintf("2026-11-11T1%d:00:00.000+0200", i/10)
			}
			cs = append(cs, fmt.Sprintf(`{"id":"%d","author":{"name":%q,"key":%q},"created":%q}`, i, who, who, at))
		}
		return &jira.Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":1000,"total":30,"comments":[` + strings.Join(cs, ",") + `]}`)}, true
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	if fetched == 0 {
		t.Fatal("comments were not fetched")
	}
	it := itemsByKey(run)["C-3"]
	if it.Comments != 3 || it.CommentSeconds != 900 || it.TodoSeconds != 900 || it.Started != "2026-11-11T10:00:00+02:00" {
		t.Errorf("C-3 %+v", it)
	}
}

// Author / commenter at 0 minutes are off: their searches do not run.
func TestV8FixedRolesOff(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), created: issuesJSON(createdJSON("N-2", "2026-11-11T11:00:00.000+0200", alexJSON)), worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, func(s *config.Settings) { s.AuthorMinutes, s.CommentMinutes = 0, 0 })
	if got, want := runItems(preview(t, a)), "A-1 [dev] todo=28800 share=28800 fixed=0"; got != want {
		t.Errorf("got %s", got)
	}
	for _, q := range fj.searched() {
		if specAuthorJQL.MatchString(q) || specCommenterJQL.MatchString(q) {
			t.Errorf("searched %s", q)
		}
	}
}

func TestV8OverridesMethods(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, zone)}
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, dir, fj, clock, nil)

	res, err := call(t, a, "getWorklogOverrides", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"issues":{}}` {
		t.Errorf("empty overrides %s", b)
	}
	for name, params := range map[string]string{
		"missing overrides": `{}`,
		"not minutes":       `{"overrides":{"createTotalSeconds":90,"issues":{}}}`,
		"negative":          `{"overrides":{"issues":{"A-1":{"commentSeconds":-60}}}}`,
		"too large":         `{"overrides":{"commentTotalSeconds":86460}}`,
		"fraction":          `{"overrides":{"commentTotalSeconds":60.5}}`,
		"lower-case key":    `{"overrides":{"issues":{"a-1":{"createSeconds":60}}}}`,
		"future date":       `{"date":"2026-11-12","overrides":{"issues":{}}}`,
		"too old":           `{"date":"2026-10-11","overrides":{"issues":{}}}`,
		"bad date":          `{"date":"2026-11-1","overrides":{"issues":{}}}`,
	} {
		if _, err := call(t, a, "setWorklogOverrides", params); err == nil || err.(*Error).Code != CodeBadParams {
			t.Errorf("%s: %v", name, err)
		}
	}
	var many []string
	for i := 1; i <= worklog.MaxOverrideIssues+1; i++ {
		many = append(many, fmt.Sprintf(`"A-%d":{"createSeconds":60}`, i))
	}
	if _, err := call(t, a, "setWorklogOverrides", `{"overrides":{"issues":{`+strings.Join(many, ",")+`}}}`); err == nil {
		t.Error("201 issues accepted")
	}

	res, err = call(t, a, "setWorklogOverrides", `{"date":"2026-11-10","overrides":{"createTotalSeconds":1800,"commentTotalSeconds":null,"issues":{"A-1":{"createSeconds":0},"B-2":{},"C-3":{"createSeconds":null}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"createTotalSeconds":1800,"issues":{"A-1":{"createSeconds":0}}}`
	if b, _ := json.Marshal(res); string(b) != want {
		t.Errorf("normalized %s", b)
	}
	// Persisted per day: another app on the same files reads it.
	b := openApp(t, dir, fj, clock, nil)
	got, err := call(t, b, "getWorklogOverrides", `{"date":"2026-11-10"}`)
	if err != nil {
		t.Fatal(err)
	}
	if js, _ := json.Marshal(got); string(js) != want {
		t.Errorf("persisted %s", js)
	}
	if got, _ := call(t, b, "getWorklogOverrides", `{"date":"2026-11-11"}`); got.(worklog.DayOverrides).CreateTotalSeconds != nil {
		t.Errorf("other day %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if !strings.Contains(string(raw), `"worklogOverrides":{"2026-11-10":{"createTotalSeconds":1800,"issues":{"A-1":{"createSeconds":0}},"at":`) {
		t.Errorf("state.json %s", raw)
	}
	// Replaced as a whole.
	if _, err := call(t, a, "setWorklogOverrides", `{"date":"2026-11-10","overrides":{"issues":{"B-2":{"commentSeconds":120}}}}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := call(t, a, "getWorklogOverrides", `{"date":"2026-11-10"}`); got.(worklog.DayOverrides).CreateTotalSeconds != nil ||
		len(got.(worklog.DayOverrides).Issues) != 1 {
		t.Errorf("not replaced: %+v", got)
	}
}

// Acceptance 3: per-issue and group overrides are used by previews and the
// scheduled run of that day.
func TestV8OverridesUsedByScheduledRun(t *testing.T) {
	fj := &fakeJira{
		created: issuesJSON(createdJSON("N-1", "2026-11-11T10:00:00.000+0200", alexJSON), createdJSON("N-2", "2026-11-11T11:00:00.000+0200", alexJSON),
			createdJSON("N-3", "2026-11-11T12:00:00.000+0200", alexJSON)),
		commented: issuesJSON(commentsJSON("C-4", 2, "alex@2026-11-11T09:30:00.000+0200", "alex@2026-11-11T19:30:00.000+0200"),
			commentsJSON("C-5", 1, "alex@2026-11-11T07:00:00.000+0200")),
		worklogs: map[string][]jira.Worklog{},
	}
	clock := &fakeClock{t: time.Date(2026, 11, 11, 15, 0, 0, 0, zone)}
	a := openApp(t, t.TempDir(), fj, clock, nil)
	// Group create total 1h for N-2 and N-3 (N-1 is manual 20m); comments:
	// C-5 manual 0, group 25m for C-4's two comments.
	if _, err := call(t, a, "setWorklogOverrides", `{"overrides":{"createTotalSeconds":3600,"commentTotalSeconds":1500,"issues":{"N-1":{"createSeconds":1200},"C-5":{"commentSeconds":0}}}}`); err != nil {
		t.Fatal(err)
	}
	items := itemsByKey(preview(t, a))
	for key, want := range map[string]string{
		"N-1": "1200 manual 1200", "N-2": "1800 group 1800", "N-3": "1800 group 1800",
		"C-4": "1500 group 1500", "C-5": "0 manual 0",
	} {
		it := items[key]
		src := it.CreateSource + it.CommentSource
		if got := fmt.Sprintf("%d %s %d", it.FixedSeconds, src, it.TodoSeconds); got != want {
			t.Errorf("%s: got %q want %q", key, got, want)
		}
	}
	if it := items["C-5"]; it.Result != worklog.ResultSkipped || it.Started != "2026-11-11T09:00:00+02:00" {
		t.Errorf("C-5 %+v", it)
	}

	clock.t = time.Date(2026, 11, 11, 18, 0, 0, 0, zone)
	if err := a.ScheduledWorklogAt(context.Background(), clock.t); err != nil {
		t.Fatal(err)
	}
	want := "N-1 1200 2026-11-11T10:00:00.000+0200|N-2 1800 2026-11-11T11:00:00.000+0200|N-3 1800 2026-11-11T12:00:00.000+0200|C-4 1500 2026-11-11T09:30:00.000+0200"
	if got := strings.Join(fj.posted, "|"); got != want {
		t.Errorf("posted\n got  %s\n want %s", got, want)
	}
	h := a.st.Snapshot().History
	if len(h) != 1 || len(h[0].Items) != 5 || !reflect.DeepEqual(h[0].Items[0].Roles, []string{"author"}) {
		t.Errorf("history %+v", h)
	}
}

// related: preview only, my other issues minus the run's items, never stored.
func TestV8RelatedIsPreviewOnly(t *testing.T) {
	dir := t.TempDir()
	fj := &fakeJira{
		issues:     issuesJSON(allDayA1),
		fieldsJSON: v8Fields,
		related: issuesJSON(
			withFields(issueJSON("A-1", "In Progress", "2026-11-01T10:00:00.000+0200"), `"assignee":`+alexJSON),
			withFields(issueJSON("X-9", "Open", "2026-11-01T10:00:00.000+0200"), `"assignee":`+alexJSON),
			withFields(issueJSON("Y-8", "Testing", "2026-11-01T10:00:00.000+0200"), `"assignee":`+alexJSON+`,"creator":`+alexJSON+`,"customfield_10302":[`+alexJSON+`]`),
			withFields(issueJSON("Z-7", "Open", "2026-11-01T10:00:00.000+0200"), `"assignee":`+bobJSON),
		),
		worklogs: map[string][]jira.Worklog{},
	}
	a := openApp(t, dir, fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	want := []worklog.RelatedItem{
		{Key: "X-9", Summary: "S X-9", Status: "Open", Roles: []string{"dev"}},
		{Key: "Y-8", Summary: "S Y-8", Status: "Testing", Roles: []string{"qa", "author"}},
	}
	if !reflect.DeepEqual(run.Related, want) {
		t.Errorf("related %+v", run.Related)
	}
	b, _ := json.Marshal(run)
	if !strings.Contains(string(b), `"related":[{"key":"X-9","summary":"S X-9","status":"Open","roles":["dev"]}`) {
		t.Errorf("run json %s", b)
	}

	res, err := a.Call(context.Background(), "worklogRun", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(worklog.Run); r.Related != nil {
		t.Errorf("manual run carries related: %+v", r.Related)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if strings.Contains(string(raw), "related") || strings.Contains(string(raw), "X-9") {
		t.Errorf("related stored: %s", raw)
	}
	// A failing related search does not fail the preview.
	fj.status = func(r *jira.Request) int {
		if strings.Contains(r.URL, "resolution") {
			return 500
		}
		return 0
	}
	if run := preview(t, a); run.Error != "" || run.Related != nil {
		t.Errorf("preview with a failing related search: %+v", run)
	}
}

// History stored before v0.8 has no roles; they are reported as [].
func TestV8OldHistoryRolesAreEmptyLists(t *testing.T) {
	dir := t.TempDir()
	old := `{"history":[{"at":"2026-11-10T18:00:00+02:00","date":"2026-11-10","trigger":"schedule","items":[{"key":"A-1","summary":"S","status":"Done","shareSeconds":60,"loggedSeconds":0,"todoSeconds":60,"duration":"1m","started":"2026-11-10T09:00:00+02:00"}],"totalSeconds":60,"otherLoggedSeconds":0,"capSeconds":28800,"total":"1m"}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	a := openApp(t, dir, &fakeJira{}, &fakeClock{t: time.Date(2026, 11, 11, 12, 0, 0, 0, zone)}, nil)
	res, err := call(t, a, "getWorklogHistory", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"roles":[]`) {
		t.Errorf("history %s", b)
	}
}

// Unknown status names are dropped from the JQL (Jira rejects the whole
// query otherwise); Jira's spelling wins; an unreadable list keeps q.
func TestKnownStatuses(t *testing.T) {
	q := RoleQuery{Statuses: []string{"in progress", "QA", "Testing", "In Review"}}
	got := KnownStatuses(q, []jira.Status{{Name: "In Progress"}, {Name: "Testing"}, {Name: "In Review"}, {Name: "Done"}})
	if strings.Join(got.Statuses, "|") != "In Progress|Testing|In Review" {
		t.Errorf("got %v", got.Statuses)
	}
	if k := KnownStatuses(q, nil); len(k.Statuses) != 4 {
		t.Errorf("nil list: %v", k.Statuses)
	}
	if got := KnownStatuses(RoleQuery{Statuses: []string{"In Testing", "for review"}}, []jira.Status{{Name: "InTesting"}, {Name: "ForReview"}}); strings.Join(got.Statuses, "|") != "InTesting|ForReview" {
		t.Errorf("spaces: %v", got.Statuses) // v0.8.4
	}
	if PastWorklogJQL("2026-11-12", KnownStatuses(q, []jira.Status{{Name: "Done"}})) != "" {
		t.Error("no known status: no past query")
	}
}

// v0.8.1: an excluded issue gets no time (also no fixed time) and the
// others get the day; putting it back restores it. Bad params are rejected.
func TestV81ExcludedIssues(t *testing.T) {
	fj := &fakeJira{
		issues:   issuesJSON(allDayA1),
		created:  issuesJSON(createdJSON("N-2", "2026-11-11T11:00:00.000+0200", alexJSON)),
		worklogs: map[string][]jira.Worklog{},
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	ctx := context.Background()
	call := func(params string) []ExcludedDTO {
		t.Helper()
		res, err := a.Call(ctx, "setWorklogExcluded", json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		return res.([]ExcludedDTO)
	}
	l := call(`{"key":"A-1","summary":"Stale task","excluded":true}`)
	if len(l) != 1 || l[0].Key != "A-1" || l[0].Summary != "Stale task" {
		t.Fatalf("list %+v", l)
	}
	if got, want := runItems(preview(t, a)), "N-2 [author] todo=900 share=0 fixed=900"; got != want {
		t.Errorf("excluded\n got  %s\n want %s", got, want)
	}
	if l := call(`{"key":"A-1","excluded":false}`); len(l) != 0 {
		t.Errorf("after re-include %+v", l)
	}
	if got := runItems(preview(t, a)); !strings.HasPrefix(got, "A-1 [dev]") {
		t.Errorf("re-included: %s", got)
	}
	res, err := a.Call(ctx, "getWorklogExcluded", nil)
	if err != nil || len(res.([]ExcludedDTO)) != 0 {
		t.Errorf("get %v %v", res, err)
	}
	for _, p := range []string{`{"key":"a-1","excluded":true}`, `{"key":"A-1"}`, `{"excluded":true}`, `{"key":"A-1","excluded":"yes"}`} {
		if _, err := a.Call(ctx, "setWorklogExcluded", json.RawMessage(p)); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v, want bad_params", p, err)
		}
	}
}

// v0.8.2: each worklog carries a short description in the comment
// language (settings; "" = the interface language), also in the preview.
func TestV82WorklogComments(t *testing.T) {
	fj := &fakeJira{
		issues:  issuesJSON(allDayA1),
		created: issuesJSON(createdJSON("N-2", "2026-11-11T11:00:00.000+0200", alexJSON)),
		commented: issuesJSON(commentsJSON("C-3", 2,
			"alex@2026-11-11T16:20:00.000+0200", "alex@2026-11-11T10:15:00.000+0200")),
		worklogs: map[string][]jira.Worklog{},
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	if c := itemsByKey(preview(t, a))["A-1"].Comment; c != "Разработка" {
		t.Errorf("preview comment %q", c)
	}
	s := a.cfg.Get()
	s.WorklogCommentLanguage = "en"
	if _, errs, err := a.cfg.Save(s, nil); err != nil || len(errs) > 0 {
		t.Fatal(errs, err)
	}
	if _, err := a.Call(context.Background(), "worklogRun", nil); err != nil {
		t.Fatal(err)
	}
	want := "A-1: Development|N-2: Issue creation|C-3: Comments (2)"
	if got := strings.Join(fj.postedComments, "|"); got != want {
		t.Errorf("posted comments\n got  %s\n want %s", got, want)
	}
}
