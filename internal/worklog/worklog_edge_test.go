package worklog

import (
	"math/rand"
	"testing"
	"time"
)

// Edge cases of the worklog engine beyond the SPEC reference scenarios.
// Expected numbers were cross-checked against work_time/allocate/jira_duration
// of the Python reference (/home/alex/jira-worklog/worklog.py).

func moved(at time.Time, from, to string) StatusChange {
	return StatusChange{At: at, From: from, To: to}
}

func TestReenteringInProgressSeveralTimesADay(t *testing.T) {
	// ToDo -> IP 09:30 -> Review 11:00 -> IP 12:00 -> Review 13:30 -> IP 15:00 (still open at 18:00):
	// 1h30m + 1h (lunch cut) + 3h = 5h30m, started at the first stretch.
	is := issueSpec{key: "A", created: at(-5, 9, 0), status: "In Progress", changes: []StatusChange{
		moved(at(0, 9, 30), "ToDo", "In Progress"),
		moved(at(0, 11, 0), "In Progress", "ForReview"),
		moved(at(0, 12, 0), "ForReview", "In Progress"),
		moved(at(0, 13, 30), "In Progress", "ForReview"),
		moved(at(0, 15, 0), "ForReview", "In Progress"),
	}}
	got := plan(t, at(0, 18, 0), []issueSpec{is})
	expectTodo(t, got, map[string]int64{"A": h(5, 30)})
	if !got["A"].Started.Equal(at(0, 9, 30)) {
		t.Errorf("started %s, want 09:30", got["A"].Started.Format("15:04"))
	}
	if n := len(got["A"].Spans); n != 3 {
		t.Errorf("want 3 clipped spans, got %d: %+v", n, got["A"].Spans)
	}
}

func TestTransitionsExactlyAtBoundaries(t *testing.T) {
	ip := func(from, to time.Time) issueSpec {
		changes := []StatusChange{moved(from, "ToDo", "In Progress")}
		status := "In Progress"
		if !to.IsZero() {
			changes = append(changes, moved(to, "In Progress", "ForReview"))
			status = "ForReview"
		}
		return issueSpec{key: "A", created: at(-3, 9, 0), status: status, changes: changes}
	}
	tests := []struct {
		name        string
		issue       issueSpec
		want        int64
		wantStarted time.Time
	}{
		{"taken at 09:00 sharp, open", ip(at(0, 9, 0), time.Time{}), h(8, 0), at(0, 9, 0)},
		{"taken at 08:00, done at 09:00 sharp", ip(at(0, 8, 0), at(0, 9, 0)), 0, time.Time{}},
		{"taken at 13:00 sharp, done at 14:00 sharp (lunch only)", ip(at(0, 13, 0), at(0, 14, 0)), 0, time.Time{}},
		{"12:00 to 13:00 sharp", ip(at(0, 12, 0), at(0, 13, 0)), h(1, 0), at(0, 12, 0)},
		{"taken at 13:00 sharp, open", ip(at(0, 13, 0), time.Time{}), h(4, 0), at(0, 14, 0)},
		{"taken at 14:00 sharp, done at 18:00 sharp", ip(at(0, 14, 0), at(0, 18, 0)), h(4, 0), at(0, 14, 0)},
		{"taken at 18:00 sharp", ip(at(0, 18, 0), time.Time{}), 0, time.Time{}},
		{"12:59 to 14:01 counts two minutes", ip(at(0, 12, 59), at(0, 14, 1)), h(0, 2), at(0, 12, 59)},
		{"taken yesterday, done today 09:00 sharp", ip(at(-1, 10, 0), at(0, 9, 0)), 0, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := plan(t, at(0, 18, 0), []issueSpec{tc.issue})
			expectTodo(t, got, map[string]int64{"A": tc.want})
			if tc.want > 0 && !got["A"].Started.Equal(tc.wantStarted) {
				t.Errorf("started %s, want %s", got["A"].Started.Format("15:04"), tc.wantStarted.Format("15:04"))
			}
		})
	}
}

