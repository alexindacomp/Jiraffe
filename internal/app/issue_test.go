package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const detailRBI12 = `{"id":"5012","key":"RBI-12","fields":{
 "summary":"Load data","status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},
 "issuetype":{"id":"3","name":"Задача","subtask":false},"priority":{"name":"High"},
 "created":"2026-11-01T10:00:00.000+0200","updated":"2026-11-10T12:30:00.000+0200",
 "assignee":{"name":"alex","key":"alex","displayName":"Alex"},"reporter":{"name":"bob","key":"bob","displayName":"Bob"},
 "creator":{"name":"carol","key":"carol","displayName":"Carol"},"project":{"key":"RBI"},
 "description":"h1. Title\n*bold* [~bob]","duedate":"2026-11-20","resolution":null,
 "labels":["etl","q4"],"components":[{"name":"API"}],"fixVersions":[{"name":"1.2"}],
 "timetracking":{"originalEstimate":"1d","originalEstimateSeconds":32400,"remainingEstimateSeconds":5400,"timeSpentSeconds":162000},
 "issuelinks":[
  {"id":"1","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"outwardIssue":` + `{"key":"RBI-13","fields":{"summary":"S","status":{"id":"1","name":"ToDo","statusCategory":{"key":"new"}},"issuetype":{"id":"3","name":"Задача"}}}},
  {"id":"2","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":` + `{"key":"RBI-14","fields":{"summary":"S","status":{"id":"10","name":"Готово","statusCategory":{"key":"done"}},"issuetype":{"id":"3","name":"Задача"}}}},
  {"id":"3","type":{"name":"Relates","inward":"relates to","outward":"relates to"},"inwardIssue":` + `{"key":"RBI-10","fields":{"summary":"Story","status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},"issuetype":{"id":"10001","name":"История"}}}}],
 "subtasks":[{"key":"RBI-15","fields":{"summary":"Sub","status":{"id":"1","name":"ToDo","statusCategory":{"key":"new"}},"issuetype":{"id":"5","name":"Подзадача","subtask":true}}}],
 "comment":{"startAt":0,"maxResults":1,"total":3,"comments":[{"id":"1","body":"only the first"}]},
 "watches":{"watchCount":4},
 "customfield_10103":"RBI-1","customfield_10130":5,
 "customfield_10107":["com.atlassian.greenhopper.service.sprint.Sprint@1f[id=6338,rapidViewId=880,state=ACTIVE,name=BI Sprint 7,startDate=2026-11-03T10:00:00.000+02:00,endDate=2026-11-17T10:00:00.000+02:00,completeDate=<null>,sequence=6338,goal=Ship it]"]},
 "changelog":{"startAt":0,"maxResults":2,"total":2,"histories":[
  {"id":"100","author":{"name":"bob","displayName":"Bob"},"created":"2026-11-02T10:00:00.000+0200","items":[{"field":"status","fromString":"ToDo","toString":"In Progress","from":"1","to":"3"}]},
  {"id":"101","author":{"name":"alex","displayName":"Alex"},"created":"2026-11-03T10:00:00.000+0200","items":[
    {"field":"assignee","from":"bob","fromString":null,"to":"alex","toString":"Alex"},{"field":"Story Points","fromString":null,"toString":"5"}]}]}}`

// sameInstant compares RFC 3339 times (the app renders them in local time).
func sameInstant(got, want string) bool {
	g, err1 := time.Parse(time.RFC3339, got)
	w, err2 := time.Parse(time.RFC3339, want)
	return err1 == nil && err2 == nil && g.Equal(w)
}

