package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

// specPastJQL is the past-day issue query written out from SPEC v0.7.1 §2
// for the default in-progress statuses.
func specPastJQL(date string) string {
	during := `DURING ("` + date + ` 00:00", "` + date + ` 23:59")`
	return `status WAS IN ("In Progress", "В работе", "В роботі") ` + during +
		` AND (assignee WAS currentUser() ` + during + ` OR status CHANGED BY currentUser() ` + during + `)`
}

var specPastJQLRE = regexp.MustCompile(`^status WAS IN \("In Progress", "В работе", "В роботі"\) DURING \("(\d{4}-\d\d-\d\d) 00:00", "\d{4}-\d\d-\d\d 23:59"\) AND `)

// pastDays serves the past-day query per date (and the cap query of that
// day) from a fakeJira; any other request falls through to the fake.
type pastDays struct {
	byDate map[string]string // date → issues JSON
	asked  []string          // dates searched, in order
}

func (p *pastDays) install(fj *fakeJira) {
	fj.handle = func(r *jira.Request, u *url.URL) (*jira.Response, bool) {
		if u.Path != "/rest/api/2/search" {
			return nil, false
		}
		q := u.Query()
		jql := q.Get("jql")
		var list []json.RawMessage
		switch m := specPastJQLRE.FindStringSubmatch(jql); {
		case m != nil:
			if jql != specPastJQL(m[1]) {
				return &jira.Response{StatusCode: 400, Body: []byte(`{"errorMessages":["unexpected jql"]}`)}, true
			}
			p.asked = append(p.asked, m[1])
			list = rawList(p.byDate[m[1]])
		case specCapJQL.MatchString(jql):
			list = nil // nothing logged on other issues
		default:
			return nil, false
		}
		sel := make([]json.RawMessage, 0, len(list))
		for _, is := range list {
			sel = append(sel, projectFields(is, q.Get("fields")))
		}
		page, _ := json.Marshal(sel)
		return &jira.Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":0,"maxResults":%d,"total":%d,"issues":%s}`, len(sel), len(sel), page))}, true
	}
}

type missRecorder struct {
	mu     sync.Mutex
	events []string
	missed []MissedDaysDTO
}

func (r *missRecorder) emit(typ string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, typ)
	if m, ok := payload.(MissedDaysDTO); ok {
		r.missed = append(r.missed, m)
	}
}

func (r *missRecorder) count(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == typ {
			n++
		}
	}
	return n
}

