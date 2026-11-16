package app

import (
	"testing"

	"jiraffe/internal/jira"
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
