package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // Europe/Kyiv without depending on the host's zoneinfo

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

// openApp builds an App on dir (config.json + state.json), so a test can
// "restart" the app on the same files. mutate adjusts the default settings.
func openApp(t *testing.T, dir string, fj jira.Transport, clock scheduler.Clock, mutate func(*config.Settings)) *App {
	t.Helper()
	cfg, err := config.Open(filepath.Join(dir, "config.json"), plainProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Configured() {
		s := config.Defaults()
		s.JiraURL = "https://sd.local"
		if mutate != nil {
			mutate(&s)
		}
		tok := testToken
		if _, errs, err := cfg.Save(s, &tok); err != nil {
			t.Fatalf("save settings: %v %+v", err, errs)
		}
	}
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	attachClock(fj, clock)
	return New(cfg, st, &fakePlatform{t: fj}, clock, nil)
}

// issueJSON builds a search hit. changes are "2026-11-11T15:00:00.000+0200 ToDo>In Progress".
func issueJSON(key, status, created string, changes ...string) string {
	var hs []string
	for _, c := range changes {
		ts, move, _ := strings.Cut(c, " ")
		from, to, _ := strings.Cut(move, ">")
		hs = append(hs, fmt.Sprintf(`{"created":%q,"items":[{"field":"status","fromString":%q,"toString":%q}]}`, ts, from, to))
	}
	return fmt.Sprintf(`{"key":%q,"fields":{"summary":"S %s","status":{"name":%q},"created":%q},"changelog":{"total":%d,"histories":[%s]}}`,
		key, key, status, created, len(hs), strings.Join(hs, ","))
}

func issuesJSON(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

// allDayA1 is in progress since yesterday 11:00 and still open.
var allDayA1 = issueJSON("A-1", "In Progress", "2026-11-10T10:00:00.000+0200", "2026-11-10T11:00:00.000+0200 ToDo>In Progress")

func preview(t *testing.T, a *App) worklog.Run {
	t.Helper()
	res, err := a.Call(context.Background(), "worklogPreview", nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.(worklog.Run)
}

func itemsByKey(run worklog.Run) map[string]worklog.Item {
	m := map[string]worklog.Item{}
	for _, it := range run.Items {
		m[it.Key] = it
	}
	return m
}

func TestOnlyMyWorklogsFromTodayAreSubtracted(t *testing.T) {
	me := &jira.User{Name: "alex", Key: "alex"}
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{"A-1": {
		{Author: &jira.User{Name: "bob", Key: "bob", DisplayName: "Alex"}, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 2 * 3600}, // other author, same display name
		{Author: me, Started: "2026-11-10T17:00:00.000+0200", TimeSpentSeconds: 3600},                                                           // mine, yesterday
		{Author: me, Started: "2026-11-10T21:30:00.000+0000", TimeSpentSeconds: 3600},                                                           // mine, 23:30 yesterday local
		{Author: me, Started: "2026-11-10T22:30:00.000+0000", TimeSpentSeconds: 30 * 60},                                                        // mine, 00:30 today local
		{Author: &jira.User{Name: "ALEX"}, Started: "2026-11-11T10:00:00.000+0200", TimeSpentSeconds: 15 * 60},                                  // mine, old Jira without key
		{Author: nil, Started: "2026-11-11T10:00:00.000+0200", TimeSpentSeconds: 3600},                                                          // anonymous
		{Author: me, Started: "not a date", TimeSpentSeconds: 3600},                                                                             // unparseable
		{Author: me, Started: "2026-11-12T09:00:00.000+0200", TimeSpentSeconds: 3600},                                                           // tomorrow
	}}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	it := itemsByKey(preview(t, a))["A-1"]
	if it.LoggedSeconds != 45*60 || it.ShareSeconds != 8*3600 || it.TodoSeconds != 7*3600+15*60 || it.Duration != "7h 15m" {
		t.Errorf("item %+v, want logged 45m, todo 7h 15m", it)
	}
}

func TestLoggedTodayDirect(t *testing.T) {
	me := &jira.User{Key: "JIRAUSER10", Name: "alex"}
	wls := []jira.Worklog{
		{Author: &jira.User{Key: "JIRAUSER10", Name: "alex.old"}, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 60}, // renamed user: key wins
		{Author: &jira.User{Key: "JIRAUSER11", Name: "alex"}, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 120},    // namesake, other key
	}
	if got := LoggedToday(wls, me, "2026-11-11", zone); got != 60 {
		t.Errorf("got %d, want 60", got)
	}
	if got := LoggedToday(wls, nil, "2026-11-11", zone); got != 0 {
		t.Errorf("unknown me: got %d", got)
	}
}

func TestRetryAfterPartialFailureDoesNotDoubleLog(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	failB := true
	fj.status = func(r *jira.Request) int {
		if failB && r.Method == "POST" && strings.Contains(r.URL, "/issue/B-2/worklog") {
			return 500
		}
		return 0
	}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	ctx := context.Background()

	if err := a.ScheduledWorklog(ctx); err == nil {
		t.Fatal("a run with a failed item must ask the scheduler to retry")
	}
	if strings.Join(fj.posted, "|") != "A-1 25200 2026-11-11T09:00:00.000+0200" {
		t.Fatalf("posted %v", fj.posted)
	}
	snap := a.st.Snapshot()
	if snap.LastWorklogDate != "" {
		t.Error("a failed run must not mark the day as done")
	}
	if it := itemsByKey(snap.History[0])["B-2"]; it.Result != worklog.ResultError || it.Error == "" || snap.History[0].Error == "" {
		t.Errorf("history %+v", snap.History[0])
	}

	failB = false
	if err := a.ScheduledWorklog(ctx); err != nil {
		t.Fatal(err)
	}
	want := "A-1 25200 2026-11-11T09:00:00.000+0200|B-2 3600 2026-11-11T15:00:00.000+0200"
	if strings.Join(fj.posted, "|") != want {
		t.Fatalf("retry posted %v, want %s", fj.posted, want)
	}
	if a.st.Snapshot().LastWorklogDate != "2026-11-11" {
		t.Error("successful retry must mark the day as done")
	}
}

func TestLostResponseIsNotLoggedTwiceOnRetry(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	tr := &lostResponse{fakeJira: fj}
	dir := t.TempDir()
	a := openApp(t, dir, tr, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	ctx := context.Background()
	if err := a.ScheduledWorklog(ctx); err == nil {
		t.Fatal("expected a retryable failure")
	}
	if err := a.ScheduledWorklog(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fj.posted) != 1 {
		t.Fatalf("the worklog was posted twice: %v", fj.posted)
	}
	run := a.st.Snapshot().History[0]
	if run.TotalSeconds != 0 || itemsByKey(run)["A-1"].LoggedSeconds != 8*3600 {
		t.Errorf("retry run %+v", run)
	}
}

func TestManualRunThenScheduledRunCompletesTheDay(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	clock := &fakeClock{t: time.Date(2026, 11, 11, 16, 0, 0, 0, zone)}
	a := openApp(t, t.TempDir(), fj, clock, nil)
	ctx := context.Background()

	if _, err := a.Call(ctx, "worklogRun", nil); err != nil {
		t.Fatal(err)
	}
	if a.st.Snapshot().LastWorklogDate != "" {
		t.Fatal("a manual run must not replace the scheduled one")
	}
	clock.t = time.Date(2026, 11, 11, 18, 0, 0, 0, zone)
	a.sched.Tick(ctx)
	// Documented behaviour: both entries start at the first in-progress moment (09:00),
	// as in the Python reference; together they never exceed the cap.
	want := "A-1 21600 2026-11-11T09:00:00.000+0200|A-1 7200 2026-11-11T09:00:00.000+0200"
	if strings.Join(fj.posted, "|") != want {
		t.Fatalf("posted %v, want %s", fj.posted, want)
	}
	if a.st.Snapshot().LastWorklogDate != "2026-11-11" {
		t.Error("scheduled run did not mark the day")
	}
	clock.t = time.Date(2026, 11, 11, 18, 30, 0, 0, zone)
	a.sched.Tick(ctx)
	if res, err := a.Call(ctx, "worklogRun", nil); err != nil || res.(worklog.Run).TotalSeconds != 0 {
		t.Fatalf("manual run after the day is complete: %+v %v", res, err)
	}
	if len(fj.posted) != 2 {
		t.Fatalf("logged again: %v", fj.posted)
	}
}

func TestRestartAfterScheduledRunDoesNotRunAgain(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}
	a := openApp(t, dir, fj, clock, nil)
	a.sched.Tick(context.Background())
	if len(fj.posted) != 1 {
		t.Fatalf("posted %v", fj.posted)
	}
	a.Stop(time.Second)

	// Restart later the same evening on the same files.
	clock2 := &fakeClock{t: time.Date(2026, 11, 11, 20, 0, 0, 0, zone)}
	b := openApp(t, dir, fj, clock2, nil)
	before := fj.searches
	b.sched.Tick(context.Background())
	if len(fj.posted) != 1 {
		t.Fatalf("restarted app logged again: %v", fj.posted)
	}
	if st := b.State(); st.NextWorklogAt != "2026-11-12T18:00:00+02:00" || st.LastWorklog == nil || st.LastWorklog.TotalSeconds != 8*3600 {
		t.Errorf("state after restart %+v", st)
	}
	// Only the comment poll may have searched; the worklog search did not run.
	if fj.searches-before > 1 {
		t.Errorf("%d searches after restart", fj.searches-before)
	}
}

func TestWorklogRunTiming(t *testing.T) {
	tests := []struct {
		name     string
		at       time.Time
		total    int64
		searched bool
	}{
		{"before workday start: nothing, no Jira calls", time.Date(2026, 11, 11, 8, 0, 0, 0, zone), 0, false},
		{"during lunch: morning only", time.Date(2026, 11, 11, 13, 30, 0, 0, zone), 4 * 3600, true},
		{"after workday end: clipped at 18:00", time.Date(2026, 11, 11, 21, 0, 0, 0, zone), 8 * 3600, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
			a, _, _ := newTestApp(t, fj, tc.at)
			run := preview(t, a)
			if run.TotalSeconds != tc.total || run.Error != "" || run.SkippedReason != "" || run.Date != "2026-11-11" {
				t.Errorf("run %+v", run)
			}
			if (fj.searches > 0) != tc.searched {
				t.Errorf("searches=%d", fj.searches)
			}
			if len(fj.posted) != 0 {
				t.Errorf("preview wrote to Jira: %v", fj.posted)
			}
		})
	}
}

func TestCapCountsAllMyWorklogsOfTheDay(t *testing.T) {
	// The daily cap covers ALL my worklogs started today. X-9 is only found by
	// the "my worklogs" search (never in progress today); its embedded list is
	// incomplete, so its worklogs are fetched. A-1 is a candidate and also
	// appears there: its 1h must not be counted twice.
	mine := &jira.User{Name: "alex", Key: "alex"}
	fj := &fakeJira{
		issues: issuesJSON(allDayA1),
		capIssues: `[
		 {"key":"A-1","fields":{"worklog":{"startAt":0,"maxResults":20,"total":1,"worklogs":[` + mineToday + `]}}},
		 {"key":"X-9","fields":{"worklog":{"startAt":0,"maxResults":20,"total":21,"worklogs":[]}}}]`,
		worklogs: map[string][]jira.Worklog{
			"A-1": {{Author: mine, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 3600}},
			"X-9": {
				{Author: mine, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 6 * 3600},
				{Author: mine, Started: "2026-11-10T09:00:00.000+0200", TimeSpentSeconds: 3 * 3600},                                // yesterday
				{Author: &jira.User{Name: "bob", Key: "bob"}, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 3 * 3600}, // not mine
			},
		},
	}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	run := preview(t, a)
	items := itemsByKey(run)
	if _, ok := items["X-9"]; ok || len(run.Items) != 1 {
		t.Errorf("issue never in progress today must not be an item: %+v", run.Items)
	}
	// 8h cap − 6h on X-9 − 1h already on A-1 = 1h.
	if items["A-1"].LoggedSeconds != 3600 || items["A-1"].TodoSeconds != 3600 || run.TotalSeconds != 3600 {
		t.Errorf("A-1 %+v total %d, want logged 1h, todo 1h", items["A-1"], run.TotalSeconds)
	}
	if run.OtherLoggedSeconds != 6*3600 || fj.worklogFetches["X-9"] != 1 {
		t.Errorf("otherLoggedSeconds %d (want 6h), X-9 fetched %d times", run.OtherLoggedSeconds, fj.worklogFetches["X-9"])
	}
}

// QR-3: if what I already logged cannot be read, the run must fail instead
// of assuming 0 and logging the day twice.
func TestWorklogFetchFailureFailsTheRunWithoutPosting(t *testing.T) {
	mine := &jira.User{Name: "alex", Key: "alex"}
	tests := []struct {
		name      string
		capIssues string
		worklogs  map[string][]jira.Worklog
		failKey   string
		capCheck  bool
	}{
		{
			name:     "candidate issue",
			worklogs: map[string][]jira.Worklog{"A-1": {{Author: mine, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 8 * 3600}}},
			failKey:  "A-1",
		},
		{
			name:      "issue found by the cap search (embedded list incomplete)",
			capIssues: `[{"key":"X-9","fields":{"worklog":{"startAt":0,"maxResults":20,"total":21,"worklogs":[]}}}]`,
			worklogs:  map[string][]jira.Worklog{"X-9": {{Author: mine, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 6 * 3600}}},
			failKey:   "X-9",
			capCheck:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJira{issues: issuesJSON(allDayA1), capIssues: tc.capIssues, worklogs: tc.worklogs}
			fj.status = func(r *jira.Request) int {
				if r.Method == "GET" && strings.Contains(r.URL, "/issue/"+tc.failKey+"/worklog") {
					return 500
				}
				return 0
			}
			a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
			err := a.ScheduledWorklog(context.Background())
			if err == nil {
				t.Fatal("the scheduler must be asked to retry")
			}
			if len(fj.posted) != 0 {
				t.Fatalf("posted %v although my logged time is unknown", fj.posted)
			}
			snap := a.st.Snapshot()
			if snap.LastWorklogDate != "" || len(snap.History) != 1 || snap.History[0].Error == "" {
				t.Fatalf("last=%q history=%+v", snap.LastWorklogDate, snap.History)
			}
			if prefix := T(a.Language(), "err_cap_check"); strings.HasPrefix(snap.History[0].Error, prefix) != tc.capCheck {
				t.Errorf("error %q (cap check prefix expected: %v)", snap.History[0].Error, tc.capCheck)
			}
		})
	}
}

func TestPausedSuppressesScheduledBalloonButNotTrayRun(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1)}
	a, plat, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	a.SetPaused(true)
	if err := a.ScheduledWorklog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fj.posted) != 1 || len(plat.notices) != 0 {
		t.Fatalf("paused scheduled run: posted %v, balloons %+v", fj.posted, plat.notices)
	}
	a.ManualWorklogFromTray() // the user asked: always answer, even when paused
	a.bgWG.Wait()
	lang := a.Language()
	if len(plat.notices) != 1 || plat.notices[0].title != T(lang, "worklogTitle") || plat.notices[0].text != T(lang, "worklogNothing") {
		t.Errorf("tray run balloons %+v", plat.notices)
	}
}

