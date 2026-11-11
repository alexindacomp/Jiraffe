package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"jiraffe/internal/jira"
	"jiraffe/internal/state"
)

func newPollApp(t *testing.T, now time.Time) (*App, *fakeJira, *fakePlatform, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: now}
	cj := &fakeJira{pollIssues: []pollIssue{}}
	a := openApp(t, t.TempDir(), cj, clock, nil)
	return a, cj, a.plat.(*fakePlatform), clock
}

func notificationIDs(a *App) []string {
	var ids []string
	for _, n := range a.st.Snapshot().Notifications {
		ids = append(ids, n.ID)
	}
	return ids
}

func t0() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, zone) }

func TestFirstPollDoesNotFlood(t *testing.T) {
	a, cj, plat, _ := newPollApp(t, t0())
	var issues []pollIssue
	for i := 0; i < 50; i++ {
		issues = append(issues, pollIssue{key: fmt.Sprintf("A-%d", i+1), updated: t0().Add(-time.Duration(i) * time.Second),
			comments: []pollComment{{id: fmt.Sprint(100 + i), author: "bob", created: t0().Add(-time.Minute)}}})
	}
	cj.setPoll(issues...)
	if err := a.PollComments(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := a.st.Snapshot()
	if len(snap.Notifications) != 0 || len(plat.notices) != 0 {
		t.Fatalf("first poll notified: %d notifications, %d balloons", len(snap.Notifications), len(plat.notices))
	}
	if len(snap.SeenComments) != 50 || snap.CommentsServer != "https://sd.local" || snap.CommentsSince == "" {
		t.Errorf("first poll state: seen=%d server=%q since=%q", len(snap.SeenComments), snap.CommentsServer, snap.CommentsSince)
	}
	if cj.lastCommentWindow() != 6 {
		t.Errorf("first window %dm, want commentPollMinutes+1 = 6", cj.lastCommentWindow())
	}
}

func TestOwnCommentsAreFilteredAndOthersNotifiedOnce(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil { // initial
		t.Fatal(err)
	}
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: clock.t.Add(-time.Minute), comments: []pollComment{
		{id: "1", author: "alex", created: clock.t.Add(-3 * time.Minute)},
		{id: "2", author: "bob", created: clock.t.Add(-2 * time.Minute)},
		{id: "3", author: "eve", created: clock.t.Add(-time.Minute)},
	}})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "3,2" {
		t.Fatalf("notifications %s, want 3,2 (newest first, mine filtered)", got)
	}
	if len(plat.notices) != 1 || !strings.Contains(plat.notices[0].title, "2") || plat.notices[0].url != "" {
		t.Errorf("want one summary balloon for 2 comments, got %+v", plat.notices)
	}
	// The same comments on the next poll are not notified again.
	clock.t = clock.t.Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: clock.t.Add(-time.Minute), comments: []pollComment{
		{id: "1", author: "alex", created: t0()}, {id: "2", author: "bob", created: t0()}, {id: "3", author: "eve", created: t0()},
	}})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notificationIDs(a)) != 2 || len(plat.notices) != 1 {
		t.Errorf("re-notified: %v, %d balloons", notificationIDs(a), len(plat.notices))
	}
}

func TestPausedNotificationsAreStoredWithoutBalloon(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	_ = a.PollComments(ctx)
	a.SetPaused(true)
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: clock.t, comments: []pollComment{{id: "9", author: "bob", created: clock.t.Add(-time.Minute)}}})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if len(notificationIDs(a)) != 1 || len(plat.notices) != 0 {
		t.Errorf("paused: notifications %v balloons %d", notificationIDs(a), len(plat.notices))
	}
}

