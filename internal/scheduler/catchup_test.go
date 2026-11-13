package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestTickCallsCatchUpAfterTheWorklogWhileConfigured(t *testing.T) {
	var calls []string
	c := cfg()
	s := New(fixedClock{at(11, 18, 0)}, Hooks{
		Config:      func() Config { return c },
		LastRunDate: func() string { return "" },
		RunWorklog: func(context.Context, time.Time) error {
			calls = append(calls, "worklog")
			return nil
		},
		PollComments: func(context.Context) error { calls = append(calls, "poll"); return nil },
		CatchUp: func(context.Context) error {
			calls = append(calls, "catchup")
			return errors.New("jira down") // logged, nothing else happens
		},
	})
	s.Tick(context.Background())
	if got := len(calls); got != 3 || calls[0] != "worklog" || calls[1] != "catchup" {
		t.Fatalf("calls %v", calls)
	}
	calls = nil
	c.Configured = false
	s.Tick(context.Background())
	for _, x := range calls {
		if x == "catchup" {
			t.Error("catch-up while not configured")
		}
	}
}

func TestTickWithoutCatchUpHook(t *testing.T) {
	s := New(fixedClock{at(11, 9, 0)}, Hooks{
		Config:       cfg,
		LastRunDate:  func() string { return "" },
		RunWorklog:   func(context.Context, time.Time) error { return nil },
		PollComments: func(context.Context) error { return nil },
	})
	s.Tick(context.Background()) // must not panic
}
