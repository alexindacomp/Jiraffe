package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	_ "time/tzdata" // Europe/Kyiv without depending on the host's zoneinfo

	"jiraffe/internal/worklog"
)

// harness is a scheduler with a fake clock, a mutable config and a persisted
// "last run date" that a successful run updates (like the app does).
type harness struct {
	t       *testing.T
	clock   *fakeClock
	cfg     Config
	lastRun string
	runs    []string // tick times of worklog runs
	badDue  []string // runs whose due differed from the tick time
	fail    bool
	s       *Scheduler
}

func newHarness(t *testing.T, now time.Time, lastRun string) *harness {
	h := &harness{t: t, clock: &fakeClock{t: now}, cfg: cfg(), lastRun: lastRun}
	h.cfg.NotifyComments = false
	h.s = New(h.clock, Hooks{
		Config:      func() Config { return h.cfg },
		LastRunDate: func() string { return h.lastRun },
		RunWorklog: func(_ context.Context, due time.Time) error {
			h.runs = append(h.runs, h.clock.t.Format("2006-01-02 15:04"))
			if !due.Equal(h.clock.t) {
				h.badDue = append(h.badDue, fmt.Sprintf("due %s at tick %s", due, h.clock.t))
			}
			if h.fail {
				return errors.New("jira down")
			}
			h.lastRun = h.clock.t.Format(worklog.DateLayout)
			return nil
		},
		PollComments: func(context.Context) error { return nil },
	})
	return h
}

func (h *harness) tickAt(t time.Time) {
	h.clock.t = t
	h.s.Tick(context.Background())
	if len(h.badDue) > 0 {
		h.t.Fatalf("RunWorklog got the wrong due time: %v", h.badDue)
	}
}

func TestDueIsTheMomentTheRunWasFoundDue(t *testing.T) {
	// Time passes while the schedule is evaluated (slow state read across
	// midnight): the run must still get the moment it was found due.
	clock := &fakeClock{t: at(11, 23, 59)}
	var dues []time.Time
	s := New(clock, Hooks{
		Config: func() Config { c := cfg(); c.NotifyComments = false; return c },
		LastRunDate: func() string {
			clock.t = at(12, 0, 0).Add(time.Second)
			return ""
		},
		RunWorklog:   func(_ context.Context, due time.Time) error { dues = append(dues, due); return nil },
		PollComments: func(context.Context) error { return nil },
	})
	s.Tick(context.Background())
	if len(dues) != 1 || !dues[0].Equal(at(11, 23, 59)) {
		t.Fatalf("dues %v, want [2026-11-11 23:59]", dues)
	}
}

func TestPastDaysAreNeverRun(t *testing.T) {
	// The app was off on Tuesday evening; it starts on Wednesday morning.
	h := newHarness(t, at(11, 8, 0), "2026-11-09")
	for _, tm := range []time.Time{at(11, 8, 0), at(11, 12, 0), at(11, 17, 59)} {
		h.tickAt(tm)
	}
	if len(h.runs) != 0 {
		t.Fatalf("ran for a past day: %v", h.runs)
	}
	h.tickAt(at(11, 18, 0))
	h.tickAt(at(11, 23, 0))
	if len(h.runs) != 1 || h.runs[0] != "2026-11-11 18:00" {
		t.Fatalf("runs %v, want exactly today's", h.runs)
	}
}

func TestCatchUpOnLateStartSameDayOnly(t *testing.T) {
	h := newHarness(t, at(11, 21, 30), "2026-11-10")
	h.tickAt(at(11, 21, 30)) // started late: catch-up
	h.tickAt(at(11, 21, 31))
	h.tickAt(at(11, 23, 59))
	h.tickAt(at(13, 0, 1)) // Friday just after midnight (Thursday is a holiday): too early for Friday
	if len(h.runs) != 1 || h.runs[0] != "2026-11-11 21:30" {
		t.Fatalf("runs %v", h.runs)
	}
	// Started on a non-working day after the run time: nothing to catch up.
	h2 := newHarness(t, at(14, 20, 0), "2026-11-13")
	h2.tickAt(at(14, 20, 0))
	h2.tickAt(at(15, 20, 0))
	if len(h2.runs) != 0 {
		t.Fatalf("weekend catch-up: %v", h2.runs)
	}
}