// newV71App builds an app on dir with an event missRecorder; it is "started"
// (catch-up enabled) without the background loops.
func newV71App(t *testing.T, dir string, fj *fakeJira, now time.Time, mutate func(*config.Settings)) (*App, *fakePlatform, *fakeClock, *missRecorder) {
	t.Helper()
	cfg, err := config.Open(filepath.Join(dir, "config.json"), plainProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Configured() {
		s := config.Defaults()
		s.JiraURL = "https://sd.local"
		s.LoadFactor = 0.7
		s.Language = "en"
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
	clock := &fakeClock{t: now}
	attachClock(fj, clock)
	plat := &fakePlatform{t: fj}
	rec := &missRecorder{}
	a := New(cfg, st, plat, clock, rec.emit)
	a.started.Store(true)
	// As if the first Jira call had succeeded: the check then runs only
	// when a test calls CatchUp (no background check racing the test).
	a.jiraOK.Store(true)
	return a, plat, clock, rec
}

// rbi1028 mirrors the real issue: In Progress from 2026-11-09 14:24:50 to
// 2026-11-12 17:36:51 (+02:00), then ForReview.
var rbi1028 = issueJSON("RBI-1028", "ForReview", "2026-11-03T10:00:00.000+0200",
	"2026-11-09T14:24:50.000+0200 Open>In Progress",
	"2026-11-12T17:36:51.000+0200 In Progress>ForReview")

var me = &jira.User{Name: "alex", Key: "alex"}

func TestPastWorklogJQL(t *testing.T) {
	if got, want := PastWorklogJQL("2026-11-12", []string{"In Progress", "В работе", "В роботі"}), specPastJQL("2026-11-12"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// trimmed, de-duplicated case-insensitively, quotes and backslashes escaped
	got := PastWorklogJQL("2026-11-12", []string{" Doing ", "doing", `Say "hi"`, `a\b`, ""})
	if !strings.HasPrefix(got, `status WAS IN ("Doing", "Say \"hi\"", "a\\b") DURING`) {
		t.Errorf("got %s", got)
	}
	if PastWorklogJQL("2026-11-12", []string{" ", ""}) != "" {
		t.Error("no statuses must give no query")
	}
}

// The real day of the incident: the preview for 12 Nov finds RBI-1028
// (7h 36m 51s in progress within the workday), the 5h 36m already logged
// fill the cap of 8h × 0.7: nothing to add.
func TestPastDayPreviewMirrorsTheRealIssue(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{"RBI-1028": {
		{ID: "443171", Author: me, Started: "2026-11-12T09:00:00.000+0200", TimeSpentSeconds: 20160},
	}}}
	days := &pastDays{byDate: map[string]string{"2026-11-12": issuesJSON(rbi1028)}}
	days.install(fj)
	a, _, _, _ := newV71App(t, t.TempDir(), fj, time.Date(2026, 11, 13, 14, 0, 0, 0, zone), nil)
	res, err := a.Call(context.Background(), "worklogPreview", json.RawMessage(`{"date":"2026-11-12"}`))
	if err != nil {
		t.Fatal(err)
	}
	run := res.(worklog.Run)
	it := itemsByKey(run)["RBI-1028"]
	if run.Date != "2026-11-12" || run.CapSeconds != 20160 || run.TotalSeconds != 0 || run.Total != "0m" {
		t.Fatalf("run %+v", run)
	}
	if it.ShareSeconds != 27411 || it.LoggedSeconds != 20160 || it.TodoSeconds != 0 || it.Result != worklog.ResultSkipped || it.Started != "2026-11-12T09:00:00+02:00" {
		t.Errorf("item %+v", it)
	}
	if len(fj.posted) != 0 {
		t.Errorf("preview posted %v", fj.posted)
	}
}

// A past day with nothing logged: the run logs only up to the cap, started
// on that day; re-running adds nothing.
func TestPastDayRunLogsOnlyTheMissingPart(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{"RBI-1028": {
		{Author: me, Started: "2026-11-12T09:00:00.000+0200", TimeSpentSeconds: 3600}, // 1h by hand
		{Author: me, Started: "2026-11-11T09:00:00.000+0200", TimeSpentSeconds: 3600}, // another day
	}}}
	days := &pastDays{byDate: map[string]string{"2026-11-12": issuesJSON(rbi1028)}}
	days.install(fj)
	a, _, _, _ := newV71App(t, t.TempDir(), fj, time.Date(2026, 11, 13, 14, 0, 0, 0, zone), nil)
	ctx := context.Background()
	res, err := a.Call(ctx, "worklogRun", json.RawMessage(`{"date":"2026-11-12"}`))
	if err != nil {
		t.Fatal(err)
	}
	run := res.(worklog.Run)
	// cap 5h 36m − 1h logged = 4h 36m = 16560 s
	if want := []string{"RBI-1028 16560 2026-11-12T09:00:00.000+0200"}; strings.Join(fj.posted, "|") != strings.Join(want, "|") {
		t.Fatalf("posted %v, want %v", fj.posted, want)
	}
	if run.Trigger != worklog.TriggerManual || run.Date != "2026-11-12" || run.TotalSeconds != 16560 {
		t.Errorf("run %+v", run)
	}
	h := a.st.Snapshot()
	if len(h.History) != 1 || h.History[0].Date != "2026-11-12" || h.LastWorklogDate != "" {
		t.Errorf("history %+v, lastWorklogDate %q (a past run must not mark today)", h.History, h.LastWorklogDate)
	}
	if _, err := a.Call(ctx, "worklogRun", json.RawMessage(`{"date":"2026-11-12"}`)); err != nil {
		t.Fatal(err)
	}
	if len(fj.posted) != 1 {
		t.Errorf("second run posted again: %v", fj.posted)
	}
}

func TestRunDateValidation(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues}
	a, _, _, _ := newV71App(t, t.TempDir(), fj, time.Date(2026, 11, 13, 14, 0, 0, 0, zone), nil)
	for _, bad := range []string{"2026-11-14", "2026-10-13", "2026-11-31", "2026-02-30", "13.11.2026", "2026-11-1", " 2026-11-12", "2026-11-12T00:00:00Z"} {
		for _, m := range []string{"worklogPreview", "worklogRun", "skipWorklogDay"} {
			_, err := a.Call(context.Background(), m, json.RawMessage(`{"date":"`+bad+`"}`))
			if e, ok := err.(*Error); !ok || e.Code != CodeBadParams {
				t.Errorf("%s %q: %v, want bad_params", m, bad, err)
			}
		}
	}
	if _, err := a.Call(context.Background(), "worklogPreview", json.RawMessage(`{"date":42}`)); err == nil {
		t.Error("a non-string date must be rejected")
	}
	// 30 days back is allowed; today is the normal run for today.
	pd := &pastDays{byDate: map[string]string{}}
	pd.install(fj)
	if _, err := a.Call(context.Background(), "worklogPreview", json.RawMessage(`{"date":"2026-10-14"}`)); err != nil {
		t.Errorf("30 days back: %v", err)
	}
	fj.jqls = nil
	res, err := a.Call(context.Background(), "worklogPreview", json.RawMessage(`{"date":"2026-11-13"}`))
	if err != nil {
		t.Fatal(err)
	}
	if run := res.(worklog.Run); run.Date != "2026-11-13" || len(fj.searched()) == 0 || fj.searched()[0] != specWorklogJQL {
		t.Errorf("today's date must use today's query: %v", fj.searched())
	}
}

