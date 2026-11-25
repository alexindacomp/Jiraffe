package app

import (
	"strings"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// v0.9.3: Jira lists statuses translated ("To Do" shown as "Сделать"); JQL
// rejects the translated name and the changelog keeps the original one, so
// the role statuses go by id.
func TestTranslatedStatusesGoByID(t *testing.T) {
	all := []jira.Status{
		{ID: "10500", Name: "Сделать"},     // "To Do"
		{ID: "10304", Name: "In Progress"}, // not translated
		{ID: "10100", Name: "Готово"},      // "Done"
		{ID: "10600", Name: "Готово"},      // another status shown with the same name
		{ID: "3", Name: "В работе"},
	}
	q := KnownStatuses(RoleQuery{Statuses: []string{"Сделать", "in progress", "Готово", "Unknown"}}, all)
	if !q.ByID || strings.Join(q.Statuses, ",") != "10500,10304,10100,10600" {
		t.Fatalf("query %+v", q)
	}
	jql := WorklogJQLFor(time.Now(), time.Now(), q)
	if !strings.Contains(jql, "status IN (10500, 10304, 10100, 10600)") || strings.Contains(jql, "Сделать") {
		t.Errorf("today JQL: %s", jql)
	}
	if past := PastWorklogJQL("2026-11-24", q); !strings.Contains(past, "status WAS IN (10500, 10304, 10100, 10600)") {
		t.Errorf("past JQL: %s", past)
	}

	// Time in "Сделать" is counted from the changelog ids ("To Do" there).
	s := config.Settings{InProgressStatuses: []string{"Сделать"}}
	set := roleStatuses(s, q, []string{worklog.RoleDev})
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)
	is := jira.Issue{Key: "OS-1", Fields: jira.Fields{
		Created: day.Add(9 * time.Hour).Format("2006-01-02T15:04:05.000-0700"),
		Status:  &jira.Status{ID: "10100", Name: "Готово"},
	}}
	is.Changelog = &jira.Changelog{Total: 2, Histories: []jira.History{
		{Created: day.Add(10 * time.Hour).Format("2006-01-02T15:04:05.000-0700"), Items: []jira.ChangeItem{{Field: "status", From: "10304", FromString: "In Progress", To: "10500", ToString: "To Do"}}},
		{Created: day.Add(12 * time.Hour).Format("2006-01-02T15:04:05.000-0700"), Items: []jira.ChangeItem{{Field: "status", From: "10500", FromString: "To Do", To: "10100", ToString: "Done"}}},
	}}
	if got := worklog.TotalSeconds(issueIntervals(is, set, true, day.Add(20*time.Hour))); got != 2*3600 {
		t.Errorf("seconds in Сделать by id: %v, want 7200", got)
	}
	if got := worklog.TotalSeconds(issueIntervals(is, worklog.NewStatusSet([]string{"Сделать"}), false, day.Add(20*time.Hour))); got != 0 {
		t.Errorf("by name the translated status never matches the changelog: %v", got)
	}

	// Without ids (an old or odd Jira) the names stay, as before.
	if n := KnownStatuses(RoleQuery{Statuses: []string{"In Progress"}}, []jira.Status{{Name: "In Progress"}}); n.ByID || n.Statuses[0] != "In Progress" {
		t.Errorf("no ids: %+v", n)
	}
}
