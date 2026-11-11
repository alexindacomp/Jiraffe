package app

import (
	"testing"
	"time"
)

func TestTruncatedChangelogIsFetchedInFull(t *testing.T) {
	// The search result's changelog misses the move to In Progress; the issue
	// endpoint (expand=changelog) has the full history.
	fj := &fakeJira{
		issues: `[{"key":"T-1","fields":{"summary":"Long history",
			"status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},"created":"2026-11-01T10:00:00.000+0200"},
			"changelog":{"startAt":0,"maxResults":1,"total":2,"histories":[
			  {"created":"2026-11-02T10:00:00.000+0200","items":[{"field":"status","fromString":"ToDo","toString":"Backlog"}]}]}}]`,
		issueBodies: map[string]string{"T-1": `{"key":"T-1","fields":{"status":{"name":"In Progress"},"created":"2026-11-01T10:00:00.000+0200"},
			"changelog":{"startAt":0,"maxResults":2,"total":2,"histories":[
			  {"created":"2026-11-02T10:00:00.000+0200","items":[{"field":"status","fromString":"ToDo","toString":"Backlog"}]},
			  {"created":"2026-11-11T14:00:00.000+0200","items":[{"field":"status","fromString":"Backlog","toString":"In Progress"}]}]}}`},
	}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	run := preview(t, a)
	if fj.issueCalls["T-1"] != 1 {
		t.Fatalf("full changelog fetched %d times", fj.issueCalls["T-1"])
	}
	// With the truncated history T-1 would look in progress all day (8h); the
	// full history says since 14:00 → 4h.
	if len(run.Items) != 1 || run.Items[0].TodoSeconds != 4*3600 {
		t.Errorf("items %+v", run.Items)
	}
}

func TestCompleteChangelogIsNotFetchedAgain(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	if run := preview(t, a); run.TotalSeconds != 8*3600 {
		t.Fatalf("run %+v", run)
	}
	if len(fj.issueCalls) != 0 {
		t.Errorf("issue endpoint called %v", fj.issueCalls)
	}
}
