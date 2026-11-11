package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// allDayB2 is in progress since yesterday like allDayA1.
var allDayB2 = issueJSON("B-2", "In Progress", "2026-11-10T10:00:00.000+0200", "2026-11-10T11:00:00.000+0200 ToDo>In Progress")

// SPEC v0.2 "Worklog change — load factor" scenarios (day 09–18, lunch
// 13–14, cap 8h, run at 18:00).
func TestLoadFactorScenarios(t *testing.T) {
	const h, m = int64(3600), int64(60)
	threeHoursElsewhere := `[{"key":"X-9","fields":{"worklog":{"startAt":0,"maxResults":20,"total":1,"worklogs":[` +
		`{"author":{"name":"alex","key":"alex"},"started":"2026-11-11T09:00:00.000+0200","timeSpentSeconds":10800}]}}}]`
	tests := []struct {
		name       string
		loadFactor float64
		issues     string
		capIssues  string
		want       map[string]int64 // todo seconds per issue
		capSeconds int64
	}{
		{"0.6, A all day", 0.6, issuesJSON(allDayA1), "", map[string]int64{"A-1": 4*h + 48*m}, 4*h + 48*m},
		{"0.5, A and B all day", 0.5, issuesJSON(allDayA1, allDayB2), "", map[string]int64{"A-1": 2 * h, "B-2": 2 * h}, 4 * h},
		{"0.5, A all day, 3h logged elsewhere", 0.5, issuesJSON(allDayA1), threeHoursElsewhere, map[string]int64{"A-1": 1 * h}, 4 * h},
		{"0: nothing", 0, issuesJSON(allDayA1, allDayB2), "", map[string]int64{"A-1": 0, "B-2": 0}, 0},
		{"1.0 = v0.1: A and B all day", 1, issuesJSON(allDayA1, allDayB2), "", map[string]int64{"A-1": 4 * h, "B-2": 4 * h}, 8 * h},
		{"1.0 = v0.1: A all day", 1, issuesJSON(allDayA1), "", map[string]int64{"A-1": 8 * h}, 8 * h},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fj := &fakeJira{issues: tc.issues, capIssues: tc.capIssues, worklogs: map[string][]jira.Worklog{}}
			a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)},
				func(s *config.Settings) { s.LoadFactor = tc.loadFactor })
			run := preview(t, a)
			if run.Error != "" || run.CapSeconds != tc.capSeconds {
				t.Fatalf("run error %q, capSeconds %d want %d", run.Error, run.CapSeconds, tc.capSeconds)
			}
			items := itemsByKey(run)
			var total int64
			for key, want := range tc.want {
				it, ok := items[key]
				if !ok || it.TodoSeconds != want {
					t.Errorf("%s: todo %d, want %d (%+v)", key, it.TodoSeconds, want, it)
				}
				if want == 0 && it.Result != worklog.ResultSkipped {
					t.Errorf("%s: result %q, want skipped", key, it.Result)
				}
				total += want
			}
			if run.TotalSeconds != total || len(run.Items) != len(tc.want) {
				t.Errorf("total %d want %d, items %+v", run.TotalSeconds, total, run.Items)
			}

			// The manual run writes exactly the previewed amounts.
			res, err := a.Call(context.Background(), "worklogRun", nil)
			if err != nil {
				t.Fatal(err)
			}
			if r := res.(worklog.Run); r.TotalSeconds != total || r.CapSeconds != tc.capSeconds {
				t.Errorf("manual run total %d cap %d", r.TotalSeconds, r.CapSeconds)
			}
			posted := 0
			for _, want := range tc.want {
				if want > 0 {
					posted++
				}
			}
			if len(fj.posted) != posted {
				t.Errorf("posted %v", fj.posted)
			}
		})
	}
}

func TestCapSecondsInWorklogRunJSON(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 14, 18, 0, 0, 0, zone)}, // Saturday
		func(s *config.Settings) { s.LoadFactor = 0.75 })
	run := preview(t, a)
	b, _ := json.Marshal(run)
	if run.SkippedReason != worklog.ReasonWeekend || !strings.Contains(string(b), `"capSeconds":21600`) {
		t.Errorf("a skipped run still reports the effective cap: %s", b)
	}
}

func TestSaveSettingsValidatesLoadFactor(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	for _, p := range []string{`{"loadFactor":1.5,"language":"en"}`, `{"loadFactor":-1,"language":"en"}`,
		`{"loadFactor":"0.5","language":"en"}`, `{"loadFactor":null,"language":"en"}`, `{"loadFactor":true,"language":"en"}`} {
		_, err := a.Call(ctx, "saveSettings", json.RawMessage(p))
		e := ToError(err)
		if e.Code != CodeValidation || len(e.Fields) != 1 || e.Fields[0].Field != "loadFactor" || e.Fields[0].Message != "Must be between 0 and 1" {
			t.Errorf("%s: %+v", p, e)
		}
	}
	if a.cfg.Get().LoadFactor != 1 {
		t.Fatalf("invalid value stored: %v", a.cfg.Get().LoadFactor)
	}
	_, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"loadFactor":"x","language":"uk"}`))
	if e := ToError(err); len(e.Fields) != 1 || e.Fields[0].Message != "Допустимо від 0 до 1" {
		t.Errorf("uk: %+v", e)
	}
	res, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"loadFactor":0.6}`))
	if err != nil {
		t.Fatal(err)
	}
	if lf := res.(SaveResult).LoadFactor; lf != 0.6 {
		t.Errorf("saved %v", lf)
	}
	got, _ := a.Call(ctx, "getSettings", nil)
	if b, _ := json.Marshal(got); !strings.Contains(string(b), `"loadFactor":0.6`) || !strings.Contains(string(b), `"trackedProjects":null`) {
		t.Errorf("getSettings %s", b)
	}
}