func TestPollErrorDoesNotAdvanceTheHorizon(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	good := a.st.Snapshot().LastCommentPoll

	// Jira fails for 30 minutes (search 503, then myself 401).
	failOn := func(path string, code int) func(*jira.Request) int {
		return func(r *jira.Request) int {
			if strings.Contains(r.URL, path) {
				return code
			}
			return 0
		}
	}
	for i, fail := range []func(*jira.Request) int{failOn("/rest/api/2/search", 503), failOn("/rest/api/2/myself", 401)} {
		cj.status = fail
		a.invalidateCaches() // force a fresh /myself call
		clock.t = t0().Add(time.Duration(15*(i+1)) * time.Minute)
		cj.setPoll(pollIssue{key: "A-1", updated: clock.t, comments: []pollComment{{id: "7", author: "bob", created: clock.t.Add(-time.Minute)}}})
		if err := a.PollComments(ctx); err == nil {
			t.Fatalf("attempt %d: error swallowed", i)
		}
		snap := a.st.Snapshot()
		if snap.LastCommentPoll != good || len(snap.Notifications) != 0 || len(snap.SeenComments) != 0 {
			t.Fatalf("attempt %d: state changed on error: last=%s want %s", i, snap.LastCommentPoll, good)
		}
	}

	// Recovery: the window covers everything since the last good poll (30m + 2m slack).
	cj.status = nil
	a.invalidateCaches()
	clock.t = t0().Add(30 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: t0().Add(10 * time.Minute), comments: []pollComment{{id: "7", author: "bob", created: t0().Add(10 * time.Minute)}}})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if w := cj.lastCommentWindow(); w != 32 {
		t.Errorf("window %dm, want 32", w)
	}
	if got := notificationIDs(a); len(got) != 1 || got[0] != "7" || len(plat.notices) != 1 {
		t.Errorf("comment from the outage was lost: %v", got)
	}
	if a.st.Snapshot().LastCommentPoll != clock.t.Format(time.RFC3339) {
		t.Errorf("horizon not advanced after success: %s", a.st.Snapshot().LastCommentPoll)
	}
}

func TestPollWindowIsCappedAtOneWeek(t *testing.T) {
	a, cj, _, clock := newPollApp(t, t0())
	ctx := context.Background()
	_ = a.PollComments(ctx)
	clock.t = t0().Add(10 * 24 * time.Hour) // laptop was off for 10 days
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if w := cj.lastCommentWindow(); w != maxPollWindowMin {
		t.Errorf("window %dm, want %d", w, maxPollWindowMin)
	}
}

