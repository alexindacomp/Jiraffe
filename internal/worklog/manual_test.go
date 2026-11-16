package worklog

import (
	"testing"
	"time"
)

func TestManualTimeSplit(t *testing.T) {
	m := ManualTime([]ManualEntry{
		{ID: "a", Keys: []string{"A-1", "B-2", "C-3"}, Seconds: 100 * 60, Activity: ActivityGrooming, Note: " sprint 115 "},
		{ID: "b", Keys: []string{"B-2"}, Seconds: 30 * 60, Activity: ActivityGrooming},
		{ID: "c", Keys: []string{"B-2"}, Seconds: 15 * 60, Activity: ActivityQuestions, Note: "sprint 115"},
	})
	if m["A-1"].Seconds != 34*60 || m["B-2"].Seconds != (33+30+15)*60 || m["C-3"].Seconds != 33*60 {
		t.Errorf("split %d %d %d", m["A-1"].Seconds, m["B-2"].Seconds, m["C-3"].Seconds)
	}
	b := m["B-2"]
	if len(b.Activities) != 2 || b.Activities[0] != ActivityGrooming || b.Activities[1] != ActivityQuestions || len(b.Notes) != 1 || b.Notes[0] != "sprint 115" {
		t.Errorf("B-2 %+v", b)
	}
}

func TestManualEntryValid(t *testing.T) {
	ok := ManualEntry{ID: "e1", Keys: []string{"MB-1", "MB-2"}, Seconds: 3600, Activity: ActivityEstimation, Note: "x"}
	if !validManual([]ManualEntry{ok}) {
		t.Fatal("valid entry rejected")
	}
	bad := []func(e *ManualEntry){
		func(e *ManualEntry) { e.ID = "" },
		func(e *ManualEntry) { e.ID = "a b" },
		func(e *ManualEntry) { e.Keys = nil },
		func(e *ManualEntry) { e.Keys = []string{"MB-1", "MB-1"} },
		func(e *ManualEntry) { e.Keys = []string{"mb-1"} },
		func(e *ManualEntry) { e.Seconds = 30 },
		func(e *ManualEntry) { e.Seconds = 90 },
		func(e *ManualEntry) { e.Seconds = MaxOverrideSeconds + 60 },
		func(e *ManualEntry) { e.Activity = "meeting" },
		func(e *ManualEntry) { e.Note = string(make([]rune, MaxManualNote+1)) },
	}
	for i, f := range bad {
		e := ok.clone()
		f(&e)
		if validManual([]ManualEntry{e}) {
			t.Errorf("bad %d accepted: %+v", i, e)
		}
	}
	if validManual([]ManualEntry{ok, ok}) {
		t.Error("duplicate ids accepted")
	}
}

// Manual time comes first; fixed then status time share what is left.
func TestPlanManualFirst(t *testing.T) {
	day := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	cands := []Candidate{
		{Key: "S-1", Spans: []Interval{{day, day.Add(8 * time.Hour)}}, Roles: []string{RoleDev}},
		{Key: "F-2", Fixed: FixedPart{CreateSeconds: 1800, CreateSource: SourceDefault}, FixedStart: day},
		{Key: "M-3", Manual: 2 * 3600, FixedStart: day},
	}
	got := map[string]int64{}
	for _, p := range Plan(cands, 3*3600) {
		got[p.Key] = p.Todo
	}
	if got["M-3"] != 7200 || got["F-2"] != 1800 || got["S-1"] != 1800 {
		t.Errorf("cap 3h: %v", got)
	}
	for _, p := range Plan(cands, 3600) { // manual alone exceeds the cap: scaled, nothing else
		got[p.Key] = p.Todo
	}
	if got["M-3"] != 3600 || got["F-2"] != 0 || got["S-1"] != 0 {
		t.Errorf("cap 1h: %v", got)
	}
	cands[2].Logged = 2 * 3600 // re-run: the manual part is logged
	for _, p := range Plan(cands, 3*3600) {
		got[p.Key] = p.Todo
	}
	if got["M-3"] != 0 || got["F-2"] != 1800 || got["S-1"] != 1800 { // 3h − 2h logged = 1h: fixed 30m, status 30m
		t.Errorf("re-run: %v", got)
	}
}
