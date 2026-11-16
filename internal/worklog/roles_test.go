package worklog

import (
	"encoding/json"
	"testing"
)

func i64(v int64) *int64 { return &v }

// fixedCand is a fixed-only candidate (author with the given create part).
func fixedCand(key string, create, comment int64, logged int64) Candidate {
	p := FixedPart{CreateSeconds: create, CommentSeconds: comment}
	if create > 0 {
		p.CreateSource = SourceDefault
	}
	if comment > 0 {
		p.CommentSource = SourceDefault
	}
	return Candidate{Key: key, Fixed: p, Logged: logged, FixedStart: at(0, 10, 0)}
}

func planByKey(ps []Planned) map[string]Planned {
	m := map[string]Planned{}
	for _, p := range ps {
		m[p.Key] = p
	}
	return m
}

func sumTodo(ps []Planned) (todo, logged int64) {
	for _, p := range ps {
		todo += p.Todo
		logged += p.Logged
	}
	return
}

// Acceptance 1: status share + 15m (created) + 2×5m (comments), fixed first.
func TestPlanFixedTimeComesFirst(t *testing.T) {
	day := refDay(at(0, 18, 0))
	full := day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}}) // 8h
	cands := []Candidate{
		{Key: "DEV-1", Spans: full, Roles: []string{RoleDev}},
		fixedCand("NEW-2", 15*60, 0, 0),
		fixedCand("CMT-3", 0, 10*60, 0),
	}
	got := planByKey(Plan(cands, 8*3600))
	// Fixed 25m first; the status share (8h) is scaled into the remaining 7h35m.
	if got["NEW-2"].Todo != 15*60 || got["CMT-3"].Todo != 10*60 {
		t.Errorf("fixed todo %d %d", got["NEW-2"].Todo, got["CMT-3"].Todo)
	}
	if got["DEV-1"].Todo != h(7, 35) || got["DEV-1"].Share != 8*3600 {
		t.Errorf("status todo %d share %v", got["DEV-1"].Todo, got["DEV-1"].Share)
	}
	if got["NEW-2"].Started != at(0, 10, 0) || got["DEV-1"].Started != at(0, 9, 0) {
		t.Errorf("started %v %v", got["NEW-2"].Started, got["DEV-1"].Started)
	}

	// Within the cap with room: everything is logged as is.
	short := day.Clip([]Interval{{at(0, 9, 0), at(0, 11, 0)}})
	got = planByKey(Plan([]Candidate{{Key: "DEV-1", Spans: short}, fixedCand("NEW-2", 15*60, 0, 0), fixedCand("CMT-3", 0, 10*60, 0)}, 8*3600))
	if got["DEV-1"].Todo != 2*3600 || got["NEW-2"].Todo != 900 || got["CMT-3"].Todo != 600 {
		t.Errorf("unscaled %+v", got)
	}
}

func TestPlanFixedOverBudgetScalesFixedOnly(t *testing.T) {
	day := refDay(at(0, 18, 0))
	full := day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}})
	cands := []Candidate{
		{Key: "DEV-1", Spans: full},
		fixedCand("A-1", 3*3600, 0, 0),
		fixedCand("B-2", 0, 1*3600, 0),
	}
	// Budget 2h < fixed 4h: fixed scaled 3:1, status gets nothing.
	got := planByKey(Plan(cands, 2*3600))
	if got["A-1"].Todo != h(1, 30) || got["B-2"].Todo != h(0, 30) || got["DEV-1"].Todo != 0 {
		t.Errorf("scaled %d %d %d", got["A-1"].Todo, got["B-2"].Todo, got["DEV-1"].Todo)
	}
}

func TestPlanLoggedCountsAgainstFixedThenShare(t *testing.T) {
	day := refDay(at(0, 18, 0))
	two := day.Clip([]Interval{{at(0, 9, 0), at(0, 11, 0)}})
	// One issue: 2h status + 15m fixed; 30m already logged on it covers the
	// fixed part and 15m of the share.
	c := Candidate{Key: "A-1", Spans: two, Logged: 30 * 60,
		Fixed: FixedPart{CreateSeconds: 15 * 60, CreateSource: SourceDefault}}
	got := Plan([]Candidate{c}, 8*3600)
	if got[0].Todo != h(1, 45) {
		t.Errorf("todo %d, want 1h45m", got[0].Todo)
	}
}