func TestCapCheckFailureFailsTheRun(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	fj.status = func(r *jira.Request) int {
		if strings.Contains(r.URL, "worklogAuthor") {
			return 500
		}
		return 0
	}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	_, err := a.Call(context.Background(), "worklogRun", nil)
	if err == nil || len(fj.posted) != 0 {
		t.Fatalf("run must fail without logging: err=%v posted=%v", err, fj.posted)
	}
	if e := a.ErrorFor(err); !strings.HasPrefix(e.Message, T(a.Language(), "err_cap_check")) || !strings.Contains(e.Message, "HTTP 500") {
		t.Errorf("message %q", e.Message)
	}
}

func TestCustomInProgressStatuses(t *testing.T) {
	doing := issueJSON("D-1", "DOING", "2026-11-10T10:00:00.000+0200", "2026-11-10T11:00:00.000+0200 ToDo>Doing")
	fj := &fakeJira{issues: issuesJSON(allDayA1, doing), worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, func(s *config.Settings) {
		s.InProgressStatuses = []string{"Doing"}
	})
	items := itemsByKey(preview(t, a))
	if len(items) != 1 || items["D-1"].TodoSeconds != 8*3600 {
		t.Errorf("items %+v", items)
	}
}

func TestDSTSwitchDaysInKyiv(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	everyDay := func(s *config.Settings) { s.WorkDays = []int{1, 2, 3, 4, 5, 6, 7} }
	tests := []struct {
		name   string
		now    time.Time
		issues string
		mine   []jira.Worklog
		posted []string
	}{
		{
			name: "spring forward (Sunday 2026-03-29)",
			now:  time.Date(2026, 3, 29, 18, 0, 0, 0, kyiv),
			issues: issuesJSON(
				issueJSON("A-1", "In Progress", "2026-03-27T10:00:00.000+0200", "2026-03-28T11:00:00.000+0200 ToDo>In Progress"),
				// B in progress 10:00-12:00 EEST, the second change reported in UTC
				issueJSON("B-2", "ForReview", "2026-03-27T10:00:00.000+0200", "2026-03-29T10:00:00.000+0300 ToDo>В работе", "2026-03-29T09:00:00.000Z В работе>ForReview"),
			),
			posted: []string{"A-1 25200 2026-03-29T09:00:00.000+0300", "B-2 3600 2026-03-29T10:00:00.000+0300"},
		},
		{
			name:   "fall back (Sunday 2026-10-25)",
			now:    time.Date(2026, 10, 25, 18, 0, 0, 0, kyiv),
			issues: issuesJSON(issueJSON("A-1", "In Progress", "2026-10-23T10:00:00.000+0300", "2026-10-24T11:00:00.000+0300 ToDo>In Progress")),
			// 30m logged earlier today, reported by the server in UTC
			mine:   []jira.Worklog{{Author: &jira.User{Name: "alex", Key: "alex"}, Started: "2026-10-25T07:00:00.000+0000", TimeSpentSeconds: 1800}},
			posted: []string{"A-1 27000 2026-10-25T09:00:00.000+0200"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJira{issues: tc.issues, worklogs: map[string][]jira.Worklog{"A-1": tc.mine}}
			a := openApp(t, t.TempDir(), fj, &fakeClock{t: tc.now}, everyDay)
			res, err := a.Call(context.Background(), "worklogRun", nil)
			if err != nil {
				t.Fatal(err)
			}
			if r := res.(worklog.Run); r.SkippedReason != "" || r.Error != "" {
				t.Fatalf("run %+v", r)
			}
			if strings.Join(fj.posted, "|") != strings.Join(tc.posted, "|") {
				t.Errorf("posted %v\nwant   %v", fj.posted, tc.posted)
			}
		})
	}
}

func TestWorklogItemJSONContract(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	b, err := json.Marshal(preview(t, a))
	if err != nil {
		t.Fatal(err)
	}
	var run struct {
		At, Date, Trigger, Total string
		TotalSeconds             int64
		Items                    []map[string]any
	}
	if err := json.Unmarshal(b, &run); err != nil {
		t.Fatal(err)
	}
	if run.Trigger != "preview" || run.Date != "2026-11-11" || run.At != "2026-11-11T18:00:00+02:00" || run.Total != "8h" || len(run.Items) != 2 {
		t.Fatalf("run %s", b)
	}
	it := run.Items[1]
	for k, v := range map[string]any{"key": "B-2", "status": "ForReview", "shareSeconds": 3600.0, "loggedSeconds": 0.0, "todoSeconds": 3600.0, "duration": "1h", "started": "2026-11-11T15:00:00+02:00"} {
		if it[k] != v {
			t.Errorf("%s = %v, want %v (item %v)", k, it[k], v, it)
		}
	}
	if _, ok := it["result"]; ok {
		t.Errorf("preview items carry no result: %v", it)
	}
}
