package app

import (
	"strings"
	"testing"
	"time"
)

func TestParseScopeAcceptsReported(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 13, 9, 0, 0, 0, zone))
	for _, tc := range []struct{ in, def, want string }{
		{"", ScopeMine, ScopeMine},
		{"mine", ScopeAll, ScopeMine},
		{"all", ScopeMine, ScopeAll},
		{"reported", ScopeMine, ScopeReported},
	} {
		if got, err := a.parseScope(tc.in, tc.def); err != nil || got != tc.want {
			t.Errorf("%q: %q %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"Reported", "reporter", "me"} {
		if _, err := a.parseScope(bad, ScopeMine); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReportedScopeQueries(t *testing.T) {
	got := ProjectBoardJQL(ScopeReported, "MB", "assignee = currentUser() ORDER BY updated DESC")
	want := `project = "MB" AND reporter = currentUser() AND (statusCategory != Done OR ` + recentlyDone + ")"
	if got != want {
		t.Errorf("project board:\n got %s\nwant %s", got, want)
	}
	if !strings.HasPrefix(ReportedBoardJQL, "reporter = currentUser() AND statusCategory != Done") || reportedClause != " AND reporter = currentUser()" {
		t.Errorf("tracked board %q, sprint clause %q", ReportedBoardJQL, reportedClause)
	}
	if boardCacheKey(ScopeReported, "", 0) == boardCacheKey(ScopeMine, "", 0) || sprintGroupsKey("MB", ScopeReported, false) == sprintGroupsKey("MB", ScopeMine, false) {
		t.Error("cache keys must differ per scope")
	}
}