func getIssueT(t *testing.T, a *App, key string) *IssueDetail {
	t.Helper()
	res, err := a.Call(context.Background(), "getIssue", json.RawMessage(`{"key":"`+key+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return res.(*IssueDetail)
}

func TestGetIssueMapping(t *testing.T) {
	fj := &fakeJira{
		issueBodies: map[string]string{"RBI-12": detailRBI12},
		comments2:   map[string]int{"RBI-12": 3},
		byKey:       fxPool(fxRBI1),
	}
	a := v2App(t, fj, []string{"RBI"})
	d := getIssueT(t, a, "RBI-12")

	is := d.Issue
	if is.Key != "RBI-12" || is.ID != "5012" || !is.Mine || is.Type.Kind != KindTask || is.StoryPoints == nil || *is.StoryPoints != 5 ||
		is.Epic == nil || *is.Epic != (EpicChipDTO{Key: "RBI-1", Name: "Platform", Color: "ghx-label-4"}) {
		t.Errorf("issue %+v", is)
	}
	// Relation as seen from this issue: outward ⇒ type.outward, inward ⇒ type.inward.
	var links []string
	for _, l := range d.Links {
		links = append(links, l.Relation+" "+l.Issue.Key+"("+l.Issue.Type.Kind+")")
	}
	if got := strings.Join(links, "; "); got != "blocks RBI-13(task); is blocked by RBI-14(task); relates to RBI-10(story)" {
		t.Errorf("links %s", got)
	}
	if refKeys(d.Stories) != "RBI-10" || refKeys(d.Children) != "RBI-15" || d.Children[0].Type.Kind != KindSubtask || d.Parent != nil {
		t.Errorf("stories %s children %s parent %+v", refKeys(d.Stories), refKeys(d.Children), d.Parent)
	}
	// Time tracking formatted with hoursPerDay = 9: 9h = 1d, 45h = 1w.
	tt := d.TimeTracking
	if tt.Original != "1d" || tt.Remaining != "1h 30m" || tt.Spent != "1w" ||
		*tt.OriginalSeconds != 32400 || *tt.RemainingSeconds != 5400 || *tt.SpentSeconds != 162000 {
		t.Errorf("time tracking %+v", tt)
	}
	if d.Epic == nil || d.Epic.Key != "RBI-1" || d.Epic.Name != "Platform" || d.Epic.Color != "ghx-label-4" || d.Epic.Type.Kind != KindEpic {
		t.Errorf("epic %+v", d.Epic)
	}
	if len(d.Sprints) != 1 || !sameInstant(d.Sprints[0].StartDate, "2026-11-03T10:00:00+02:00") || !sameInstant(d.Sprints[0].EndDate, "2026-11-17T10:00:00+02:00") {
		t.Fatalf("sprints %+v", d.Sprints)
	}
	if sp := d.Sprints[0]; sp.ID != 6338 || sp.Name != "BI Sprint 7" || sp.State != "active" || sp.Goal != "Ship it" || sp.BoardID != 880 || sp.CompleteDate != "" {
		t.Errorf("sprint %+v", sp)
	}
	// All comments (the embedded page was incomplete), oldest first.
	if len(d.Comments) != 3 || d.Comments[0].ID != "1" || d.Comments[2].ID != "3" || d.Comments[0].Author.DisplayName != "Bob" {
		t.Errorf("comments %+v", d.Comments)
	}
	if len(d.Changelog) != 2 || d.Changelog[0].ID != "101" || d.Changelog[1].ID != "100" ||
		d.Changelog[0].Items[0] != (ChangeItemDTO{Field: "assignee", From: "bob", To: "Alex"}) ||
		d.Changelog[0].Items[1] != (ChangeItemDTO{Field: "Story Points", From: "", To: "5"}) {
		t.Errorf("changelog %+v", d.Changelog)
	}
	if d.Description != "h1. Title\n*bold* [~bob]" || d.Reporter.Name != "bob" || d.Creator.DisplayName != "Carol" ||
		d.DueDate != "2026-11-20" || d.Resolution != "" || d.Priority.Name != "High" || *d.Watchers != 4 ||
		strings.Join(d.Labels, ",") != "etl,q4" || strings.Join(d.Components, ",") != "API" || strings.Join(d.FixVersions, ",") != "1.2" ||
		!sameInstant(d.Created, "2026-11-01T10:00:00+02:00") {
		t.Errorf("detail %+v", d)
	}
	js, _ := json.Marshal(d)
	for _, want := range []string{`"timeTracking":{"originalSeconds":32400,`, `"original":"1d"`, `"links":[{"relation":"blocks","issue":{`,
		`"changelog":[{"id":"101"`, `"sprints":[{"id":6338`, `"watchers":4`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s: %s", want, js)
		}
	}
	if _, err := a.Call(context.Background(), "getIssue", json.RawMessage(`{"key":"../../x"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("bad key: %v", err)
	}
}

func TestGetIssueChildrenOfEpicAndStory(t *testing.T) {
	epic := strings.Replace(fxRBI1.json(), `"fields":{`, `"changelog":{"histories":[]},"fields":{`, 1)
	story := strings.Replace(fxRBI10.json(), `"fields":{`, `"changelog":{"histories":[]},"fields":{`, 1)
	fj := &fakeJira{
		issueBodies: map[string]string{"RBI-1": epic, "RBI-10": story},
		routes:      map[string]string{`cf[10103] = "RBI-1" ORDER BY key DESC`: fxList(fxRBI20, fxRBI12, fxRBI10)},
	}
	a := v2App(t, fj, []string{"RBI"})
	e := getIssueT(t, a, "RBI-1")
	if e.Issue.Type.Kind != KindEpic || refKeys(e.Children) != "RBI-20,RBI-12,RBI-10" || e.Epic != nil {
		t.Errorf("epic children %s (%+v)", refKeys(e.Children), e.Issue.Type)
	}
	s := getIssueT(t, a, "RBI-10")
	// Story: sub-tasks + linked issues that are neither epics nor stories.
	if s.Issue.Type.Kind != KindStory || refKeys(s.Children) != "RBI-11,RBI-12" || len(s.Stories) != 0 || len(s.Links) != 2 {
		t.Errorf("story children %s stories %s links %+v", refKeys(s.Children), refKeys(s.Stories), s.Links)
	}
	if s.Epic == nil || s.Epic.Key != "RBI-1" {
		t.Errorf("story epic %+v", s.Epic)
	}
}