func TestPlanRerunLogsNothingAndCapHolds(t *testing.T) {
	day := refDay(at(0, 18, 0))
	cases := []struct {
		name  string
		cap   int64
		cands []Candidate
	}{
		{"room", 8 * 3600, []Candidate{
			{Key: "D-1", Spans: day.Clip([]Interval{{at(0, 9, 0), at(0, 12, 7)}})},
			fixedCand("N-2", 15*60, 0, 0), fixedCand("C-3", 0, 3*5*60, 0)}},
		{"status scaled", 7*3600 + 1234, []Candidate{
			{Key: "D-1", Spans: day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}})},
			{Key: "D-2", Spans: day.Clip([]Interval{{at(0, 10, 13), at(0, 16, 41)}})},
			fixedCand("N-2", 20*60, 5*60, 0), fixedCand("C-3", 0, 7*60, 0)}},
		{"fixed scaled", 1000, []Candidate{
			{Key: "D-1", Spans: day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}})},
			fixedCand("N-2", 15*60, 0, 0), fixedCand("C-3", 0, 10*60, 0), fixedCand("C-4", 0, 5*60, 0)}},
		{"already over the cap", 3600, []Candidate{
			fixedCand("N-2", 15*60, 0, 2*3600), {Key: "D-1", Spans: day.Clip([]Interval{{at(0, 9, 0), at(0, 10, 0)}})}}},
	}
	for _, tc := range cases {
		first := Plan(tc.cands, tc.cap)
		todo, logged := sumTodo(first)
		if logged < tc.cap && todo+logged > tc.cap {
			t.Errorf("%s: first run %d + logged %d exceeds the cap %d", tc.name, todo, logged, tc.cap)
		}
		if logged >= tc.cap && todo != 0 {
			t.Errorf("%s: over the cap but todo %d", tc.name, todo)
		}
		for _, p := range first {
			if p.Todo%60 != 0 || p.Todo < 0 {
				t.Errorf("%s: %s todo %d is not whole minutes", tc.name, p.Key, p.Todo)
			}
		}
		// Re-run: what was logged is subtracted. When the status time was
		// scaled, the minutes lost to rounding may top the day up once (as
		// before v0.8), never beyond the cap; after that nothing is logged.
		cur := append([]Candidate{}, tc.cands...)
		for run := 2; run <= 3; run++ {
			prev := planByKey(Plan(cur, tc.cap))
			for i := range cur {
				cur[i].Logged += prev[cur[i].Key].Todo
			}
			next := Plan(cur, tc.cap)
			todo2, logged2 := sumTodo(next)
			if logged2 < tc.cap && todo2+logged2 > tc.cap {
				t.Errorf("%s: run %d exceeds the cap", tc.name, run)
			}
			if tc.name != "status scaled" || run == 3 {
				if todo2 != 0 {
					t.Errorf("%s: run %d logs %d more", tc.name, run, todo2)
				}
			}
		}
	}
}

func TestPlanKeepsFixedOnlyCandidates(t *testing.T) {
	zero := Candidate{Key: "Z-1", Fixed: FixedPart{CreateSource: SourceManual}} // manual 0: shown, nothing logged
	got := Plan([]Candidate{{Key: "EMPTY"}, zero, fixedCand("N-2", 900, 0, 0)}, 8*3600)
	if len(got) != 2 || got[0].Key != "Z-1" || got[0].Todo != 0 || got[1].Todo != 900 {
		t.Errorf("got %+v", got)
	}
}