// A past non-working day is computed on request, and says so.
func TestPastNonWorkingDayIsComputedAndFlagged(t *testing.T) {
	fj := &fakeJira{}
	sat := issueJSON("A-1", "In Progress", "2026-11-06T10:00:00.000+0200", "2026-11-07T10:00:00.000+0200 Open>In Progress")
	(&pastDays{byDate: map[string]string{"2026-11-07": issuesJSON(sat)}}).install(fj)
	a, _, _, _ := newV71App(t, t.TempDir(), fj, time.Date(2026, 11, 13, 14, 0, 0, 0, zone), nil)
	res, err := a.Call(context.Background(), "worklogPreview", json.RawMessage(`{"date":"2026-11-07"}`))
	if err != nil {
		t.Fatal(err)
	}
	run := res.(worklog.Run)
	if run.NonWorkingDay != worklog.ReasonWeekend || run.SkippedReason != "" || run.TotalSeconds != 20160 { // 7h capped at 5h 36m
		t.Errorf("run %+v", run)
	}
}

func TestCandidateDays(t *testing.T) {
	fri := time.Date(2026, 11, 13, 9, 0, 0, 0, zone) // Friday
	cal := worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5}}
	tests := []struct {
		name    string
		now     time.Time
		cal     worklog.Calendar
		first   string
		handled map[string]bool
		want    string
	}{
		{"last 7 days, weekend skipped", fri, cal, "2026-01-01", nil, "2026-11-06 2026-11-09 2026-11-10 2026-11-11 2026-11-12"},
		{"monday looks back over the weekend", time.Date(2026, 11, 16, 9, 0, 0, 0, zone), cal, "2026-01-01", nil, "2026-11-09 2026-11-10 2026-11-11 2026-11-12 2026-11-13"},
		{"not before the first run", fri, cal, "2026-11-11", nil, "2026-11-11 2026-11-12"},
		{"first run today: nothing", fri, cal, "2026-11-13", nil, ""},
		{"handled days", fri, cal, "2026-01-01", map[string]bool{"2026-11-12": true, "2026-11-06": true}, "2026-11-09 2026-11-10 2026-11-11"},
		{"holiday and vacation", fri, worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5}, Holidays: []string{"2026-11-10"},
			Vacations: []worklog.DateRange{{From: "2026-11-05", To: "2026-11-09"}}}, "2026-01-01", nil, "2026-11-11 2026-11-12"},
		{"every day a working day: at most 5, oldest first", fri, worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5, 6, 7}}, "2026-01-01", nil, "2026-11-06 2026-11-07 2026-11-08 2026-11-09 2026-11-10"},
	}
	for _, tc := range tests {
		got := strings.Join(CandidateDays(tc.now, tc.cal, tc.first, tc.handled), " ")
		if got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHandledDaysIgnorePreviewsAndFailedRuns(t *testing.T) {
	h := handledDays(state.Data{
		History: []worklog.Run{
			{Date: "2026-11-09", Trigger: worklog.TriggerPreview},
			{Date: "2026-11-10", Trigger: worklog.TriggerSchedule, Error: "Jira unreachable"},
			{Date: "2026-11-11", Trigger: worklog.TriggerSchedule},
			{Date: "2026-11-12", Trigger: worklog.TriggerCatchup},
		},
		SkippedWorklogDays: []string{"2026-11-06"},
	})
	if h["2026-11-09"] || h["2026-11-10"] || !h["2026-11-11"] || !h["2026-11-12"] || !h["2026-11-06"] {
		t.Errorf("handled %v", h)
	}
}

func TestFirstRunDateMigration(t *testing.T) {
	dir := t.TempDir()
	st, _ := state.Open(filepath.Join(dir, "state.json"))
	_ = st.Update(func(tx state.Tx) {
		tx.AddRun(worklog.Run{Date: "2026-10-20", At: "2026-10-20T18:00:00+02:00", Trigger: worklog.TriggerSchedule})
		tx.AddRun(worklog.Run{Date: "2026-09-30", At: "2026-11-01T18:00:00+02:00", Trigger: worklog.TriggerManual})
	})
	a, _, clock, _ := newV71App(t, dir, &fakeJira{}, time.Date(2026, 11, 13, 9, 0, 0, 0, zone), nil)
	if got := a.ensureFirstRunDate(); got != "2026-09-30" {
		t.Errorf("existing install: %q, want the earliest history date", got)
	}
	clock.t = clock.t.AddDate(0, 1, 0)
	if got := a.ensureFirstRunDate(); got != "2026-09-30" {
		t.Errorf("set once: %q", got)
	}
	b, _, _, _ := newV71App(t, t.TempDir(), &fakeJira{}, time.Date(2026, 11, 13, 9, 0, 0, 0, zone), nil)
	b.Start()
	defer b.Stop(time.Second)
	if got := b.st.Snapshot().FirstRunDate; got != "2026-11-13" {
		t.Errorf("fresh install: %q, want today (set by Start)", got)
	}
}

// fixture for the end-to-end catch-up: Friday 13 Nov 2026, first run long
// ago. Mon 9: a history entry (handled). Tue 10: nothing in progress
// (nothing to add). Wed 11: logged by hand already (nothing to add). Thu
// 12: missed (2h in progress, nothing logged). Fri 6: skipped by the user.
func catchupFixture(t *testing.T) (*App, *fakeJira, *fakePlatform, *missRecorder, *pastDays) {
	return catchupFixtureIn(t, t.TempDir())
}

func catchupFixtureIn(t *testing.T, dir string) (*App, *fakeJira, *fakePlatform, *missRecorder, *pastDays) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{"B-2": {{Author: me, Started: "2026-11-11T10:00:00.000+0200", TimeSpentSeconds: 2 * 3600}}}}
	days := &pastDays{byDate: map[string]string{
		"2026-11-11": issuesJSON(issueJSON("B-2", "Done", "2026-11-01T10:00:00.000+0200", "2026-11-11T10:00:00.000+0200 Open>In Progress", "2026-11-11T12:00:00.000+0200 In Progress>Done")),
		"2026-11-12": issuesJSON(issueJSON("C-3", "Done", "2026-11-01T10:00:00.000+0200", "2026-11-12T10:00:00.000+0200 Open>In Progress", "2026-11-12T12:00:00.000+0200 In Progress>Done")),
	}}
	days.install(fj)
	st, _ := state.Open(filepath.Join(dir, "state.json"))
	_ = st.Update(func(tx state.Tx) {
		tx.FirstRunDate = "2026-10-01"
		tx.AddRun(worklog.Run{Date: "2026-11-09", At: "2026-11-09T18:00:00+02:00", Trigger: worklog.TriggerSchedule})
		tx.SkipDay("2026-11-06")
	})
	a, plat, _, rec := newV71App(t, dir, fj, time.Date(2026, 11, 13, 9, 30, 0, 0, zone), nil)
	return a, fj, plat, rec, days
}

