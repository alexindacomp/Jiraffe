// Package scheduler decides when the daily worklog runs and when comments are
// polled. The clock is injected so the decisions are unit-testable.
package scheduler

import (
	"context"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"jiraffe/internal/worklog"
)

// Clock returns the current time.
type Clock interface{ Now() time.Time }

// SystemClock is the real clock (local time).
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }

// Config is the part of the settings the scheduler needs.
type Config struct {
	Configured     bool
	WorklogEnabled bool
	WorklogMinute  int // minutes since midnight of worklogTime
	Calendar       worklog.Calendar
	NotifyComments bool
	PollEvery      time.Duration
}

// worklogAt returns the scheduled worklog time on the day of t.
func worklogAt(t time.Time, minute int) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, minute/60, minute%60, 0, 0, t.Location())
}

// WorklogDue reports whether the scheduled worklog should run at now: worklog
// enabled and configured, today is a working day, now is at/after today's
// worklog time and it has not run successfully today (lastRunDate). This
// covers catch-up when the app starts later the same day; past days are never
// run.
func WorklogDue(now time.Time, cfg Config, lastRunDate string) bool {
	if !cfg.Configured || !cfg.WorklogEnabled {
		return false
	}
	if lastRunDate == now.Format(worklog.DateLayout) {
		return false
	}
	if ok, _ := cfg.Calendar.Check(now); !ok {
		return false
	}
	return !now.Before(worklogAt(now, cfg.WorklogMinute))
}

// NextWorklogAt returns when the next scheduled worklog will run. If it is due
// right now it returns now. ok is false when worklog is disabled/unconfigured
// or no working day exists within a year.
func NextWorklogAt(now time.Time, cfg Config, lastRunDate string) (time.Time, bool) {
	if !cfg.Configured || !cfg.WorklogEnabled {
		return time.Time{}, false
	}
	if WorklogDue(now, cfg, lastRunDate) {
		return now, true
	}
	today := now.Format(worklog.DateLayout)
	y, m, d := now.Date()
	for i := 0; i <= 366; i++ {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, now.Location())
		if ok, _ := cfg.Calendar.Check(day); !ok {
			continue
		}
		at := worklogAt(day, cfg.WorklogMinute)
		if i == 0 && (lastRunDate == today || !at.After(now)) {
			continue
		}
		return at, true
	}
	return time.Time{}, false
}

// NextWorklogAt is like the package function NextWorklogAt but knows the
// retry state: while waiting for a retry it returns the retry time, and once
// today's attempts are used up it returns the next working day's time.
func (s *Scheduler) NextWorklogAt(now time.Time, cfg Config, lastRunDate string) (time.Time, bool) {
	if !WorklogDue(now, cfg, lastRunDate) {
		return NextWorklogAt(now, cfg, lastRunDate)
	}
	today := now.Format(worklog.DateLayout)
	s.mu.Lock()
	attempts, retryAt := 0, time.Time{}
	if s.attemptsDate == today {
		attempts, retryAt = s.attempts, s.retryAt
	}
	s.mu.Unlock()
	switch {
	case attempts >= MaxDailyAttempts:
		// Gave up for today: behave as if today's run happened.
		return NextWorklogAt(now, cfg, today)
	case now.Before(retryAt):
		return retryAt, true
	default:
		return now, true
	}
}

// Retry policy for failed scheduled worklog runs.
const (
	RetryDelay       = 15 * time.Minute
	MaxDailyAttempts = 4
)

// Hooks are the actions the scheduler triggers.
type Hooks struct {
	Config      func() Config
	LastRunDate func() string
	// RunWorklog performs the scheduled run; due is the moment it was found
	// due (the run computes that day). It records LastRunDate on success.
	RunWorklog   func(ctx context.Context, due time.Time) error
	PollComments func(ctx context.Context) error
}

// Scheduler runs Hooks at the right time. Tick does one evaluation; Run
// calls Tick periodically until ctx is done.
type Scheduler struct {
	clock Clock
	hooks Hooks

	mu           sync.Mutex
	nextPoll     time.Time
	attemptsDate string
	attempts     int
	retryAt      time.Time
	poke         chan struct{}
}

// New creates a scheduler.
func New(clock Clock, hooks Hooks) *Scheduler {
	return &Scheduler{clock: clock, hooks: hooks, poke: make(chan struct{}, 1)}
}

// Poke requests an immediate re-evaluation (e.g. after settings changed) and
// makes the next comment poll happen right away.
func (s *Scheduler) Poke() {
	s.mu.Lock()
	s.nextPoll = time.Time{}
	s.mu.Unlock()
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// Tick evaluates the schedule once at the clock's current time.
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.clock.Now()
	cfg := s.hooks.Config()

	if s.worklogShouldRun(now, cfg) {
		err := s.safe(ctx, "worklog", func(ctx context.Context) error { return s.hooks.RunWorklog(ctx, now) })
		s.mu.Lock()
		if err != nil {
			s.retryAt = now.Add(RetryDelay)
		}
		s.mu.Unlock()
	}

	if cfg.Configured && cfg.NotifyComments && cfg.PollEvery > 0 {
		s.mu.Lock()
		due := !now.Before(s.nextPoll)
		if due {
			s.nextPoll = now.Add(cfg.PollEvery)
		}
		s.mu.Unlock()
		if due {
			_ = s.safe(ctx, "comment poll", s.hooks.PollComments)
		}
	}
}

func (s *Scheduler) worklogShouldRun(now time.Time, cfg Config) bool {
	if !WorklogDue(now, cfg, s.hooks.LastRunDate()) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	today := now.Format(worklog.DateLayout)
	if s.attemptsDate != today {
		s.attemptsDate, s.attempts, s.retryAt = today, 0, time.Time{}
	}
	if s.attempts >= MaxDailyAttempts || now.Before(s.retryAt) {
		return false
	}
	s.attempts++
	return true
}

// safe runs fn, converting panics into errors and logging failures.
func (s *Scheduler) safe(ctx context.Context, what string, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("scheduler: %s panicked: %v\n%s", what, r, debug.Stack())
			err = errPanic
		}
	}()
	if err = fn(ctx); err != nil {
		log.Printf("scheduler: %s failed: %v", what, err)
	}
	return err
}

type panicError struct{}

func (panicError) Error() string { return "internal error" }

var errPanic error = panicError{}

// Run ticks immediately (catch-up) and then every interval, or when poked,
// until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.poke:
		}
	}
}
