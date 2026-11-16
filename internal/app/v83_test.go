package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// v0.8.3: "mine" on the board and in sprints = assignee or a role field
// (Developer, QA, Reviewer); never reporter / commenter.
func TestMineCondWithRoles(t *testing.T) {
	roles := jira.FieldIDs{Developer: "customfield_10300", QA: "customfield_10302", Reviewer: "customfield_10301"}
	mine := MineCond(roles)
	if want := "(assignee = currentUser() OR cf[10300] = currentUser() OR cf[10302] = currentUser() OR cf[10301] = currentUser())"; mine != want {
		t.Errorf("MineCond %s", mine)
	}
	if got := MineCond(jira.FieldIDs{QA: "customfield_10302"}); got != "(assignee = currentUser() OR cf[10302] = currentUser())" {
		t.Errorf("QA only %s", got)
	}
	if got := MineCond(jira.FieldIDs{}); got != "assignee = currentUser()" {
		t.Errorf("no fields %s", got)
	}
	for _, tc := range []struct{ in, want string }{
		{"assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC", mine + " AND statusCategory != Done ORDER BY updated DESC"},
		{"project = MB AND Assignee=currentUser()", "project = MB AND " + mine},
		{"ASSIGNEE  =  CURRENTUSER() OR reporter = currentUser()", mine + " OR reporter = currentUser()"},
		{"reporter = currentUser()", "reporter = currentUser()"},
		{"previous_assignee = currentUser()", "previous_assignee = currentUser()"}, // another field
		{"assignee in (currentUser())", "assignee in (currentUser())"},             // other forms kept as written
	} {
		if got := WithMyRoles(tc.in, mine); got != tc.want {
			t.Errorf("WithMyRoles(%q)\n got  %s\n want %s", tc.in, got, tc.want)
		}
	}
	got := ProjectBoardJQL(ScopeMine, "MB", "", MineCond(jira.FieldIDs{QA: "customfield_10302"}))
	if want := `project = "MB" AND (assignee = currentUser() OR cf[10302] = currentUser()) AND (statusCategory != Done OR ` + recentlyDone + `)`; got != want {
		t.Errorf("project board\n got  %s\n want %s", got, want)
	}
}

// v0.8.4: the description names the status roles whose statuses had time,
// plus the fixed roles; without Worked (old callers) all roles.
func TestDescribedRoles(t *testing.T) {
	c := worklog.Candidate{Roles: []string{"qa", "reviewer", "commenter"}, Worked: []string{"qa"}}
	if got := describedRoles(c); len(got) != 2 || got[0] != "qa" || got[1] != "commenter" {
		t.Errorf("got %v", got)
	}
	c.Worked = nil
	if got := describedRoles(c); len(got) != 3 {
		t.Errorf("nil worked: %v", got)
	}
}

// v0.8.5: manual entries on issues without activity (read by key), split,
// described by activity and note, saved per day, logged first.
func TestV85ManualEntries(t *testing.T) {
	backlog := issueJSON("B-9", "Backlog", "2026-11-01T10:00:00.000+0200")
	fj := &fakeJira{
		issues:   issuesJSON(allDayA1),
		routes:   map[string]string{"key in (B-9)": issuesJSON(backlog)},
		worklogs: map[string][]jira.Worklog{},
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	ctx := context.Background()
	ov := `{"overrides":{"issues":{},"manual":[` +
		`{"id":"m1","keys":["B-9","A-1"],"seconds":3600,"activity":"grooming","note":" спринт 115 "}]}}`
	res, err := a.Call(ctx, "setWorklogOverrides", json.RawMessage(ov))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"manual":[{"id":"m1","keys":["B-9","A-1"],"seconds":3600,"activity":"grooming","note":"спринт 115"}]`) {
		t.Errorf("saved %s", b)
	}
	items := itemsByKey(preview(t, a))
	b9, a1 := items["B-9"], items["A-1"]
	if b9.ManualSeconds != 1800 || b9.TodoSeconds != 1800 || strings.Join(b9.Roles, ",") != "manual" || b9.Comment != "Груминг; спринт 115" || b9.Summary == "" {
		t.Errorf("B-9 %+v", b9)
	}
	// A-1: 30m manual first, the rest of the 8h cap is its status share.
	if a1.ManualSeconds != 1800 || a1.TodoSeconds != 8*3600-1800 || strings.Join(a1.Roles, ",") != "dev,manual" || a1.Comment != "Разработка; груминг; спринт 115" {
		t.Errorf("A-1 %+v", a1)
	}
	for _, bad := range []string{
		`{"overrides":{"issues":{},"manual":[{"id":"m1","keys":["B-9"],"seconds":90}]}}`,
		`{"overrides":{"issues":{},"manual":[{"id":"m1","keys":["B-9"],"seconds":600,"activity":"party"}]}}`,
	} {
		if _, err := a.Call(ctx, "setWorklogOverrides", json.RawMessage(bad)); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