func TestCatchUpFindsMissedDaysAndRecordsTheRestSilently(t *testing.T) {
	a, fj, plat, rec, days := catchupFixture(t)
	ctx := context.Background()
	if err := a.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(days.asked, " "); got != "2026-11-10 2026-11-11 2026-11-12" {
		t.Errorf("previewed %q (oldest first, handled and skipped days left out)", got)
	}
	if len(fj.posted) != 0 {
		t.Fatalf("catch-up must not write to Jira: %v", fj.posted)
	}
	snap := a.st.Snapshot()
	var catchups []string
	for _, r := range snap.History {
		if r.Trigger == worklog.TriggerCatchup {
			catchups = append(catchups, r.Date)
		}
	}
	if strings.Join(catchups, " ") != "2026-11-11 2026-11-10" {
		t.Errorf("silent catch-up entries %v", catchups)
	}
	m := a.missedDays()
	if len(m.Days) != 1 || m.Days[0].Date != "2026-11-12" || m.Days[0].TotalSeconds != 2*3600 || m.Days[0].Trigger != worklog.TriggerPreview {
		t.Fatalf("missed %+v", m)
	}
	if rec.count("worklogMissed") != 1 || len(rec.missed[0].Days) != 1 {
		t.Errorf("events %v", rec.events)
	}
	if len(plat.notices) != 1 || plat.notices[0].text != "Worklog not recorded for 12 Nov — open Jiraffe" || plat.notices[0].url != "" {
		t.Errorf("balloons %+v", plat.notices)
	}
	// once a day
	if err := a.CatchUp(ctx); err != nil || len(days.asked) != 3 || len(plat.notices) != 1 {
		t.Errorf("second check the same day: err %v, asked %v, balloons %d", err, days.asked, len(plat.notices))
	}
	res, err := a.Call(ctx, "getMissedWorklogDays", nil)
	if err != nil || len(res.(MissedDaysDTO).Days) != 1 {
		t.Errorf("getMissedWorklogDays %+v %v", res, err)
	}

	// Logging the day removes it.
	if _, err := a.Call(ctx, "worklogRun", json.RawMessage(`{"date":"2026-11-12"}`)); err != nil {
		t.Fatal(err)
	}
	if want := "C-3 7200 2026-11-12T10:00:00.000+0200"; strings.Join(fj.posted, "|") != want {
		t.Errorf("posted %v, want %s", fj.posted, want)
	}
	if m := a.missedDays(); len(m.Days) != 0 {
		t.Errorf("still missed %+v", m)
	}
	if last := rec.missed[len(rec.missed)-1]; len(last.Days) != 0 {
		t.Errorf("last worklogMissed event %+v, want no days", last)
	}
	b, _ := json.Marshal(rec.missed[len(rec.missed)-1])
	if string(b) != `{"days":[]}` {
		t.Errorf("event JSON %s", b)
	}
}