func TestIssueCreatedTodayAlreadyInProgress(t *testing.T) {
	now := at(0, 18, 0)
	t.Run("no changelog: counted from created", func(t *testing.T) {
		got := plan(t, now, []issueSpec{{key: "A", created: at(0, 10, 15), status: "In Progress"}})
		expectTodo(t, got, map[string]int64{"A": h(6, 45)})
		if !got["A"].Started.Equal(at(0, 10, 15)) {
			t.Errorf("started %s, want 10:15", got["A"].Started.Format("15:04"))
		}
	})
	t.Run("created in progress, moved on at 12:00", func(t *testing.T) {
		got := plan(t, now, []issueSpec{{key: "A", created: at(0, 10, 0), status: "ForReview",
			changes: []StatusChange{moved(at(0, 12, 0), "In Progress", "ForReview")}}})
		expectTodo(t, got, map[string]int64{"A": h(2, 0)})
	})
	t.Run("created before workday start in progress", func(t *testing.T) {
		got := plan(t, now, []issueSpec{{key: "A", created: at(0, 7, 0), status: "в работе"}})
		expectTodo(t, got, map[string]int64{"A": h(8, 0)})
		if !got["A"].Started.Equal(at(0, 9, 0)) {
			t.Errorf("started %s, want 09:00", got["A"].Started.Format("15:04"))
		}
	})
	t.Run("created today in ToDo, never started", func(t *testing.T) {
		got := plan(t, now, []issueSpec{{key: "A", created: at(0, 10, 0), status: "ToDo"}})
		if len(got) != 0 {
			t.Errorf("got %+v", got)
		}
	})
}

func TestStatusNamesCaseInsensitiveAndTranslated(t *testing.T) {
	for _, name := range []string{"In Progress", "IN PROGRESS", "in progress", " In Progress ", "В работе", "В РАБОТЕ", "в работе", "В роботі", "В РОБОТІ"} {
		t.Run(name, func(t *testing.T) {
			is := issueSpec{key: "A", created: at(-3, 9, 0), status: name,
				changes: []StatusChange{moved(at(-1, 11, 0), "ToDo", name)}}
			expectTodo(t, plan(t, at(0, 18, 0), []issueSpec{is}), map[string]int64{"A": h(8, 0)})
		})
	}
	for _, name := range []string{"ForReview", "InReview", "Готово", "Предоставлено решение", "In Progres", ""} {
		if defaultInProgress.Has(name) {
			t.Errorf("%q must not count as in progress", name)
		}
	}
	custom := NewStatusSet([]string{"Doing", "  ", ""})
	if !custom.Has("DOING") || custom.Has("In Progress") || custom.Has("") {
		t.Errorf("custom set %v", custom)
	}
}

func TestRunTiming(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want int64 // for an issue in progress since yesterday
	}{
		{"run at 09:00 sharp", at(0, 9, 0), 0},
		{"run at 12:00", at(0, 12, 0), h(3, 0)},
		{"run at 13:00 sharp", at(0, 13, 0), h(4, 0)},
		{"run during lunch", at(0, 13, 30), h(4, 0)},
		{"run at 14:00 sharp", at(0, 14, 0), h(4, 0)},
		{"run at 14:30", at(0, 14, 30), h(4, 30)},
		{"run after workday end is clipped at 18:00", at(0, 21, 0), h(8, 0)},
		{"run at 23:59", at(0, 23, 59), h(8, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expectTodo(t, plan(t, tc.now, []issueSpec{allDay("A")}), map[string]int64{"A": tc.want})
		})
	}
	t.Run("issue moved on after workday end is still capped by the workday", func(t *testing.T) {
		is := issueSpec{key: "A", created: at(-3, 9, 0), status: "ForReview", changes: []StatusChange{
			moved(at(-1, 11, 0), "ToDo", "In Progress"),
			moved(at(0, 20, 0), "In Progress", "ForReview"),
		}}
		expectTodo(t, plan(t, at(0, 21, 0), []issueSpec{is}), map[string]int64{"A": h(8, 0)})
	})
}

func TestManyParallelIssuesRoundDownWithinCap(t *testing.T) {
	now := at(0, 18, 0)
	for n := 1; n <= 9; n++ {
		var issues []issueSpec
		for i := 0; i < n; i++ {
			issues = append(issues, allDay(string(rune('A'+i))))
		}
		got := plan(t, now, issues)
		var total int64
		wantEach := int64(480/n) * 60 // whole minutes, rounded down
		for k, p := range got {
			if p.Todo != wantEach {
				t.Errorf("n=%d %s: todo %ds, want %ds", n, k, p.Todo, wantEach)
			}
			total += p.Todo
		}
		if total > 8*3600 {
			t.Errorf("n=%d: total %ds exceeds the cap", n, total)
		}
		// Re-run after logging: the rounding remainder (< 1 minute per issue) is never logged.
		for i := range issues {
			issues[i].logged = got[issues[i].key].Todo
		}
		for k, p := range plan(t, now, issues) {
			if p.Todo != 0 {
				t.Errorf("n=%d: re-run would log %ds more on %s", n, p.Todo, k)
			}
		}
	}
	// 7 issues: 480m/7 = 68.57m -> 68m each, 7h 56m in total (Python reference: 4080s, 28560s).
	var issues []issueSpec
	for _, k := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		issues = append(issues, allDay(k))
	}
	var total int64
	for _, p := range plan(t, now, issues) {
		total += p.Todo
	}
	if total != 28560 || FormatDuration(float64(total), 9) != "7h 56m" {
		t.Errorf("7 parallel issues: total %ds (%s), want 28560s (7h 56m)", total, FormatDuration(float64(total), 9))
	}
}

