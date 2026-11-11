package worklog

import (
	"testing"
	"time"
)

var kyiv = time.FixedZone("EET", 2*3600)

// at returns 2026-11-11 (a Wednesday) hh:mm in Kyiv; dayOffset shifts the date.
func at(dayOffset, hh, mm int) time.Time {
	return time.Date(2026, 11, 11+dayOffset, hh, mm, 0, 0, kyiv)
}

var defaultInProgress = NewStatusSet([]string{"In Progress", "В работе", "В роботі"})

func refDay(now time.Time) Day {
	end := at(0, 18, 0)
	if now.Before(end) {
		end = now
	}
	return Day{Start: at(0, 9, 0), End: end, HasLunch: true, LunchStart: at(0, 13, 0), LunchEnd: at(0, 14, 0)}
}

type issueSpec struct {
	key     string
	created time.Time
	status  string
	changes []StatusChange
	logged  int64
}

func plan(t *testing.T, now time.Time, issues []issueSpec) map[string]Planned {
	t.Helper()
	day := refDay(now)
	var cands []Candidate
	for _, is := range issues {
		spans := day.Clip(InProgressIntervals(is.created, is.status, is.changes, defaultInProgress, now))
		if TotalSeconds(spans) < MinSeconds {
			continue
		}
		cands = append(cands, Candidate{Key: is.key, Status: is.status, Spans: spans, Logged: is.logged})
	}
	out := map[string]Planned{}
	for _, p := range Plan(cands, 8*3600) {
		out[p.Key] = p
	}
	return out
}

// allDay is an issue taken into work yesterday and still in progress.
func allDay(key string) issueSpec {
	return issueSpec{key: key, created: at(-1, 10, 0), status: "In Progress",
		changes: []StatusChange{{At: at(-1, 11, 0), From: "ToDo", To: "In Progress"}}}
}

func h(hours, minutes int) int64 { return int64(hours*3600 + minutes*60) }

func expectTodo(t *testing.T, got map[string]Planned, want map[string]int64) {
	t.Helper()
	for k, w := range want {
		p, ok := got[k]
		if !ok {
			if w == 0 {
				continue
			}
			t.Errorf("%s: missing from plan, want %s", k, FormatDuration(float64(w), 9))
			continue
		}
		if p.Todo != w {
			t.Errorf("%s: todo %s (%ds), want %s (%ds)", k, FormatDuration(float64(p.Todo), 9), p.Todo, FormatDuration(float64(w), 9), w)
		}
	}
	for k, p := range got {
		if _, ok := want[k]; !ok && p.Todo != 0 {
			t.Errorf("%s: unexpected todo %ds", k, p.Todo)
		}
	}
}

func TestReferenceScenarios(t *testing.T) {
	now := at(0, 18, 0)
	tests := []struct {
		name        string
		issues      []issueSpec
		want        map[string]int64
		wantStarted map[string]time.Time
	}{
		{
			name:        "A in progress since yesterday, still open at 18:00",
			issues:      []issueSpec{allDay("A")},
			want:        map[string]int64{"A": h(8, 0)},
			wantStarted: map[string]time.Time{"A": at(0, 9, 0)},
		},
		{
			name:   "A and B both in progress all day",
			issues: []issueSpec{allDay("A"), allDay("B")},
			want:   map[string]int64{"A": h(4, 0), "B": h(4, 0)},
		},
		{
			name: "A all day, B 15:00-17:00",
			issues: []issueSpec{allDay("A"), {key: "B", created: at(-3, 9, 0), status: "ForReview", changes: []StatusChange{
				{At: at(0, 15, 0), From: "ToDo", To: "In Progress"},
				{At: at(0, 17, 0), From: "In Progress", To: "ForReview"},
			}}},
			want:        map[string]int64{"A": h(7, 0), "B": h(1, 0)},
			wantStarted: map[string]time.Time{"B": at(0, 15, 0)},
		},
		{
			name: "A 10:00-15:30 then ForReview (lunch excluded)",
			issues: []issueSpec{{key: "A", created: at(-2, 9, 0), status: "ForReview", changes: []StatusChange{
				{At: at(0, 10, 0), From: "ToDo", To: "В работе"},
				{At: at(0, 15, 30), From: "В работе", To: "ForReview"},
			}}},
			want:        map[string]int64{"A": h(4, 30)},
			wantStarted: map[string]time.Time{"A": at(0, 10, 0)},
		},
		{
			name: "A taken 13:30, ForReview 16:00",
			issues: []issueSpec{{key: "A", created: at(-2, 9, 0), status: "ForReview", changes: []StatusChange{
				{At: at(0, 13, 30), From: "ToDo", To: "In Progress"},
				{At: at(0, 16, 0), From: "In Progress", To: "ForReview"},
			}}},
			want:        map[string]int64{"A": h(2, 0)},
			wantStarted: map[string]time.Time{"A": at(0, 14, 0)},
		},
		{
			name: "A,B all day, C from 12:00, A already logged 2h",
			issues: []issueSpec{
				func() issueSpec { a := allDay("A"); a.logged = h(2, 0); return a }(),
				allDay("B"),
				{key: "C", created: at(-1, 9, 0), status: "In Progress", changes: []StatusChange{
					{At: at(0, 12, 0), From: "ToDo", To: "In Progress"},
				}},
			},
			want: map[string]int64{"A": h(1, 10), "B": h(3, 10), "C": h(1, 40)},
		},
		{
			name: "A,B all day, B already logged 6h: cap leaves 2h for A",
			issues: []issueSpec{
				allDay("A"),
				func() issueSpec { b := allDay("B"); b.logged = h(6, 0); return b }(),
			},
			want: map[string]int64{"A": h(2, 0), "B": 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := plan(t, now, tc.issues)
			expectTodo(t, got, tc.want)
			for k, ws := range tc.wantStarted {
				if !got[k].Started.Equal(ws) {
					t.Errorf("%s: started %s, want %s", k, got[k].Started.Format("15:04"), ws.Format("15:04"))
				}
			}
		})
	}
}