func TestHorizonAdvancesToLastFetchedIssueWhenTheCapIsHit(t *testing.T) {
	a, cj, _, clock := newPollApp(t, t0())
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil { // initial, nothing updated yet
		t.Fatal(err)
	}
	// While the app was closed for a day, 1200 of my issues were updated one
	// minute apart; issues #999-#1001 got comments from bob.
	const n = 1200
	var issues []pollIssue
	for i := 1; i <= n; i++ {
		is := pollIssue{key: fmt.Sprintf("A-%d", i), updated: t0().Add(time.Duration(i) * time.Minute)}
		if i >= 999 && i <= 1001 {
			is.comments = []pollComment{{id: fmt.Sprintf("c%d", i), author: "bob", created: is.updated.Add(-30 * time.Second)}}
		}
		issues = append(issues, is)
	}
	cj.setPoll(issues...)
	clock.t = t0().Add(24 * time.Hour)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	horizon := t0().Add(maxPollIssues * time.Minute) // updated time of issue #1000
	snap := a.st.Snapshot()
	if snap.LastCommentPoll != horizon.Format(time.RFC3339) {
		t.Fatalf("horizon %s, want %s (last fetched issue)", snap.LastCommentPoll, horizon.Format(time.RFC3339))
	}
	if got := strings.Join(notificationIDs(a), ","); got != "c1000,c999" {
		t.Fatalf("notifications %s, want c1000,c999", got)
	}
	if jqls := cj.searched(); !specCommentJQL.MatchString(jqls[len(jqls)-1]) {
		t.Errorf("poll JQL %q", jqls[len(jqls)-1])
	}

	// Next poll continues from the horizon and finds #1001 without repeating #999/#1000.
	clock.t = clock.t.Add(5 * time.Minute)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	wantWindow := int(clock.t.Sub(horizon).Minutes()) + 2
	if w := cj.lastCommentWindow(); w != wantWindow {
		t.Errorf("window %dm, want %d", w, wantWindow)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "c1001,c1000,c999" {
		t.Errorf("notifications %s, want c1001,c1000,c999", got)
	}
	if a.st.Snapshot().LastCommentPoll != clock.t.Format(time.RFC3339) {
		t.Errorf("horizon after catching up: %s", a.st.Snapshot().LastCommentPoll)
	}
}

func TestChangingJiraServerReinitializesWithoutFlood(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	_ = a.PollComments(ctx)
	if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"https://jira2.local","token":"tok2"}`)); err != nil {
		t.Fatal(err)
	}
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "B-1", updated: clock.t, comments: []pollComment{{id: "1", author: "bob", created: clock.t.Add(-time.Minute)}}})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	snap := a.st.Snapshot()
	if len(snap.Notifications) != 0 || len(plat.notices) != 0 || snap.CommentsServer != "https://jira2.local" {
		t.Errorf("server change flooded: %+v", snap.Notifications)
	}
	if cj.lastCommentWindow() != 6 {
		t.Errorf("window for a new server %dm, want 6 (old horizon must not apply)", cj.lastCommentWindow())
	}
}

func TestSeenSetIsCappedAndRecentIDsStaySeen(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	_ = a.PollComments(ctx)
	// More new comments than the seen-set cap arrive in one poll.
	total := state.MaxSeenComments + 50
	var cs []pollComment
	for i := 0; i < total; i++ {
		cs = append(cs, pollComment{id: fmt.Sprint(i), author: "bob", created: t0().Add(time.Minute)})
	}
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: clock.t, comments: cs})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	snap := a.st.Snapshot()
	if len(snap.SeenComments) != state.MaxSeenComments || len(snap.Notifications) != state.MaxNotifications {
		t.Fatalf("seen=%d notifications=%d", len(snap.SeenComments), len(snap.Notifications))
	}
	if snap.Notifications[0].ID != fmt.Sprint(total-1) || len(plat.notices) != 1 {
		t.Errorf("newest notification %s, balloons %d (want one summary)", snap.Notifications[0].ID, len(plat.notices))
	}
	// The most recent ids (still inside the cap) are never notified again.
	clock.t = clock.t.Add(5 * time.Minute)
	cj.setPoll(pollIssue{key: "A-1", updated: clock.t, comments: cs[total-state.MaxSeenComments:]})
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if len(plat.notices) != 1 || a.st.Snapshot().Notifications[0].ID != fmt.Sprint(total-1) {
		t.Errorf("recent comments re-notified: %d balloons", len(plat.notices))
	}
}

func TestTokenNeverAppearsInLogsOrState(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	const secret1, secret2 = "PAT-s3cr3t-0001", "PAT-s3cr3t-0002"
	fj := &fakeJira{issues: worklogIssues, comments: "[]"}
	dir := t.TempDir()
	a := openApp(t, dir, fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)}, nil)
	ctx := context.Background()

	if _, err := a.Call(ctx, "saveSettings", json.RawMessage(fmt.Sprintf(`{"jiraUrl":"https://sd.local","token":%q}`, secret1))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Call(ctx, "getBoard", json.RawMessage(`{"force":true}`)); err != nil || fj.lastToken != "Bearer "+secret1 {
		t.Fatalf("saved token not used: err=%v auth=%q", err, fj.lastToken)
	}
	res, err := a.Call(ctx, "testConnection", json.RawMessage(fmt.Sprintf(`{"jiraUrl":"https://sd.local","token":%q}`, secret2)))
	if b, _ := json.Marshal(res); err != nil || !strings.Contains(string(b), `"ok":true`) || fj.lastToken != "Bearer "+secret2 {
		t.Fatalf("testConnection: %s err=%v auth=%q", b, err, fj.lastToken)
	}

	// Failures that are logged: worklog POSTs, /myself, the comment poll, TLS.
	fj.status = func(r *jira.Request) int {
		if r.Method == "POST" {
			return 500
		}
		return 0
	}
	if err := a.ScheduledWorklog(ctx); err == nil {
		t.Fatal("expected failed POSTs")
	}
	fj.status = func(*jira.Request) int { return 401 }
	a.invalidateCaches()
	a.refreshUser(ctx)
	if err := a.PollComments(ctx); err == nil {
		t.Fatal("expected a 401")
	}
	fj.status = nil
	fj.fail = func(*jira.Request) error { return &jira.TransportError{Kind: jira.NetTLSUntrust, Message: "untrusted"} }
	if _, err := a.Call(ctx, "getBoard", json.RawMessage(`{"force":true}`)); err == nil {
		t.Fatal("expected a TLS error")
	}

	out := buf.String()
	if !strings.Contains(out, "worklog A-1: add") || !strings.Contains(out, "fetch current user") {
		t.Fatalf("expected the failures to be logged, got %q", out)
	}
	stateJSON, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || !strings.Contains(string(stateJSON), "history") {
		t.Fatalf("state.json: %v", err)
	}
	cfgJSON, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	for name, text := range map[string]string{"log": out, "state.json": string(stateJSON), "config.json": string(cfgJSON)} {
		for _, s := range []string{secret1, secret2, "Bearer"} {
			if strings.Contains(text, s) {
				t.Errorf("%s contains %q", name, s)
			}
		}
	}
}

func TestOldCommentsOnRecentlyUpdatedIssuesAreNotNotified(t *testing.T) {
	t.Run("created before tracking started", func(t *testing.T) {
		a, cj, plat, clock := newPollApp(t, t0())
		ctx := context.Background()
		if err := a.PollComments(ctx); err != nil { // first poll: nothing updated, tracking starts at t0
			t.Fatal(err)
		}
		clock.t = t0().Add(5 * time.Minute)
		cj.setPoll(pollIssue{key: "A-1", updated: clock.t, comments: []pollComment{
			{id: "old", author: "bob", created: t0().Add(-48 * time.Hour)}, // 2 days before tracking, < 7 days old
			{id: "new", author: "bob", created: t0().Add(2 * time.Minute)}, // control
		}})
		if err := a.PollComments(ctx); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(notificationIDs(a), ","); got != "new" || len(plat.notices) != 1 {
			t.Errorf("notifications %q, balloons %d", got, len(plat.notices))
		}
		expectSeen(t, a, "old", "new")
	})
	t.Run("older than a week", func(t *testing.T) {
		now := t0()
		a, cj, plat, _ := newPollApp(t, now)
		if err := a.st.Update(func(tx state.Tx) {
			tx.CommentsServer = "https://sd.local"
			tx.CommentsSince = now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
			tx.LastCommentPoll = now.Add(-5 * time.Minute).Format(time.RFC3339)
		}); err != nil {
			t.Fatal(err)
		}
		cj.setPoll(pollIssue{key: "A-1", updated: now, comments: []pollComment{
			{id: "ten-days", author: "bob", created: now.Add(-10 * 24 * time.Hour)}, // after tracking start, > 7 days
			{id: "six-days", author: "bob", created: now.Add(-6 * 24 * time.Hour)},  // control
		}})
		if err := a.PollComments(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(notificationIDs(a), ","); got != "six-days" || len(plat.notices) != 1 {
			t.Errorf("notifications %q, balloons %d", got, len(plat.notices))
		}
		expectSeen(t, a, "ten-days", "six-days")
	})
}

func expectSeen(t *testing.T, a *App, ids ...string) {
	t.Helper()
	_ = a.st.Update(func(tx state.Tx) {
		for _, id := range ids {
			if !tx.Seen(id) {
				t.Errorf("comment %s not marked seen", id)
			}
		}
	})
}

func TestNotificationsOfOnePollAreOrderedByCreation(t *testing.T) {
	a, cj, plat, clock := newPollApp(t, t0())
	ctx := context.Background()
	_ = a.PollComments(ctx)
	clock.t = t0().Add(10 * time.Minute)
	// Search order is A-1 then B-1; by creation time the comments interleave
	// and the newest one (a4, by Eve) is on the first issue.
	cj.setPoll(
		pollIssue{key: "A-1", updated: t0().Add(5 * time.Minute), comments: []pollComment{
			{id: "a1", author: "bob", created: t0().Add(1 * time.Minute)},
			{id: "a4", author: "eve", created: t0().Add(4 * time.Minute)},
		}},
		pollIssue{key: "B-1", updated: t0().Add(6 * time.Minute), comments: []pollComment{
			{id: "b2", author: "bob", created: t0().Add(2 * time.Minute)},
			{id: "b3", author: "bob", created: t0().Add(3 * time.Minute)},
		}},
	)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "a4,b3,b2,a1" {
		t.Errorf("notifications %s, want newest first a4,b3,b2,a1", got)
	}
	if len(plat.notices) != 1 || !strings.HasPrefix(plat.notices[0].text, "Eve · A-1: ") {
		t.Errorf("summary balloon must name the newest comment: %+v", plat.notices)
	}
}

func TestPlainText(t *testing.T) {
	cyr := func(n int) string { return strings.Repeat("ж", n) }
	tests := []struct {
		name, in, want string
	}{
		{"299 runes kept", cyr(299), cyr(299)},
		{"300 runes kept", cyr(300), cyr(300)},
		{"301 runes cut to 300 with an ellipsis", cyr(301), cyr(299) + "…"},
		{"mention", "hi [~alex], see", "hi @alex, see"},
		{"two mentions", "[~a][~b.c]", "@a@b.c"},
		{"bracket with a space is not a mention", "see [~a b] here", "see [~a b] here"},
		{"unclosed mention", "ping [~x and more", "ping [~x and more"},
		{"whitespace collapsed", "  line1\r\n\n\tline2   end  ", "line1 line2 end"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		got := PlainText(tc.in, 300)
		if got != tc.want {
			t.Errorf("%s: got %q (%d runes)", tc.name, got, utf8.RuneCountInString(got))
		}
		if n := utf8.RuneCountInString(got); n > 300 {
			t.Errorf("%s: %d runes", tc.name, n)
		}
	}
}