func TestFractionalCapWithLoggedTimeMatchesReference(t *testing.T) {
	// A all day (share 6h50m) with 5h17m already logged, B 10:00-12:20 (share 1h10m),
	// cap 7.5h: budget 2h13m < need 2h43m -> scaled to A 1h15m, B 57m (Python: 4500, 3420).
	day := refDay(at(0, 18, 0))
	cands := []Candidate{
		{Key: "A", Spans: day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}}), Logged: h(5, 17)},
		{Key: "B", Spans: day.Clip([]Interval{{at(0, 10, 0), at(0, 12, 20)}})},
	}
	got := Plan(cands, int64(7.5*3600))
	if got[0].Key != "A" || got[1].Key != "B" {
		t.Fatalf("order not preserved: %+v", got)
	}
	if got[0].Share != float64(h(6, 50)) || got[1].Share != float64(h(1, 10)) {
		t.Errorf("shares %v %v", got[0].Share, got[1].Share)
	}
	if got[0].Todo != 4500 || got[1].Todo != 3420 {
		t.Errorf("todo A=%d B=%d, want 4500 3420", got[0].Todo, got[1].Todo)
	}
	if sum := got[0].Todo + got[1].Todo + got[0].Logged; sum > int64(7.5*3600) {
		t.Errorf("total %d exceeds the cap", sum)
	}
}

func TestAlreadyOverCapLogsNothing(t *testing.T) {
	a := allDay("A")
	a.logged = h(9, 0) // more than the 8h cap, e.g. logged by hand
	got := plan(t, at(0, 18, 0), []issueSpec{a, allDay("B")})
	expectTodo(t, got, map[string]int64{"A": 0, "B": 0})
}

// TestAllocateInvariants checks, on many deterministic pseudo-random days,
// the properties that protect the user's time records: no negative or partial
// minutes, never above the cap, never more than the issue's own share minus
// what is already logged, and parallel shares never exceed wall-clock time.
func TestAllocateInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(20261111))
	day := refDay(at(0, 18, 0))
	base := at(0, 8, 0)
	for iter := 0; iter < 2000; iter++ {
		n := 1 + rng.Intn(8)
		spans := map[string][]Interval{}
		logged := map[string]int64{}
		var union []Interval
		for i := 0; i < n; i++ {
			key := string(rune('A' + i))
			var raw []Interval
			for j := 0; j < 1+rng.Intn(3); j++ {
				s := base.Add(time.Duration(rng.Intn(11*60*60)) * time.Second)
				e := s.Add(time.Duration(1+rng.Intn(6*60*60)) * time.Second)
				raw = append(raw, Interval{s, e})
			}
			spans[key] = day.Clip(raw)
			union = append(union, spans[key]...)
			if rng.Intn(3) == 0 {
				logged[key] = int64(rng.Intn(5*60)) * 60
			}
		}
		capSec := int64(1+rng.Intn(10)) * 3600
		share, todo := Allocate(spans, logged, capSec)

		var sumTodo, sumLogged int64
		var sumShare float64
		for k := range spans {
			v := todo[k]
			if v < 0 || v%60 != 0 {
				t.Fatalf("iter %d %s: todo %d is negative or not whole minutes", iter, k, v)
			}
			if float64(v) > share[k]-float64(logged[k])+1e-3 && v > 0 {
				t.Fatalf("iter %d %s: todo %d exceeds share %.1f - logged %d", iter, k, v, share[k], logged[k])
			}
			sumTodo += v
			sumLogged += logged[k]
			sumShare += share[k]
		}
		if sumLogged <= capSec && sumTodo+sumLogged > capSec {
			t.Fatalf("iter %d: todo %d + logged %d exceeds cap %d", iter, sumTodo, sumLogged, capSec)
		}
		if sumLogged > capSec && sumTodo != 0 {
			t.Fatalf("iter %d: over the cap already, still todo %d", iter, sumTodo)
		}
		if wall := unionSeconds(union); sumShare > wall+1e-3 {
			t.Fatalf("iter %d: shares %.1f exceed wall-clock %.1f", iter, sumShare, wall)
		}
	}
}

