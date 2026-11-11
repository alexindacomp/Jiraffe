package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
)

func TestWorklogJQLMatchesSpec(t *testing.T) {
	if WorklogJQL != specWorklogJQL {
		t.Errorf("WorklogJQL\n got  %s\n want %s", WorklogJQL, specWorklogJQL)
	}
	wed := time.Date(2026, 11, 11, 23, 59, 59, 0, zone)
	widened := func(n string) string {
		return `(assignee = currentUser() OR status changed BY currentUser() DURING (startOfDay(-` + n + `d), now())) AND ` +
			`(status changed DURING (startOfDay(-` + n + `d), now()) OR statusCategory = "In Progress")`
	}
	tests := []struct {
		name           string
		effective, now time.Time
		want           string
	}{
		{"same day", wed.Add(-time.Hour), wed, specWorklogJQL},
		{"real clock earlier the same day", wed, wed.Add(-time.Hour), specWorklogJQL},
		{"one day later (just after midnight)", wed, time.Date(2026, 11, 12, 0, 0, 1, 0, zone), widened("2")},
		{"three days later", wed, time.Date(2026, 11, 14, 9, 0, 0, 0, zone), widened("4")},
		{"real clock in UTC, same local day", wed, wed.UTC(), specWorklogJQL},
	}
	for _, tc := range tests {
		if got := WorklogJQLFor(tc.effective, tc.now); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
}

func TestOtherWorklogsJQL(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	q := func(from, to string) string {
		return `worklogAuthor = currentUser() AND worklogDate >= "` + from + `" AND worklogDate <= "` + to + `"`
	}
	tests := []struct {
		day  time.Time
		want string
	}{
		{time.Date(2026, 11, 11, 18, 0, 0, 0, zone), q("2026-11-10", "2026-11-12")},
		{time.Date(2026, 12, 31, 18, 0, 0, 0, zone), q("2026-12-30", "2027-01-01")},
		{time.Date(2027, 1, 1, 0, 30, 0, 0, zone), q("2026-12-31", "2027-01-02")},
		{time.Date(2028, 3, 1, 18, 0, 0, 0, zone), q("2028-02-29", "2028-03-02")},    // leap year
		{time.Date(2026, 3, 29, 18, 0, 0, 0, kyiv), q("2026-03-28", "2026-03-30")},   // spring DST switch
		{time.Date(2026, 10, 25, 0, 30, 0, 0, kyiv), q("2026-10-24", "2026-10-26")},  // autumn DST switch
		{time.Date(2026, 11, 11, 23, 30, 0, 0, zone), q("2026-11-10", "2026-11-12")}, // the local date counts, not UTC
	}
	for _, tc := range tests {
		if got := OtherWorklogsJQL(tc.day); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.day, got, tc.want)
		}
	}
}

func TestEffectiveNow(t *testing.T) {
	due := time.Date(2026, 11, 11, 23, 59, 59, 0, zone)
	tests := []struct {
		name     string
		now, due time.Time
		want     time.Time
	}{
		{"manual run (no due)", time.Date(2026, 11, 12, 0, 0, 1, 0, zone), time.Time{}, time.Date(2026, 11, 12, 0, 0, 1, 0, zone)},
		{"same day", time.Date(2026, 11, 11, 18, 0, 5, 0, zone), time.Date(2026, 11, 11, 18, 0, 0, 0, zone), time.Date(2026, 11, 11, 18, 0, 5, 0, zone)},
		{"midnight crossed: end of the due day", time.Date(2026, 11, 12, 0, 0, 1, 0, zone), due, due},
		{"days later: end of the due day", time.Date(2026, 11, 14, 9, 0, 0, 0, zone), time.Date(2026, 11, 11, 18, 0, 0, 0, zone), due},
		{"clock went backwards over midnight", time.Date(2026, 11, 10, 23, 59, 0, 0, zone), time.Date(2026, 11, 11, 0, 0, 30, 0, zone), time.Date(2026, 11, 11, 0, 0, 30, 0, zone)},
	}
	for _, tc := range tests {
		got := effectiveNow(tc.now, tc.due)
		if !got.Equal(tc.want) || got.Location() != tc.want.Location() {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// QA-2 regression: a run found due at 23:59:59 that only executes after
// midnight computes and records the decided day.
func TestRunDueBeforeMidnightComputesTheDecidedDay(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), capDay: "2026-11-11"}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 12, 0, 0, 1, 0, zone)}, nil) // Thursday 00:00:01
	due := time.Date(2026, 11, 11, 23, 59, 59, 0, zone)                                             // decided on Wednesday
	if err := a.ScheduledWorklogAt(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fj.posted, "|"); got != "A-1 28800 2026-11-11T09:00:00.000+0200" {
		t.Errorf("posted %q", got)
	}
	if jqls := fj.searched(); len(jqls) == 0 || !strings.Contains(jqls[0], "startOfDay(-2d)") {
		t.Errorf("issue query %q", jqls)
	}
	snap := a.st.Snapshot()
	if snap.LastWorklogDate != "2026-11-11" || len(snap.History) != 1 || snap.History[0].Date != "2026-11-11" ||
		snap.History[0].At != "2026-11-12T00:00:01+02:00" || snap.History[0].TotalSeconds != 8*3600 {
		t.Errorf("last=%q history=%+v", snap.LastWorklogDate, snap.History)
	}
}

func TestScheduledRunBeforeWorklogTimeDoesNotMarkTheDay(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1)}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 17, 0, 0, 0, zone)}, func(s *config.Settings) {
		s.WorklogTime = "18:00"
	})
	if err := a.ScheduledWorklogAt(context.Background(), time.Date(2026, 11, 11, 17, 0, 0, 0, zone)); err == nil {
		t.Error("an early run must ask for another run")
	}
	if d := a.st.Snapshot().LastWorklogDate; d != "" {
		t.Errorf("LastWorklogDate %q before the worklog time", d)
	}
}

const mineToday = `{"author":{"name":"alex","key":"alex"},"started":"2026-11-11T09:00:00.000+0200","timeSpentSeconds":3600}`

func TestEmbeddedWorklogsAvoidFullFetch(t *testing.T) {
	fj := &fakeJira{
		issues: issuesJSON(allDayA1),
		capIssues: `[
		 {"key":"C-1","fields":{"worklog":{"startAt":0,"maxResults":20,"total":1,"worklogs":[` + mineToday + `]}}},
		 {"key":"P-2","fields":{"worklog":{"startAt":0,"maxResults":20,"total":25,"worklogs":[]}}}]`,
		worklogs: map[string][]jira.Worklog{
			"P-2": {
				{Author: &jira.User{Name: "alex", Key: "alex"}, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 3600},
				{Author: &jira.User{Name: "alex", Key: "alex"}, Started: "2026-11-11T11:00:00.000+0200", TimeSpentSeconds: 3600},
			},
		},
	}
	// The complete embedded list must be trusted: fetching C-1 again would fail the run.
	fj.status = func(r *jira.Request) int {
		if strings.Contains(r.URL, "/issue/C-1/worklog") {
			return 500
		}
		return 0
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	if fj.worklogFetches["P-2"] != 1 {
		t.Errorf("incomplete embedded worklogs of P-2 fetched %d times, want 1", fj.worklogFetches["P-2"])
	}
	// 1h on C-1 + 2h on P-2 → 5h left for A-1.
	if run.Error != "" || run.OtherLoggedSeconds != 3*3600 || run.TotalSeconds != 5*3600 {
		t.Errorf("error=%q other=%d total=%d", run.Error, run.OtherLoggedSeconds, run.TotalSeconds)
	}
}