func TestSkipWorklogDay(t *testing.T) {
	dir := t.TempDir()
	a, fj, _, rec, days := catchupFixtureIn(t, dir)
	ctx := context.Background()
	if err := a.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := a.Call(ctx, "skipWorklogDay", json.RawMessage(`{"date":"2026-11-12"}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := res.(MissedDaysDTO); len(d.Days) != 0 {
		t.Errorf("after skip %+v", d)
	}
	if got := a.st.Snapshot().SkippedWorklogDays; strings.Join(got, ",") != "2026-11-06,2026-11-12" {
		t.Errorf("skipped %v", got)
	}
	if rec.count("worklogMissed") != 2 {
		t.Errorf("events %v", rec.events)
	}
	// Never asked again, also after a restart on the next day.
	a2, _, _, _ := newV71App(t, dir, fj, time.Date(2026, 11, 16, 9, 30, 0, 0, zone), nil)
	days.asked = nil
	if err := a2.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	for _, d := range days.asked {
		if d == "2026-11-12" {
			t.Errorf("skipped day previewed again: %v", days.asked)
		}
	}
}

func TestCatchUpConditions(t *testing.T) {
	ctx := context.Background()
	t.Run("not before a Jira call succeeded", func(t *testing.T) {
		a, _, _, _, days := catchupFixture(t)
		a.jiraOK.Store(false)
		_ = a.CatchUp(ctx)
		if len(days.asked) != 0 {
			t.Errorf("asked %v", days.asked)
		}
	})
	t.Run("not when the automatic worklog is off", func(t *testing.T) {
		fj := &fakeJira{}
		days := &pastDays{}
		days.install(fj)
		a, _, _, _ := newV71App(t, t.TempDir(), fj, time.Date(2026, 11, 13, 9, 30, 0, 0, zone), func(s *config.Settings) { s.WorklogEnabled = false })
		_ = a.CatchUp(ctx)
		if len(days.asked) != 0 {
			t.Errorf("asked %v", days.asked)
		}
	})
	t.Run("not before Start", func(t *testing.T) {
		a, _, _, _, days := catchupFixture(t)
		a.started.Store(false)
		_ = a.CatchUp(ctx)
		if len(days.asked) != 0 {
			t.Errorf("asked %v", days.asked)
		}
	})
	t.Run("failure retries after the pause, then daily", func(t *testing.T) {
		a, fj, plat, _, days := catchupFixture(t)
		down := true
		fj.fail = func(*jira.Request) error {
			if down {
				return &jira.TransportError{Kind: jira.NetTimeout, Message: "timed out"}
			}
			return nil
		}
		clock := a.clock.(*fakeClock)
		if err := a.CatchUp(ctx); err == nil {
			t.Fatal("a failed check must return its error")
		}
		down = false
		clock.t = clock.t.Add(catchupRetry - time.Minute)
		_ = a.CatchUp(ctx)
		if len(days.asked) != 0 {
			t.Errorf("retried too early: %v", days.asked)
		}
		clock.t = clock.t.Add(time.Minute)
		if err := a.CatchUp(ctx); err != nil || len(days.asked) == 0 || len(plat.notices) != 1 {
			t.Errorf("retry: err %v, asked %v, balloons %d", err, days.asked, len(plat.notices))
		}
		n := len(days.asked)
		clock.t = time.Date(2026, 11, 16, 9, 0, 0, 0, zone) // Monday: the daily check runs again
		_ = a.CatchUp(ctx)
		if len(days.asked) == n {
			t.Error("no check on the next day")
		}
	})
	t.Run("paused notifications: no balloon, still listed", func(t *testing.T) {
		a, _, plat, _, _ := catchupFixture(t)
		a.SetPaused(true)
		_ = a.CatchUp(ctx)
		if len(plat.notices) != 0 || len(a.missedDays().Days) != 1 {
			t.Errorf("balloons %d, missed %d", len(plat.notices), len(a.missedDays().Days))
		}
	})
}

// A day handled by another run (or process) after the check is no longer
// returned as missed.
func TestMissedDaysAreFilteredByLaterHistory(t *testing.T) {
	a, _, _, _, _ := catchupFixture(t)
	_ = a.CatchUp(context.Background())
	_ = a.st.Update(func(tx state.Tx) {
		tx.AddRun(worklog.Run{Date: "2026-11-12", At: "2026-11-13T10:00:00+02:00", Trigger: worklog.TriggerManual})
	})
	if m := a.missedDays(); len(m.Days) != 0 {
		t.Errorf("missed %+v", m)
	}
}

func TestMissedBalloonListsDaysPerLanguage(t *testing.T) {
	a, _, _, _ := newV71App(t, t.TempDir(), &fakeJira{}, time.Date(2026, 11, 13, 9, 0, 0, 0, zone), func(s *config.Settings) { s.Language = "uk" })
	if got := T("uk", "worklogMissed", a.dayLabels([]string{"2026-11-10", "2026-11-12"})); got != "Час не списано за 10 лист., 12 лист. — відкрийте Jiraffe" {
		t.Errorf("got %q", got)
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		if len(shortMonths[lang]) != 12 {
			t.Errorf("%s months", lang)
		}
	}
}

// Acceptance 1 at the app level: two app instances on one data dir, each
// with its own (stale) in-memory state, saving alternately.
func TestTwoAppsOnOneDataDirLoseNothing(t *testing.T) {
	dir := t.TempDir()
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	now := time.Date(2026, 11, 12, 18, 0, 0, 0, zone)
	a, _, _, _ := newV71App(t, dir, fj, now, nil)
	b, _, clockB, _ := newV71App(t, dir, fj, now, nil)
	ctx := context.Background()
	if err := a.ScheduledWorklogAt(ctx, now); err != nil { // A posts and records 12 Nov
		t.Fatal(err)
	}
	if _, err := b.Call(ctx, "skipWorklogDay", json.RawMessage(`{"date":"2026-11-10"}`)); err != nil { // B saves with its stale state
		t.Fatal(err)
	}
	clockB.t = now.Add(time.Hour)
	if _, err := b.Call(ctx, "markNotificationsRead", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Call(ctx, "skipWorklogDay", json.RawMessage(`{"date":"2026-11-11"}`)); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Open(filepath.Join(dir, "state.json"))
	d := st.Snapshot()
	if len(d.History) != 1 || d.History[0].Date != "2026-11-12" || d.LastWorklogDate != "2026-11-12" {
		t.Errorf("history %+v, last %q", d.History, d.LastWorklogDate)
	}
	if strings.Join(d.SkippedWorklogDays, ",") != "2026-11-10,2026-11-11" {
		t.Errorf("skipped %v", d.SkippedWorklogDays)
	}
	if got := b.st.Snapshot(); len(got.History) != 1 {
		t.Errorf("B does not see A's run after its save: %d entries", len(got.History))
	}
}

func TestWorklogMissedContractJSON(t *testing.T) {
	b, _ := json.Marshal(MissedDaysDTO{Days: []worklog.Run{{Date: "2026-11-12", At: "2026-11-13T09:30:00+02:00", Trigger: "preview",
		Items: []worklog.Item{}, TotalSeconds: 7200, CapSeconds: 20160, Total: "2h", NonWorkingDay: ""}}})
	want := `{"days":[{"at":"2026-11-13T09:30:00+02:00","date":"2026-11-12","trigger":"preview","items":[],"totalSeconds":7200,"otherLoggedSeconds":0,"capSeconds":20160,"total":"2h"}]}`
	if string(b) != want {
		t.Errorf("got  %s\nwant %s", b, want)
	}
	r, _ := json.Marshal(worklog.Run{Date: "2026-11-07", Items: []worklog.Item{}, NonWorkingDay: "weekend"})
	if !strings.Contains(string(r), `"nonWorkingDay":"weekend"`) {
		t.Errorf("run JSON %s", r)
	}
}