// unionSeconds is the length of the union of intervals (a straightforward
// reimplementation used only as a test oracle).
func unionSeconds(ivs []Interval) float64 {
	var total float64
	for i := 0; i < len(ivs); i++ {
		for j := i + 1; j < len(ivs); j++ {
			if ivs[j].Start.Before(ivs[i].Start) {
				ivs[i], ivs[j] = ivs[j], ivs[i]
			}
		}
	}
	var curS, curE time.Time
	for i, iv := range ivs {
		if i == 0 || iv.Start.After(curE) {
			if i > 0 {
				total += curE.Sub(curS).Seconds()
			}
			curS, curE = iv.Start, iv.End
			continue
		}
		if iv.End.After(curE) {
			curE = iv.End
		}
	}
	if len(ivs) > 0 {
		total += curE.Sub(curS).Seconds()
	}
	return total
}

func TestPlanSkipsEmptyAndDuplicateCandidates(t *testing.T) {
	day := refDay(at(0, 18, 0))
	full := day.Clip([]Interval{{at(-1, 9, 0), at(0, 18, 0)}})
	got := Plan([]Candidate{
		{Key: "A", Spans: full},
		{Key: "EMPTY"},
		{Key: "A", Spans: day.Clip([]Interval{{at(0, 9, 0), at(0, 10, 0)}}), Logged: h(8, 0)}, // duplicate from a shifted search page
		{Key: "B", Spans: full},
	}, 8*3600)
	if len(got) != 2 || got[0].Key != "A" || got[1].Key != "B" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Todo != h(4, 0) || got[1].Todo != h(4, 0) || got[0].Logged != 0 {
		t.Errorf("the first occurrence must win: %+v", got)
	}
}

func TestClipEdges(t *testing.T) {
	d := refDay(at(0, 18, 0))
	tests := []struct {
		name string
		in   Interval
		want []Interval
	}{
		{"inside lunch", Interval{at(0, 13, 10), at(0, 13, 50)}, nil},
		{"spans lunch", Interval{at(0, 12, 0), at(0, 15, 0)}, []Interval{{at(0, 12, 0), at(0, 13, 0)}, {at(0, 14, 0), at(0, 15, 0)}}},
		{"before workday", Interval{at(0, 7, 0), at(0, 8, 59)}, nil},
		{"after workday", Interval{at(0, 18, 0), at(0, 20, 0)}, nil},
		{"yesterday only", Interval{at(-1, 9, 0), at(-1, 18, 0)}, nil},
		{"empty interval", Interval{at(0, 10, 0), at(0, 10, 0)}, nil},
	}
	for _, tc := range tests {
		got := d.Clip([]Interval{tc.in})
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %+v", tc.name, got)
			continue
		}
		for i := range got {
			if !got[i].Start.Equal(tc.want[i].Start) || !got[i].End.Equal(tc.want[i].End) {
				t.Errorf("%s: piece %d = %v-%v", tc.name, i, got[i].Start, got[i].End)
			}
		}
	}
	// A run before the workday start produces an inverted day: nothing counts.
	early := Day{Start: at(0, 9, 0), End: at(0, 8, 0), HasLunch: true, LunchStart: at(0, 13, 0), LunchEnd: at(0, 14, 0)}
	if got := early.Clip([]Interval{{at(-1, 9, 0), at(0, 8, 0)}}); len(got) != 0 {
		t.Errorf("inverted day: %+v", got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		sec  float64
		want string
	}{
		{-60, "0m"},
		{0, "0m"},
		{10, "1m"},
		{29, "1m"},
		{30, "1m"},
		{60, "1m"},
		{89, "1m"},
		{4*3600 + 30*60, "4h 30m"},
		{8 * 3600, "8h"},
		{8*3600 + 59*60, "8h 59m"},
		{8*3600 + 59*60 + 30, "1d"}, // rounds up to 9h = 1d (Python reference agrees)
		{9 * 3600, "1d"},
		{9*3600 + 60, "1d 1m"},
		{17*3600 + 59*60, "1d 8h 59m"},
		{18 * 3600, "2d"},
		{44*3600 + 59*60, "4d 8h 59m"},
		{45 * 3600, "1w"},
		{54 * 3600, "1w 1d"},
		{90*3600 + 61*60, "2w 1h 1m"},
		{(45+18+3)*3600 + 15*60, "1w 2d 3h 15m"},
		{10*3600 + 56*60, "1d 1h 56m"},
	}
	for _, tc := range tests {
		if got := FormatDuration(tc.sec, 9); got != tc.want {
			t.Errorf("FormatDuration(%v, 9) = %q, want %q", tc.sec, got, tc.want)
		}
	}
	if got := FormatDuration(8*3600, 8); got != "1d" {
		t.Errorf("hoursPerDay 8: %q", got)
	}
	if got := FormatDuration(7.5*3600, 7.5); got != "1d" {
		t.Errorf("hoursPerDay 7.5: %q", got)
	}
	if got := FormatDuration(9*3600, 0); got != "1d 1h" {
		t.Errorf("hoursPerDay 0 must fall back to 8h days: %q", got)
	}
}