func TestFixedTimePrecedence(t *testing.T) {
	issues := []FixedIssue{
		{Key: "A-1", Created: true},
		{Key: "B-2", Created: true, Comments: 2},
		{Key: "C-3", Comments: 1},
		{Key: "D-4", Created: true, Comments: 1},
	}
	// Defaults.
	got := FixedTime(issues, DayOverrides{}, 900, 300)
	if p := got["B-2"]; p.CreateSeconds != 900 || p.CommentSeconds != 600 || p.CreateSource != SourceDefault || p.CommentSource != SourceDefault {
		t.Errorf("defaults B-2 %+v", p)
	}
	if p := got["A-1"]; p.CommentSource != "" || p.CommentSeconds != 0 || p.Seconds() != 900 {
		t.Errorf("defaults A-1 %+v", p)
	}
	if p := got["C-3"]; p.CreateSource != "" || p.CommentSeconds != 300 {
		t.Errorf("defaults C-3 %+v", p)
	}

	// Group totals split evenly; manual values win and are left out of the split.
	ov := DayOverrides{
		CreateTotalSeconds:  i64(3600), // 2 shares (A-1, B-2): 30m each
		CommentTotalSeconds: i64(1000), // 3 comments (B-2 ×2, C-3): 333s per comment
		Issues: map[string]IssueOverride{
			"D-4": {CreateSeconds: i64(0), CommentSeconds: i64(1200)},
		},
	}
	got = FixedTime(issues, ov, 900, 300)
	want := map[string]FixedPart{
		"A-1": {CreateSeconds: 1800, CreateSource: SourceGroup},
		"B-2": {CreateSeconds: 1800, CreateSource: SourceGroup, CommentSeconds: 660, CommentSource: SourceGroup}, // 666s → 11m
		"C-3": {CommentSeconds: 300, CommentSource: SourceGroup},                                                 // 333s → 5m
		"D-4": {CreateSeconds: 0, CreateSource: SourceManual, CommentSeconds: 1200, CommentSource: SourceManual},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %+v want %+v", k, got[k], w)
		}
	}

	// Only a group comment total: create parts stay default.
	got = FixedTime(issues, DayOverrides{CommentTotalSeconds: i64(0)}, 900, 300)
	if p := got["B-2"]; p.CreateSource != SourceDefault || p.CommentSeconds != 0 || p.CommentSource != SourceGroup {
		t.Errorf("comment group only %+v", p)
	}
	// An override for an issue without that role is ignored.
	got = FixedTime([]FixedIssue{{Key: "C-3", Comments: 1}}, DayOverrides{Issues: map[string]IssueOverride{"C-3": {CreateSeconds: i64(600)}}}, 900, 300)
	if p := got["C-3"]; p.CreateSeconds != 0 || p.CreateSource != "" || p.CommentSeconds != 300 {
		t.Errorf("foreign override %+v", p)
	}
}

func TestValidKey(t *testing.T) {
	for k, want := range map[string]bool{
		"A-1": true, "ABC_9-123": true, "A1-0": true,
		"": false, "a-1": false, "1A-1": false, "_A-1": false, "A-": false, "-1": false, "A1": false,
		"A-1a": false, "A--1": false, "AB-1-2": false, "Ab-1": false, "A-1 ": false, "А-1": false,
	} {
		if got := ValidKey(k); got != want {
			t.Errorf("ValidKey(%q) = %v", k, got)
		}
	}
}

func TestDayOverridesValidAndNormalize(t *testing.T) {
	ok := DayOverrides{CreateTotalSeconds: i64(0), CommentTotalSeconds: i64(86400),
		Issues: map[string]IssueOverride{"A-1": {CreateSeconds: i64(60)}, "B-2": {}}}
	if !ok.Valid() {
		t.Error("valid overrides rejected")
	}
	for name, bad := range map[string]DayOverrides{
		"negative":       {CreateTotalSeconds: i64(-60)},
		"too large":      {CommentTotalSeconds: i64(86460)},
		"not minutes":    {Issues: map[string]IssueOverride{"A-1": {CommentSeconds: i64(90)}}},
		"bad key":        {Issues: map[string]IssueOverride{"a-1": {CreateSeconds: i64(60)}}},
		"bad issue item": {Issues: map[string]IssueOverride{"A-1": {CreateSeconds: i64(86401)}}},
	} {
		if bad.Valid() {
			t.Errorf("%s accepted", name)
		}
	}
	many := DayOverrides{Issues: map[string]IssueOverride{}}
	for i := 0; i <= MaxOverrideIssues; i++ {
		many.Issues["A-"+string(rune('0'+i%10))+string(rune('0'+i/10%10))+string(rune('0'+i/100))] = IssueOverride{CreateSeconds: i64(60)}
	}
	if len(many.Issues) != MaxOverrideIssues+1 || many.Valid() {
		t.Errorf("%d issues accepted", len(many.Issues))
	}

	ok.Normalize()
	if _, kept := ok.Issues["B-2"]; kept || len(ok.Issues) != 1 {
		t.Errorf("normalize kept empty issues: %+v", ok.Issues)
	}
	b, _ := json.Marshal(ok)
	if string(b) != `{"createTotalSeconds":0,"commentTotalSeconds":86400,"issues":{"A-1":{"createSeconds":60}}}` {
		t.Errorf("json %s", b)
	}
	var empty DayOverrides
	empty.Normalize()
	if b, _ := json.Marshal(empty); string(b) != `{"issues":{}}` || !empty.Empty() {
		t.Errorf("empty json %s", b)
	}
}