func TestRestartAfterRunIsIdempotent(t *testing.T) {
	h := newHarness(t, at(11, 18, 0), "")
	h.tickAt(at(11, 18, 0))
	if len(h.runs) != 1 {
		t.Fatalf("runs %v", h.runs)
	}
	// "Restart": a new scheduler (fresh retry state) on the persisted last run date.
	h2 := newHarness(t, at(11, 19, 0), h.lastRun)
	h2.tickAt(at(11, 19, 0))
	h2.tickAt(at(11, 22, 0))
	if len(h2.runs) != 0 {
		t.Fatalf("ran again after restart: %v", h2.runs)
	}
	if next, ok := h2.s.NextWorklogAt(at(11, 19, 0), h2.cfg, h2.lastRun); !ok || !next.Equal(at(13, 18, 0)) {
		t.Errorf("next %v, want Friday 18:00 (Thursday is a holiday)", next)
	}
}

func TestWorklogTimeChangedMidDay(t *testing.T) {
	t.Run("moved earlier before it ran: runs at the new time", func(t *testing.T) {
		h := newHarness(t, at(11, 17, 0), "")
		h.tickAt(at(11, 17, 0))
		h.cfg.WorklogMinute = 17*60 + 30
		h.tickAt(at(11, 17, 29))
		h.tickAt(at(11, 17, 30))
		h.tickAt(at(11, 18, 0))
		if len(h.runs) != 1 || h.runs[0] != "2026-11-11 17:30" {
			t.Fatalf("runs %v", h.runs)
		}
	})
	t.Run("moved later after it ran: no second run", func(t *testing.T) {
		h := newHarness(t, at(11, 18, 0), "")
		h.tickAt(at(11, 18, 0))
		h.cfg.WorklogMinute = 19 * 60
		h.tickAt(at(11, 19, 0))
		if len(h.runs) != 1 {
			t.Fatalf("runs %v", h.runs)
		}
	})
	t.Run("moved later before it ran: waits for the new time", func(t *testing.T) {
		h := newHarness(t, at(11, 17, 0), "")
		h.cfg.WorklogMinute = 20 * 60
		h.tickAt(at(11, 18, 0))
		h.tickAt(at(11, 19, 59))
		if len(h.runs) != 0 {
			t.Fatalf("ran before the new time: %v", h.runs)
		}
		h.tickAt(at(11, 20, 0))
		if len(h.runs) != 1 {
			t.Fatalf("runs %v", h.runs)
		}
	})
	t.Run("moved to a time already passed: runs immediately", func(t *testing.T) {
		h := newHarness(t, at(11, 18, 30), "")
		h.cfg.WorklogMinute = 20 * 60
		h.tickAt(at(11, 18, 30))
		h.cfg.WorklogMinute = 18 * 60
		h.tickAt(at(11, 18, 31))
		if len(h.runs) != 1 || h.runs[0] != "2026-11-11 18:31" {
			t.Fatalf("runs %v", h.runs)
		}
	})
	t.Run("worklog disabled mid-day: no run", func(t *testing.T) {
		h := newHarness(t, at(11, 17, 0), "")
		h.cfg.WorklogEnabled = false
		h.tickAt(at(11, 18, 0))
		if len(h.runs) != 0 {
			t.Fatalf("runs %v", h.runs)
		}
	})
}

func TestRetryBudgetResetsOnTheNextWorkingDay(t *testing.T) {
	h := newHarness(t, at(11, 18, 0), "")
	h.fail = true
	for tm := at(11, 18, 0); tm.Before(at(11, 23, 59)); tm = tm.Add(5 * time.Minute) {
		h.tickAt(tm)
	}
	if len(h.runs) != MaxDailyAttempts {
		t.Fatalf("day 1 runs %v", h.runs)
	}
	h.fail = false
	h.tickAt(at(13, 18, 0)) // Friday
	if len(h.runs) != MaxDailyAttempts+1 || h.lastRun != "2026-11-13" {
		t.Fatalf("Friday did not get a fresh attempt: %v (last %q)", h.runs, h.lastRun)
	}
}

