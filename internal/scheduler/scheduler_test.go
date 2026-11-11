package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"jiraffe/internal/worklog"
)

var zone = time.FixedZone("EET", 2*3600)

// 2026-11-11 is a Wednesday.
func at(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, zone) }

func cfg() Config {
	return Config{
		Configured: true, WorklogEnabled: true, WorklogMinute: 18 * 60,
		Calendar:       worklog.Calendar{WorkDays: []int{1, 2, 3, 4, 5}, Holidays: []string{"2026-11-12"}},
		NotifyComments: true, PollEvery: 5 * time.Minute,
	}
}

func TestWorklogDue(t *testing.T) {
	c := cfg()
	tests := []struct {
		name    string
		now     time.Time
		lastRun string
		cfg     Config
		want    bool
	}{
		{"before time", at(11, 17, 59), "", c, false},
		{"at time", at(11, 18, 0), "", c, true},
		{"catch-up later same day", at(11, 21, 30), "2026-11-10", c, true},
		{"already ran today", at(11, 18, 5), "2026-11-11", c, false},
		{"holiday", at(12, 18, 0), "", c, false},
		{"weekend", at(14, 18, 0), "", c, false},
		{"disabled", at(11, 18, 0), "", func() Config { x := c; x.WorklogEnabled = false; return x }(), false},
		{"not configured", at(11, 18, 0), "", func() Config { x := c; x.Configured = false; return x }(), false},
	}
	for _, tc := range tests {
		if got := WorklogDue(tc.now, tc.cfg, tc.lastRun); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestNextWorklogAt(t *testing.T) {
	c := cfg()
	tests := []struct {
		name    string
		now     time.Time
		lastRun string
		want    time.Time
	}{
		{"later today", at(11, 10, 0), "", at(11, 18, 0)},
		{"due now", at(11, 19, 0), "", at(11, 19, 0)},
		{"ran today, tomorrow is a holiday -> Friday", at(11, 19, 0), "2026-11-11", at(13, 18, 0)},
		{"Friday after run -> Monday", at(13, 18, 30), "2026-11-13", at(16, 18, 0)},
		{"Saturday -> Monday", at(14, 9, 0), "", at(16, 18, 0)},
	}
	for _, tc := range tests {
		got, ok := NextWorklogAt(tc.now, c, tc.lastRun)
		if !ok || !got.Equal(tc.want) {
			t.Errorf("%s: got %v (%v), want %v", tc.name, got, ok, tc.want)
		}
	}
	c.WorklogEnabled = false
	if _, ok := NextWorklogAt(at(11, 10, 0), c, ""); ok {
		t.Error("disabled worklog must have no next run")
	}
}

type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time { return f.t }

func TestTickRunsOncePerDayAndRetriesFailures(t *testing.T) {
	clock := &fakeClock{t: at(11, 17, 0)}
	lastRun := ""
	runs, polls := 0, 0
	fail := true
	s := New(clock, Hooks{
		Config:      cfg,
		LastRunDate: func() string { return lastRun },
		RunWorklog: func(context.Context, time.Time) error {
			runs++
			if fail {
				return errors.New("network down")
			}
			lastRun = clock.t.Format(worklog.DateLayout)
			return nil
		},
		PollComments: func(context.Context) error { polls++; return nil },
	})
	ctx := context.Background()

	s.Tick(ctx) // 17:00: not due
	if runs != 0 || polls != 1 {
		t.Fatalf("17:00 runs=%d polls=%d", runs, polls)
	}
	clock.t = at(11, 18, 0)
	s.Tick(ctx) // fails
	clock.t = at(11, 18, 5)
	s.Tick(ctx) // within retry delay: no run
	if runs != 1 {
		t.Fatalf("runs=%d, want 1 before retry delay", runs)
	}
	if polls != 3 {
		t.Errorf("polls=%d, want 3 (17:00, 18:00, 18:05)", polls)
	}
	fail = false
	clock.t = at(11, 18, 16)
	s.Tick(ctx) // retry succeeds
	clock.t = at(11, 18, 40)
	s.Tick(ctx) // already ran
	if runs != 2 || lastRun != "2026-11-11" {
		t.Fatalf("runs=%d lastRun=%q", runs, lastRun)
	}
}

func TestTickGivesUpAfterMaxAttempts(t *testing.T) {
	clock := &fakeClock{t: at(11, 18, 0)}
	runs := 0
	s := New(clock, Hooks{
		Config:       cfg,
		LastRunDate:  func() string { return "" },
		RunWorklog:   func(context.Context, time.Time) error { runs++; return errors.New("boom") },
		PollComments: func(context.Context) error { return nil },
	})
	for i := 0; i < 10; i++ {
		s.Tick(context.Background())
		clock.t = clock.t.Add(RetryDelay)
	}
	if runs != MaxDailyAttempts {
		t.Errorf("runs=%d, want %d", runs, MaxDailyAttempts)
	}
}

func TestTickRecoversPanics(t *testing.T) {
	clock := &fakeClock{t: at(11, 18, 0)}
	s := New(clock, Hooks{
		Config:       cfg,
		LastRunDate:  func() string { return "" },
		RunWorklog:   func(context.Context, time.Time) error { panic("bug") },
		PollComments: func(context.Context) error { panic("bug") },
	})
	s.Tick(context.Background()) // must not panic
}

func TestSchedulerNextWorklogAtFollowsRetries(t *testing.T) {
	clock := &fakeClock{t: at(11, 18, 0)}
	s := New(clock, Hooks{
		Config:       cfg,
		LastRunDate:  func() string { return "" },
		RunWorklog:   func(context.Context, time.Time) error { return errors.New("down") },
		PollComments: func(context.Context) error { return nil },
	})
	c := cfg()
	if got, _ := s.NextWorklogAt(clock.t, c, ""); !got.Equal(at(11, 18, 0)) {
		t.Fatalf("before first attempt: %v", got)
	}
	s.Tick(context.Background()) // attempt 1 fails at 18:00
	clock.t = at(11, 18, 5)
	if got, _ := s.NextWorklogAt(clock.t, c, ""); !got.Equal(at(11, 18, 15)) {
		t.Errorf("while waiting for retry: %v, want 18:15", got)
	}
	for i := 1; i < MaxDailyAttempts; i++ {
		clock.t = clock.t.Add(RetryDelay)
		s.Tick(context.Background())
	}
	// Tomorrow (12th) is a holiday in cfg(), so the next run is Friday 13th.
	if got, _ := s.NextWorklogAt(clock.t, c, ""); !got.Equal(at(13, 18, 0)) {
		t.Errorf("after giving up: %v, want Friday 18:00", got)
	}
}