func TestSharesOfParallelIssues(t *testing.T) {
	// A,B all day, C from 12:00: shares are 3h10m, 3h10m, 1h40m (= 8h total).
	got := plan(t, at(0, 18, 0), []issueSpec{allDay("A"), allDay("B"), {key: "C", created: at(-1, 9, 0), status: "In Progress",
		changes: []StatusChange{{At: at(0, 12, 0), From: "ToDo", To: "In Progress"}}}})
	want := map[string]float64{"A": float64(h(3, 10)), "B": float64(h(3, 10)), "C": float64(h(1, 40))}
	for k, w := range want {
		if d := got[k].Share - w; d > 0.001 || d < -0.001 {
			t.Errorf("%s share %.3f, want %.0f", k, got[k].Share, w)
		}
	}
}

func TestRunTwiceLogsNothing(t *testing.T) {
	now := at(0, 18, 0)
	scenarios := [][]issueSpec{
		{allDay("A")},
		{allDay("A"), allDay("B"), {key: "C", created: at(-1, 9, 0), status: "In Progress",
			changes: []StatusChange{{At: at(0, 12, 0), From: "ToDo", To: "In Progress"}}}},
		{allDay("A"), func() issueSpec { b := allDay("B"); b.logged = h(6, 0); return b }()},
		{allDay("A"), allDay("B"), allDay("C")}, // 8h/3 is not a whole number of minutes
	}
	for i, issues := range scenarios {
		first := plan(t, now, issues)
		var total int64
		for j := range issues {
			issues[j].logged += first[issues[j].key].Todo
			total += issues[j].logged
		}
		if total > 8*3600 {
			t.Errorf("scenario %d: total logged %ds exceeds the cap", i, total)
		}
		second := plan(t, now, issues)
		for k, p := range second {
			if p.Todo >= MinSeconds {
				t.Errorf("scenario %d: second run would log %ds on %s", i, p.Todo, k)
			}
		}
	}
}

func TestBeforeWorkdayStartNothing(t *testing.T) {
	got := plan(t, at(0, 8, 30), []issueSpec{allDay("A")})
	if len(got) != 0 {
		t.Fatalf("expected nothing before 09:00, got %+v", got)
	}
}

func TestEarlyRunStopsAtNow(t *testing.T) {
	// Run at 16:30 with A in progress all day: 09-13 + 14-16:30 = 6h30m.
	got := plan(t, at(0, 16, 30), []issueSpec{allDay("A")})
	expectTodo(t, got, map[string]int64{"A": h(6, 30)})
}

func TestInProgressIntervals(t *testing.T) {
	now := at(0, 18, 0)
	t.Run("no changes, currently in progress: from created", func(t *testing.T) {
		got := InProgressIntervals(at(0, 11, 0), "в работе", nil, defaultInProgress, now)
		if len(got) != 1 || !got[0].Start.Equal(at(0, 11, 0)) || !got[0].End.Equal(now) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("no changes, not in progress", func(t *testing.T) {
		if got := InProgressIntervals(at(0, 11, 0), "ToDo", nil, defaultInProgress, now); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("created in progress, unsorted changes", func(t *testing.T) {
		changes := []StatusChange{
			{At: at(0, 15, 0), From: "ForReview", To: "IN PROGRESS"},
			{At: at(0, 12, 0), From: "In Progress", To: "ForReview"},
		}
		got := InProgressIntervals(at(0, 10, 0), "In Progress", changes, defaultInProgress, now)
		want := []Interval{{at(0, 10, 0), at(0, 12, 0)}, {at(0, 15, 0), now}}
		if len(got) != len(want) {
			t.Fatalf("got %+v", got)
		}
		for i := range want {
			if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
				t.Errorf("interval %d: got %v-%v", i, got[i].Start, got[i].End)
			}
		}
	})
	t.Run("unknown created: first in-progress stretch is dropped", func(t *testing.T) {
		changes := []StatusChange{{At: at(0, 12, 0), From: "In Progress", To: "Done"}}
		if got := InProgressIntervals(time.Time{}, "Done", changes, defaultInProgress, now); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

func TestClipWithoutLunch(t *testing.T) {
	d := Day{Start: at(0, 9, 0), End: at(0, 18, 0)}
	got := d.Clip([]Interval{{at(-1, 10, 0), at(0, 20, 0)}})
	if len(got) != 1 || got[0].Seconds() != 9*3600 {
		t.Fatalf("got %+v", got)
	}
}