func TestCommentPollCadence(t *testing.T) {
	polls := 0
	clock := &fakeClock{t: at(11, 10, 0)}
	c := cfg()
	c.WorklogEnabled = false
	s := New(clock, Hooks{
		Config:       func() Config { return c },
		LastRunDate:  func() string { return "" },
		RunWorklog:   func(context.Context, time.Time) error { return nil },
		PollComments: func(context.Context) error { polls++; return errors.New("jira down") },
	})
	for _, m := range []int{0, 1, 4, 5, 6, 10} { // poll every 5 minutes, failures do not cause a storm
		clock.t = at(11, 10, m)
		s.Tick(context.Background())
	}
	if polls != 3 {
		t.Fatalf("polls=%d, want 3 (10:00, 10:05, 10:10)", polls)
	}
	s.Poke() // settings changed: poll right away
	s.Tick(context.Background())
	if polls != 4 {
		t.Fatalf("poke did not trigger a poll: %d", polls)
	}
	c.NotifyComments = false
	clock.t = at(11, 11, 0)
	s.Tick(context.Background())
	c.NotifyComments, c.Configured = true, false
	s.Tick(context.Background())
	if polls != 4 {
		t.Fatalf("polled while disabled/unconfigured: %d", polls)
	}
}

func TestDSTSwitchDaysInKyiv(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	c := cfg()
	c.Calendar = worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5, 6, 7}} // the switch days are Sundays
	tests := []struct {
		name    string
		now     time.Time
		lastRun string
		want    string
	}{
		{"spring: Saturday after run -> Sunday 18:00 EEST", time.Date(2026, 3, 28, 19, 0, 0, 0, kyiv), "2026-03-28", "2026-03-29T18:00:00+03:00"},
		{"spring: on the switch day before the run", time.Date(2026, 3, 29, 10, 0, 0, 0, kyiv), "2026-03-28", "2026-03-29T18:00:00+03:00"},
		{"autumn: Saturday after run -> Sunday 18:00 EET", time.Date(2026, 10, 24, 19, 0, 0, 0, kyiv), "2026-10-24", "2026-10-25T18:00:00+02:00"},
		{"autumn: on the switch day after the run -> Monday", time.Date(2026, 10, 25, 18, 30, 0, 0, kyiv), "2026-10-25", "2026-10-26T18:00:00+02:00"},
	}
	for _, tc := range tests {
		got, ok := NextWorklogAt(tc.now, c, tc.lastRun)
		if !ok || got.Format(time.RFC3339) != tc.want {
			t.Errorf("%s: got %s (%v), want %s", tc.name, got.Format(time.RFC3339), ok, tc.want)
		}
	}
	// Due at 18:00 local wall time on both switch days.
	for _, d := range []time.Time{time.Date(2026, 3, 29, 0, 0, 0, 0, kyiv), time.Date(2026, 10, 25, 0, 0, 0, 0, kyiv)} {
		y, m, dd := d.Date()
		if WorklogDue(time.Date(y, m, dd, 17, 59, 0, 0, kyiv), c, "") {
			t.Errorf("%s: due before 18:00", d.Format(worklog.DateLayout))
		}
		if !WorklogDue(time.Date(y, m, dd, 18, 0, 0, 0, kyiv), c, "") {
			t.Errorf("%s: not due at 18:00", d.Format(worklog.DateLayout))
		}
	}
	// Default Mon–Fri: Friday before the spring switch -> Monday 18:00 EEST.
	c.Calendar = worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5}}
	got, _ := NextWorklogAt(time.Date(2026, 3, 27, 19, 0, 0, 0, kyiv), c, "2026-03-27")
	if got.Format(time.RFC3339) != "2026-03-30T18:00:00+03:00" {
		t.Errorf("Friday -> Monday across DST: %s", got.Format(time.RFC3339))
	}
}

func TestNextWorklogAtSkipsVacationAcrossNewYear(t *testing.T) {
	c := cfg()
	c.Calendar = worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5}, Vacations: []worklog.DateRange{{From: "2026-12-24", To: "2027-01-08"}}}
	got, ok := NextWorklogAt(time.Date(2026, 12, 23, 19, 0, 0, 0, zone), c, "2026-12-23")
	if !ok || !got.Equal(time.Date(2027, 1, 11, 18, 0, 0, 0, zone)) {
		t.Errorf("got %v %v, want Monday 2027-01-11 18:00", got, ok)
	}
	c.Calendar = worklog.Calendar{} // no working days at all
	if _, ok := NextWorklogAt(at(11, 10, 0), c, ""); ok {
		t.Error("no working day within a year must report ok=false")
	}
}
