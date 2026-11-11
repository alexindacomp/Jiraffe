package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// --- load factor -------------------------------------------------------------

// Re-running the worklog with a load factor never logs more than the
// effective cap; raising the factor later tops the day up, lowering it never
// produces negative or extra worklogs.
func TestLoadFactorRerunsAreIdempotentAndFollowFactorChanges(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1), worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)},
		func(s *config.Settings) { s.LoadFactor = 0.5 })
	ctx := context.Background()
	run := func() worklog.Run {
		t.Helper()
		res, err := a.Call(ctx, "worklogRun", nil)
		if err != nil {
			t.Fatal(err)
		}
		r := res.(worklog.Run)
		if r.Error != "" {
			t.Fatalf("run error %q", r.Error)
		}
		return r
	}
	setFactor := func(lf string) {
		t.Helper()
		if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"loadFactor":`+lf+`}`)); err != nil {
			t.Fatal(err)
		}
	}

	if r := run(); r.TotalSeconds != 4*3600 || r.CapSeconds != 4*3600 {
		t.Fatalf("first run total %d cap %d", r.TotalSeconds, r.CapSeconds)
	}
	if r := run(); r.TotalSeconds != 0 || len(fj.posted) != 1 {
		t.Fatalf("re-run logged again: total %d, posted %v", r.TotalSeconds, fj.posted)
	}
	setFactor("0.75")
	if r := run(); r.TotalSeconds != 2*3600 || r.CapSeconds != 6*3600 || len(fj.posted) != 2 || !strings.HasPrefix(fj.posted[1], "A-1 7200 ") {
		t.Fatalf("after raising to 0.75: total %d cap %d posted %v", r.TotalSeconds, r.CapSeconds, fj.posted)
	}
	setFactor("0.25")
	r := run()
	if r.TotalSeconds != 0 || r.CapSeconds != 2*3600 || len(fj.posted) != 2 {
		t.Fatalf("after lowering to 0.25: total %d cap %d posted %v", r.TotalSeconds, r.CapSeconds, fj.posted)
	}
	if it := itemsByKey(r)["A-1"]; it.TodoSeconds != 0 || it.LoggedSeconds != 6*3600 {
		t.Errorf("item after lowering %+v", it)
	}
}

// Time logged elsewhere beyond the effective cap leaves nothing to log (no
// negative budget).
func TestLoadFactorCapAlreadyExceededElsewhere(t *testing.T) {
	fourHours := `[{"key":"X-9","fields":{"worklog":{"startAt":0,"maxResults":20,"total":1,"worklogs":[` +
		`{"author":{"name":"alex","key":"alex"},"started":"2026-11-11T09:00:00.000+0200","timeSpentSeconds":14400}]}}}]`
	fj := &fakeJira{issues: issuesJSON(allDayA1, allDayB2), capIssues: fourHours, worklogs: map[string][]jira.Worklog{}}
	a := openApp(t, t.TempDir(), fj, &fakeClock{t: time.Date(2026, 11, 11, 18, 0, 0, 0, zone)},
		func(s *config.Settings) { s.LoadFactor = 0.25 }) // cap 2h < 4h logged
	r := preview(t, a)
	if r.Error != "" || r.TotalSeconds != 0 || r.CapSeconds != 2*3600 || r.OtherLoggedSeconds != 4*3600 {
		t.Fatalf("run %+v", r)
	}
	for k, it := range itemsByKey(r) {
		if it.TodoSeconds != 0 {
			t.Errorf("%s todo %d", k, it.TodoSeconds)
		}
	}
}

// The settings form contract: loadFactor 0 is a value (not "missing"),
// absent keeps the stored value, exponent notation is a number.
func TestSaveSettingsLoadFactorZeroAbsentAndExponent(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{worklogs: map[string][]jira.Worklog{}}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	for _, tc := range []struct {
		params string
		want   float64
	}{
		{`{"loadFactor":0}`, 0},
		{`{"worklogTime":"18:30"}`, 0}, // absent: unchanged
		{`{"loadFactor":5e-1}`, 0.5},
		{`{"loadFactor":1}`, 1},
		{`{"loadFactor":0.05}`, 0.05},
	} {
		res, err := a.Call(ctx, "saveSettings", json.RawMessage(tc.params))
		if err != nil {
			t.Fatalf("%s: %v", tc.params, err)
		}
		if got := res.(SaveResult).LoadFactor; got != tc.want || a.cfg.Get().LoadFactor != tc.want {
			t.Errorf("%s: result %v stored %v, want %v", tc.params, got, a.cfg.Get().LoadFactor, tc.want)
		}
	}
	// A rejected loadFactor does not save the other fields of the form either.
	_, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"loadFactor":2,"worklogTime":"19:00"}`))
	if ToError(err).Code != CodeValidation || a.cfg.Get().WorklogTime != "18:30" {
		t.Errorf("partial save: %v, worklogTime %s", err, a.cfg.Get().WorklogTime)
	}
}

// --- tracked projects ----------------------------------------------------------

// Concurrent first calls of every tracked-projects consumer initialise the
// list once, and every query already uses it.
func TestLazyTrackedInitRunsOnceUnderConcurrentFirstCalls(t *testing.T) {
	const in = `project in ("OPS","RBI")`
	fj := &fakeJira{
		issues:       fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "OPS-2", assignee: "alex"}, fx{key: "OPS-3", assignee: "alex"}),
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"OPS","name":"Ops"}]`,
		routes: map[string]string{
			specAllJQL("OPS", "RBI"): "[]",
			`issuetype = Epic AND ` + in + ` AND statusCategory != Done ORDER BY key DESC`:                         "[]",
			`issuetype = Story AND ` + in + ` AND cf[10103] is EMPTY AND statusCategory != Done ORDER BY key DESC`: "[]",
			`issuetype = Story AND ` + in + ` AND statusCategory != Done ORDER BY key DESC`:                        "[]",
		},
	}
	a := v2App(t, fj, nil)
	ctx := context.Background()
	calls := []struct{ method, params string }{
		{"getBoard", `{}`}, {"getBoard", `{"scope":"all"}`}, {"getEpics", `{}`}, {"getStories", `{}`}, {"getAgileBoards", `{}`},
	}
	var wg sync.WaitGroup
	for round := 0; round < 2; round++ {
		for _, c := range calls {
			wg.Add(1)
			go func(method, params string) {
				defer wg.Done()
				if _, err := a.Call(ctx, method, json.RawMessage(params)); err != nil {
					t.Errorf("%s %s: %v", method, params, err)
				}
			}(c.method, c.params)
		}
	}
	wg.Wait()
	if n := countJQL(fj, specMyOpenJQL); n != 1 {
		t.Errorf("lazy init queried my open issues %d times", n)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,RBI" {
		t.Errorf("tracked %q", got)
	}
	for _, q := range fj.searched() {
		if strings.Contains(q, "project in (") && !strings.Contains(q, in) {
			t.Errorf("query with another project list: %s", q)
		}
	}
}

// The initial list keeps the 200 projects with most of my issues.
func TestLazyTrackedInitKeepsTheProjectsWithMostIssues(t *testing.T) {
	var items []fx
	items = append(items, fx{key: "AAA-1", assignee: "alex"}) // alphabetically first, fewest issues
	for i := 0; i < 200; i++ {
		for n := 1; n <= 2; n++ {
			items = append(items, fx{key: fmt.Sprintf("P%03d-%d", i, n), assignee: "alex"})
		}
	}
	fj := &fakeJira{myOpen: strp(fxList(items...)), issues: "[]"}
	a := v2App(t, fj, nil)
	getBoardT(t, a, `{}`)
	tp := a.cfg.Get().TrackedProjects
	if len(tp) != config.MaxTrackedProjects || tp[0] != "P000" || strings.Contains(strings.Join(tp, ","), "AAA") {
		t.Errorf("%d tracked, first %v, AAA dropped=%v", len(tp), tp[:1], !strings.Contains(strings.Join(tp, ","), "AAA"))
	}
}

// Untracking the last project leaves an initialised empty list: nothing is
// shown, and it is not re-initialised (not even after a restart).
func TestUntrackingEverythingStaysEmptyAfterRestart(t *testing.T) {
	fj := &fakeJira{
		issues:       fxList(fx{key: "RBI-1", assignee: "alex"}),
		projectsJSON: `[{"key":"RBI","name":"BI"}]`,
	}
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)}
	fj.typesJSON, fj.routes = fxIssueTypes, map[string]string{learnStoryJQL: fxLearnStory}
	a := openApp(t, dir, fj, clock, nil)
	ctx := context.Background()
	if b := getBoardT(t, a, `{}`); len(boardKeys(b)) != 1 {
		t.Fatalf("board before %v", boardKeys(b))
	}
	if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"RBI","tracked":false}`)); err != nil {
		t.Fatal(err)
	}
	if tp := a.cfg.Get().TrackedProjects; tp == nil || len(tp) != 0 {
		t.Fatalf("tracked %#v", tp)
	}
	if b := getBoardT(t, a, `{}`); len(boardKeys(b)) != 0 {
		t.Errorf("mine board after untracking everything %v", boardKeys(b))
	}
	if r := getEpicsT(t, a, `{}`); len(r.Epics) != 0 {
		t.Errorf("epics %+v", r)
	}
	initQueries := countJQL(fj, specMyOpenJQL)

	b := openApp(t, dir, fj, clock, nil)
	if tp := b.cfg.Get().TrackedProjects; tp == nil || len(tp) != 0 {
		t.Fatalf("after restart %#v", tp)
	}
	if bd := getBoardT(t, b, `{}`); len(boardKeys(bd)) != 0 {
		t.Errorf("board after restart %v", boardKeys(bd))
	}
	if n := countJQL(fj, specMyOpenJQL); n != initQueries {
		t.Errorf("re-initialised: %d → %d my-open queries", initQueries, n)
	}
}

// Tracking changes reach the "mine" board too (both scopes are filtered and
// both caches dropped).
func TestSetProjectTrackedRefiltersTheMineBoard(t *testing.T) {
	fj := &fakeJira{
		issues:       fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "OPS-2", assignee: "alex"}),
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"OPS","name":"Ops"}]`,
	}
	a := v2App(t, fj, []string{"OPS", "RBI"})
	if got := strings.Join(boardKeys(getBoardT(t, a, `{}`)), ","); got != "RBI-1,OPS-2" {
		t.Fatalf("before %s", got)
	}
	if _, err := a.Call(context.Background(), "setProjectTracked", json.RawMessage(`{"key":"OPS","tracked":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(boardKeys(getBoardT(t, a, `{}`)), ","); got != "RBI-1" {
		t.Errorf("mine board after untracking OPS: %s", got)
	}
	// Re-tracking (idempotent when repeated) brings it back.
	for i := 0; i < 2; i++ {
		if _, err := a.Call(context.Background(), "setProjectTracked", json.RawMessage(`{"key":"ops","tracked":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,RBI" {
		t.Errorf("tracked %s", got)
	}
	if got := strings.Join(boardKeys(getBoardT(t, a, `{}`)), ","); got != "RBI-1,OPS-2" {
		t.Errorf("after re-tracking %s", got)
	}
}

// A transition invalidates every cached issue list: both board scopes,
// epics and stories; a fetch that raced with an invalidation is not cached.
func TestTransitionInvalidatesBoardScopesAndTrees(t *testing.T) {
	allJQL := specAllJQL("RBI")
	fj := treeJira()
	fj.issues = fxList(fx{key: "RBI-12", assignee: "alex"})
	fj.routes[allJQL] = fxList(fx{key: "RBI-12", assignee: "alex"}, fx{key: "RBI-13", assignee: "carol"})
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	load := func() {
		t.Helper()
		getBoardT(t, a, `{}`)
		getBoardT(t, a, `{"scope":"all"}`)
		getEpicsT(t, a, `{}`)
		if _, err := a.Call(ctx, "getStories", nil); err != nil {
			t.Fatal(err)
		}
	}
	jqls := []string{specBoardJQL, allJQL, treeEpicsJQL, treeStoriesJQL}
	load()
	load() // cached
	for _, q := range jqls {
		if n := countJQL(fj, q); n != 1 {
			t.Errorf("%s: %d fetches before the transition", q, n)
		}
	}
	if _, err := a.Call(ctx, "transition", json.RawMessage(`{"key":"RBI-12","transitionId":"31"}`)); err != nil {
		t.Fatal(err)
	}
	load()
	for _, q := range jqls {
		if n := countJQL(fj, q); n != 2 {
			t.Errorf("%s: %d fetches, want a refetch after the transition", q, n)
		}
	}

	// An invalidation while the epics are being read: returned, not cached.
	var once sync.Once
	fj.searchHook = func() { once.Do(a.invalidateBoard) }
	getEpicsT(t, a, `{"force":true}`)
	fj.searchHook = nil
	getEpicsT(t, a, `{}`)
	if n := countJQL(fj, treeEpicsJQL); n != 4 {
		t.Errorf("epics fetched %d times, want 4 (the raced result must not be cached)", n)
	}
}

// The "all" board is cut at 500 issues and says so.
func TestAllBoardIsTruncatedAt500(t *testing.T) {
	var items []fx
	for i := 1; i <= 501; i++ {
		items = append(items, fx{key: fmt.Sprintf("RBI-%d", i), assignee: "bob"})
	}
	fj := &fakeJira{routes: map[string]string{specAllJQL("RBI"): fxList(items...)}}
	a := v2App(t, fj, []string{"RBI"})
	b := getBoardT(t, a, `{"scope":"all"}`)
	if n := len(boardKeys(b)); n != 500 || !b.Truncated {
		t.Errorf("%d issues, truncated=%v", n, b.Truncated)
	}
	if js, _ := json.Marshal(b); !strings.Contains(string(js), `"truncated":true`) || !strings.Contains(string(js), `"scope":"all"`) {
		t.Errorf("JSON %.200s", js)
	}
}

// --- comment alerts --------------------------------------------------------------

// A comment poll with trackedProjects = null initialises them and filters
// right away.
func TestCommentPollInitialisesTrackedProjects(t *testing.T) {
	clock := &fakeClock{t: t0()}
	cj := &fakeJira{pollIssues: []pollIssue{}, myOpen: strp(fxList(fx{key: "A-5", assignee: "alex"}))}
	a := openApp(t, t.TempDir(), cj, clock, nil)
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "A" {
		t.Fatalf("tracked %q after the first poll", got)
	}
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(
		pollIssue{key: "A-1", updated: clock.t, comments: []pollComment{{id: "1", author: "bob", created: clock.t.Add(-time.Minute)}}},
		pollIssue{key: "C-3", updated: clock.t, comments: []pollComment{{id: "2", author: "bob", created: clock.t.Add(-time.Minute)}}},
	)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "1" {
		t.Errorf("notifications %s", got)
	}
	if n := countJQL(cj, specMyOpenJQL); n != 1 {
		t.Errorf("my open issues queried %d times", n)
	}
}

// If the tracked projects cannot be determined, alerts are not lost.
func TestCommentPollAlertsForAllWhenTrackedProjectsUnknown(t *testing.T) {
	clock := &fakeClock{t: t0()}
	cj := &fakeJira{pollIssues: []pollIssue{}}
	cj.status = func(r *jira.Request) int {
		if u, _ := url.Parse(r.URL); u.Query().Get("jql") == specMyOpenJQL {
			return 500
		}
		return 0
	}
	a := openApp(t, t.TempDir(), cj, clock, nil)
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	clock.t = t0().Add(5 * time.Minute)
	cj.setPoll(
		pollIssue{key: "A-1", updated: clock.t, comments: []pollComment{{id: "1", author: "bob", created: clock.t.Add(-time.Minute)}}},
		pollIssue{key: "C-3", updated: clock.t, comments: []pollComment{{id: "2", author: "bob", created: clock.t.Add(-time.Minute)}}},
	)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "2,1" && got != "1,2" {
		t.Errorf("notifications %q, want both", got)
	}
	if a.cfg.Get().TrackedProjects != nil {
		t.Errorf("tracked %v", a.cfg.Get().TrackedProjects)
	}
}

// --- epic / story tree -----------------------------------------------------------

// A task with Epic Link E1 that is linked to a story of epic E2: an
// epic-level task of E1 and a task of the story under E2 (counted in both).
// Done stories inside a not-done epic are listed (done filter = top level).
func TestTreeTaskLinkedToStoryOfAnotherEpic(t *testing.T) {
	e1 := fx{key: "RBI-1", typ: "epic", cat: "indeterminate", assignee: "bob"}
	e2 := fx{key: "RBI-2", typ: "epic", cat: "new", assignee: "bob"}
	s20 := fx{key: "RBI-20", typ: "story", cat: "indeterminate", assignee: "bob", epic: "RBI-2", links: []string{"out:relates:RBI-12:task:new"}}
	s21 := fx{key: "RBI-21", typ: "story", cat: "done", assignee: "bob", epic: "RBI-2"}
	t12 := fx{key: "RBI-12", cat: "new", assignee: "alex", epic: "RBI-1", links: []string{"in:relates:RBI-20:story:indeterminate"}}
	fj := &fakeJira{routes: map[string]string{
		treeEpicsJQL:  fxList(e2, e1),
		treeOrphanJQL: "[]",
		treeChildJQL:  fxList(s21, s20, t12),
	}}
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 2 {
		t.Fatalf("epics %+v", r.Epics)
	}
	byKey := map[string]EpicNode{}
	for _, e := range r.Epics {
		byKey[e.Epic.Key] = e
	}
	en1, en2 := byKey["RBI-1"], byKey["RBI-2"]
	if refKeys(en1.Tasks) != "RBI-12" || len(en1.Stories) != 0 || en1.Progress != (Progress{Todo: 1, Total: 1}) {
		t.Errorf("E1 tasks %s stories %s progress %+v", refKeys(en1.Tasks), storyKeys(en1.Stories), en1.Progress)
	}
	if storyKeys(en2.Stories) != "RBI-20[RBI-12],RBI-21[]" || len(en2.Tasks) != 0 ||
		en2.Progress != (Progress{Done: 1, InProgress: 1, Todo: 1, Total: 3}) {
		t.Errorf("E2 stories %s tasks %s progress %+v", storyKeys(en2.Stories), refKeys(en2.Tasks), en2.Progress)
	}
	// Only mine: both epics have my task (E2 through its story).
	if m := getEpicsT(t, a, `{"scope":"mine"}`); len(m.Epics) != 2 {
		t.Errorf("mine: %d epics", len(m.Epics))
	}
}

// Self-links, mutual story links and a sub-task that is also linked:
// no duplicates, no stories as tasks, every descendant counted once.
// Tasks sort in progress → to do → done, then by key number descending.
func TestTreeSelfLinksCyclesAndSorting(t *testing.T) {
	s10 := fx{key: "RBI-10", typ: "story", cat: "new", assignee: "bob", epic: "RBI-1",
		links: []string{"out:relates:RBI-10:story:new", "out:blocks:RBI-20:story:new", "in:blocks:RBI-20:story:new",
			"out:relates:RBI-11:sub:new", "out:relates:RBI-11:sub:new"},
		subtasks: []string{"RBI-11:new"}}
	s20 := fx{key: "RBI-20", typ: "story", cat: "new", assignee: "bob", epic: "RBI-1",
		links: []string{"in:blocks:RBI-10:story:new", "out:blocks:RBI-10:story:new"}}
	t99 := fx{key: "RBI-99", cat: "new", epic: "RBI-1", links: []string{"out:relates:RBI-99:task:new"}}
	t100 := fx{key: "RBI-100", cat: "new", epic: "RBI-1"}
	t98 := fx{key: "RBI-98", cat: "indeterminate", epic: "RBI-1"}
	t101 := fx{key: "RBI-101", cat: "done", epic: "RBI-1"}
	fj := &fakeJira{
		routes: map[string]string{
			treeEpicsJQL:  fxList(fxRBI1),
			treeOrphanJQL: "[]",
			`cf[10103] in ("RBI-1") ORDER BY key DESC`: fxList(t101, t100, t99, t98, s20, s10),
		},
		byKey: fxPool(fx{key: "RBI-11", typ: "sub", cat: "new", assignee: "alex"}),
	}
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 1 {
		t.Fatalf("epics %+v", r.Epics)
	}
	e := r.Epics[0]
	if got := storyKeys(e.Stories); got != "RBI-20[],RBI-10[RBI-11]" {
		t.Errorf("stories %s", got)
	}
	if got := refKeys(e.Tasks); got != "RBI-98,RBI-100,RBI-99,RBI-101" {
		t.Errorf("tasks %s (in progress, to do by key desc numerically, done)", got)
	}
	if e.Progress != (Progress{Done: 1, InProgress: 1, Todo: 5, Total: 7}) {
		t.Errorf("progress %+v (2 stories + sub-task + 4 tasks)", e.Progress)
	}
	if len(e.Stories) != 2 || len(e.Stories[1].Tasks) != 1 {
		t.Fatalf("stories %s", storyKeys(e.Stories))
	}
	if sub := e.Stories[1].Tasks[0]; !sub.Mine || sub.Type.Kind != KindSubtask {
		t.Errorf("sub-task %+v", sub)
	}
}

// More than 1000 children: the first 1000 are shown and truncated is set.
func TestTreeChildrenTruncatedAt1000(t *testing.T) {
	var children []fx
	for i := 1002; i >= 2; i-- { // 1001 tasks
		children = append(children, fx{key: fmt.Sprintf("RBI-%d", i), epic: "RBI-1"})
	}
	fj := &fakeJira{routes: map[string]string{
		treeEpicsJQL:  fxList(fxRBI1),
		treeOrphanJQL: "[]",
		`cf[10103] in ("RBI-1") ORDER BY key DESC`: fxList(children...),
	}}
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 1 || !r.Truncated {
		t.Fatalf("truncated=%v epics %d", r.Truncated, len(r.Epics))
	}
	if len(r.Epics[0].Tasks) == 0 {
		t.Fatal("no tasks")
	}
	if e := r.Epics[0]; len(e.Tasks) != 1000 || e.Progress.Total != 1000 || e.Tasks[0].Key != "RBI-1002" {
		t.Errorf("%d tasks, progress %+v, first %s", len(e.Tasks), e.Progress, e.Tasks[0].Key)
	}
	// Exactly 1000 children is not truncated.
	fj.routes[`cf[10103] in ("RBI-1") ORDER BY key DESC`] = fxList(children[1:]...)
	r = getEpicsT(t, a, `{"force":true}`)
	if len(r.Epics) != 1 {
		t.Fatalf("1000 children: %d epics", len(r.Epics))
	}
	if r.Truncated || len(r.Epics[0].Tasks) != 1000 {
		t.Errorf("1000 children: truncated=%v tasks %d", r.Truncated, len(r.Epics[0].Tasks))
	}
}

// More than 300 stories: getStories is cut at 300 and says so.
func TestStoriesTruncatedAt300(t *testing.T) {
	var stories []fx
	for i := 301; i >= 1; i-- {
		stories = append(stories, fx{key: fmt.Sprintf("RBI-%d", i), typ: "story"})
	}
	fj := &fakeJira{routes: map[string]string{treeStoriesJQL: fxList(stories...)}}
	a := v2App(t, fj, []string{"RBI"})
	res, err := a.Call(context.Background(), "getStories", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(*StoriesResult); len(r.Stories) != 300 || !r.Truncated {
		t.Errorf("%d stories, truncated=%v", len(r.Stories), r.Truncated)
	}
}

// "Only mine" keeps an orphan story whose only link to me is a sub-task
// (its assignee is read by key, sub-tasks are embedded without one).
func TestMineKeepsOrphanStoryWithMySubtask(t *testing.T) {
	s30 := fx{key: "RBI-30", typ: "story", cat: "new", assignee: "bob", subtasks: []string{"RBI-32:new"}}
	s31 := fx{key: "RBI-31", typ: "story", cat: "new", assignee: "bob", subtasks: []string{"RBI-33:new"}}
	fj := &fakeJira{
		routes: map[string]string{treeEpicsJQL: "[]", treeOrphanJQL: fxList(s31, s30)},
		byKey:  fxPool(fx{key: "RBI-32", typ: "sub", assignee: "alex"}, fx{key: "RBI-33", typ: "sub", assignee: "bob"}),
	}
	a := v2App(t, fj, []string{"RBI"})
	if r := getEpicsT(t, a, `{"scope":"mine"}`); storyKeys(r.OrphanStories) != "RBI-30[RBI-32]" {
		t.Errorf("mine orphans %s", storyKeys(r.OrphanStories))
	}
	if r := getEpicsT(t, a, `{}`); storyKeys(r.OrphanStories) != "RBI-31[RBI-33],RBI-30[RBI-32]" {
		t.Errorf("all orphans %s", storyKeys(r.OrphanStories))
	}
}

// --- issue detail --------------------------------------------------------------------

func detailJira(body string) *fakeJira {
	return &fakeJira{issueBodies: map[string]string{"RBI-12": body}, comments2: map[string]int{"RBI-12": 3}, byKey: fxPool(fxRBI1)}
}

// A Jira without the Jira Software fields (or with /field forbidden):
// getIssue works, without epic, sprints and story points.
func TestGetIssueWithoutCustomFields(t *testing.T) {
	for name, setup := range map[string]func(*fakeJira){
		"no such fields": func(fj *fakeJira) { fj.fieldsJSON = `[]` },
		"field list 404": func(fj *fakeJira) {
			fj.status = func(r *jira.Request) int {
				if strings.HasSuffix(r.URL, "/rest/api/2/field") {
					return 404
				}
				return 0
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fj := detailJira(detailRBI12)
			setup(fj)
			a := v2App(t, fj, []string{"RBI"})
			d := getIssueT(t, a, "RBI-12")
			if d.Epic != nil || d.Issue.Epic != nil || d.StoryPoints != nil || d.Issue.StoryPoints != nil || len(d.Sprints) != 0 {
				t.Errorf("epic %+v sp %v sprints %+v", d.Epic, d.StoryPoints, d.Sprints)
			}
			if d.Issue.Key != "RBI-12" || len(d.Links) != 3 || len(d.Comments) != 3 || refKeys(d.Stories) != "RBI-10" {
				t.Errorf("rest of the detail %+v", d)
			}
			js, _ := json.Marshal(d)
			if !strings.Contains(string(js), `"sprints":[]`) || strings.Contains(string(js), `"storyPoints"`) || strings.Contains(string(js), `"epic"`) {
				t.Errorf("JSON %s", js)
			}
		})
	}
}

// Odd custom field values degrade to "no value".
func TestGetIssueOddCustomFieldValues(t *testing.T) {
	body := strings.NewReplacer(
		`"customfield_10103":"RBI-1"`, `"customfield_10103":"not a key"`,
		`"customfield_10130":5`, `"customfield_10130":"5"`,
	).Replace(detailRBI12)
	body = body[:strings.Index(body, `"customfield_10107":[`)] + `"customfield_10107":null},` + body[strings.Index(body, `"changelog":`):]
	a := v2App(t, detailJira(body), []string{"RBI"})
	d := getIssueT(t, a, "RBI-12")
	if d.Epic != nil || d.Issue.Epic != nil || d.StoryPoints != nil || len(d.Sprints) != 0 {
		t.Errorf("epic %+v sp %v sprints %+v", d.Epic, d.StoryPoints, d.Sprints)
	}
}

// More than 200 comments: the newest 200, oldest first. A failing comment
// read falls back to the embedded page.
func TestGetIssueCommentsPagingAndFallback(t *testing.T) {
	fj := detailJira(detailRBI12)
	fj.comments2["RBI-12"] = 250
	a := v2App(t, fj, []string{"RBI"})
	d := getIssueT(t, a, "RBI-12")
	if len(d.Comments) != 200 {
		t.Fatalf("%d comments, want 200", len(d.Comments))
	}
	if d.Comments[0].ID != "51" || d.Comments[199].ID != "250" {
		t.Fatalf("%d comments, %s … %s", len(d.Comments), d.Comments[0].ID, d.Comments[len(d.Comments)-1].ID)
	}
	if n := len(fj.requests("/rest/api/2/issue/RBI-12/comment?")); n < 5 {
		t.Errorf("%d comment pages (Jira serves 50 per page)", n)
	}

	fj2 := detailJira(detailRBI12)
	fj2.status = func(r *jira.Request) int {
		if strings.Contains(r.URL, "/comment?") {
			return 500
		}
		return 0
	}
	b := v2App(t, fj2, []string{"RBI"})
	if d := getIssueT(t, b, "RBI-12"); len(d.Comments) != 1 || d.Comments[0].Body != "only the first" {
		t.Errorf("fallback comments %+v", d.Comments)
	}
}

// A sub-task: parent set; a story linked twice (both directions) is one
// story but two links; a link without an issue is skipped.
func TestGetIssueSubtaskParentAndDuplicateStoryLinks(t *testing.T) {
	story := `{"key":"RBI-10","fields":{"summary":"Story","status":{"id":"1","name":"ToDo","statusCategory":{"key":"new"}},"issuetype":{"id":"10001","name":"История"}}}`
	body := `{"id":"5015","key":"RBI-15","changelog":{"histories":[]},"fields":{
	 "summary":"Sub","status":{"id":"1","name":"ToDo","statusCategory":{"key":"new"}},
	 "issuetype":{"id":"5","name":"Подзадача","subtask":true},"project":{"key":"RBI"},
	 "parent":{"key":"RBI-12","fields":{"summary":"Load data","status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},"issuetype":{"id":"3","name":"Задача"}}},
	 "issuelinks":[
	  {"id":"1","type":{"name":"Relates","inward":"relates to","outward":"relates to"},"outwardIssue":` + story + `},
	  {"id":"2","type":{"name":"Blocks","inward":"is blocked by","outward":"blocks"},"inwardIssue":` + story + `},
	  {"id":"3","type":{"name":"Broken","inward":"x","outward":"y"}}],
	 "subtasks":[],"comment":{"total":0,"comments":[]}}}`
	fj := &fakeJira{issueBodies: map[string]string{"RBI-15": body}}
	a := v2App(t, fj, []string{"RBI"})
	d := getIssueT(t, a, "RBI-15")
	if d.Issue.Type.Kind != KindSubtask || d.Parent == nil || d.Parent.Key != "RBI-12" || d.Parent.Type.Kind != KindTask {
		t.Errorf("parent %+v kind %s", d.Parent, d.Issue.Type.Kind)
	}
	if refKeys(d.Stories) != "RBI-10" || len(d.Links) != 2 || d.Links[0].Relation != "relates to" || d.Links[1].Relation != "is blocked by" {
		t.Errorf("stories %s links %+v", refKeys(d.Stories), d.Links)
	}
	if len(d.Children) != 0 || d.Watchers != nil || d.Labels == nil || d.Components == nil || d.FixVersions == nil {
		t.Errorf("children %v watchers %v labels %#v", d.Children, d.Watchers, d.Labels)
	}
	js, _ := json.Marshal(d)
	for _, want := range []string{`"labels":[]`, `"components":[]`, `"fixVersions":[]`, `"comments":[]`, `"changelog":[]`, `"children":[]`, `"timeTracking":{}`, `"description":""`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
}

// A story whose sub-task is also linked: one child, two kinds of relation.
func TestGetIssueStoryChildrenAreDeduplicated(t *testing.T) {
	s := fxRBI10
	s.links = append([]string{"out:relates:RBI-11:sub:done"}, s.links...)
	body := strings.Replace(s.json(), `"fields":{`, `"changelog":{"histories":[]},"fields":{`, 1)
	a := v2App(t, &fakeJira{issueBodies: map[string]string{"RBI-10": body}}, []string{"RBI"})
	d := getIssueT(t, a, "RBI-10")
	if refKeys(d.Children) != "RBI-11,RBI-12" || len(d.Links) != 3 {
		t.Errorf("children %s links %d", refKeys(d.Children), len(d.Links))
	}
}

// --- sprints -----------------------------------------------------------------------

// Fewer than 5 closed sprints, no open ones.
func TestSprintsFewClosed(t *testing.T) {
	fj := &fakeJira{
		sprints: map[int][]string{880: {sprintJSON(101, "closed"), sprintJSON(102, "closed")}},
	}
	a := v2App(t, fj, []string{"RBI"})
	res, err := a.Call(context.Background(), "getSprints", json.RawMessage(`{"boardId":880}`))
	if err != nil {
		t.Fatal(err)
	}
	if ss := res.([]SprintDTO); len(ss) != 2 || ss[0].ID != 102 || ss[1].ID != 101 {
		t.Errorf("sprints %+v", ss)
	}
}

// Story points of a sprint group: done points count only done issues;
// fractional points add up.
func TestSprintGroupStoryPointsAndFractions(t *testing.T) {
	sp := `"customfield_10107":["com.atlassian.greenhopper.service.sprint.Sprint@1[id=201,rapidViewId=880,state=ACTIVE,name=S201,startDate=2026-11-03T10:00:00.000+02:00,endDate=<null>,completeDate=<null>,goal=]"]`
	with := func(x fx) string { return strings.Replace(x.json(), `"fields":{`, `"fields":{`+sp+`,`, 1) }
	fj := &fakeJira{routes: map[string]string{
		`project in ("RBI") AND (sprint in openSprints() OR sprint in futureSprints()) ORDER BY Rank ASC`: "[" + strings.Join([]string{
			with(fx{key: "RBI-1", cat: "done", assignee: "alex", sp: 0.5}),
			with(fx{key: "RBI-2", cat: "done", assignee: "bob", sp: 8}),
			with(fx{key: "RBI-3", cat: "indeterminate", assignee: "alex", sp: 1.5}),
			with(fx{key: "RBI-4", cat: "new", assignee: "bob"}),
		}, ",") + "]",
		`project in ("RBI") AND statusCategory != Done AND (sprint is EMPTY OR sprint not in (openSprints(), futureSprints())) ORDER BY Rank ASC`: "[]",
	}}
	a := v2App(t, fj, []string{"RBI"})
	res, err := a.Call(context.Background(), "getSprintGroups", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := res.(*SprintGroups)
	if len(g.Groups) != 1 {
		t.Fatalf("groups %+v", g.Groups)
	}
	if sg := g.Groups[0]; sg.StoryPoints != (PointsDTO{Done: 8.5, Total: 10}) || sg.Progress != (Progress{Done: 2, InProgress: 1, Todo: 1, Total: 4}) {
		t.Errorf("points %+v progress %+v", sg.StoryPoints, sg.Progress)
	}
	if _, err := a.Call(context.Background(), "getSprintGroups", json.RawMessage(`{"scope":"team"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("bad scope: %v", err)
	}
}

// Tracking changes drop the cached boards and sprint lists.
func TestSetProjectTrackedDropsAgileCaches(t *testing.T) {
	fj := &fakeJira{
		agileBoards:  map[string]string{"RBI": `[{"id":880,"name":"BI","type":"scrum"}]`, "OPS": `[{"id":900,"name":"Ops","type":"scrum"}]`},
		sprints:      map[int][]string{880: {sprintJSON(201, "active")}},
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"OPS","name":"Ops"}]`,
	}
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	boards := func() string {
		t.Helper()
		res, err := a.Call(ctx, "getAgileBoards", nil)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, b := range res.([]AgileBoardDTO) {
			ids = append(ids, fmt.Sprint(b.ID))
		}
		return strings.Join(ids, ",")
	}
	if got := boards(); got != "880" {
		t.Fatalf("boards %s", got)
	}
	if _, err := a.Call(ctx, "getSprints", json.RawMessage(`{"boardId":880}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"OPS","tracked":true}`)); err != nil {
		t.Fatal(err)
	}
	if got := boards(); got != "900,880" {
		t.Errorf("boards after tracking OPS: %s", got)
	}
	before := len(fj.requests("/rest/agile/1.0/board/880/sprint?"))
	if _, err := a.Call(ctx, "getSprints", json.RawMessage(`{"boardId":880}`)); err != nil {
		t.Fatal(err)
	}
	if len(fj.requests("/rest/agile/1.0/board/880/sprint?")) == before {
		t.Error("sprint list served from a cache that tracking should have dropped")
	}
}

// --- UI preferences ---------------------------------------------------------------

// The size limit counts UTF-8 bytes; non-ASCII content round-trips.
func TestUIPrefsSizeCountsBytesAndUnicodeRoundtrips(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	// 'я' is 2 bytes: (32768 − 8) / 2 + 1 runes exceed the limit by one byte.
	over := fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("я", (MaxUIPrefsBytes-len(`{"v":""}`))/2+1))
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":`+over+`}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("%d bytes accepted: %v", len(over), err)
	}
	prefs := `{"title":"Доска <b>&</b> \"x\"","expanded":{"RBI-1":true},"n":[1,2.5,null]}`
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":`+prefs+`}`)); err != nil {
		t.Fatal(err)
	}
	// Same JSON value (the transport may escape <, >, & as \u003c…).
	var got, want any
	if err := json.Unmarshal([]byte(uiPrefsJSON(t, a)), &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(prefs), &want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":{}}`)); err != nil {
		t.Fatal(err)
	}
	if got := uiPrefsJSON(t, a); got != `{}` {
		t.Errorf("empty object: %s", got)
	}
}

// --- issue type learning (QR-2) --------------------------------------------------------

// localizedEpic renders fixtures with the epic type named like on a
// non-English Jira, so only a learned type id can classify it.
func localizedEpic(s string) string {
	return strings.ReplaceAll(s, `"name":"Epic"`, `"name":"Эпик"`)
}

const typesWithoutEnglishEpic = `[{"id":"10000","name":"Эпик","subtask":false},{"id":"10001","name":"История","subtask":false},` +
	`{"id":"3","name":"Задача","subtask":false},{"id":"5","name":"Подзадача","subtask":true}]`

func epicDetailBody() string {
	return localizedEpic(strings.Replace(fxRBI1.json(), `"fields":{`, `"changelog":{"histories":[]},"fields":{`, 1))
}

// The Epic type id is learned from the "issuetype = Epic" query when
// /issuetype only has a localized name: the drawer and board cards say
// "epic".
func TestEpicTypeIdLearnedFromCanonicalQuery(t *testing.T) {
	fj := &fakeJira{
		typesJSON:   typesWithoutEnglishEpic,
		issues:      localizedEpic(fxList(fx{key: "RBI-1", typ: "epic", assignee: "alex"}, fx{key: "RBI-5", assignee: "alex"})),
		issueBodies: map[string]string{"RBI-1": epicDetailBody()},
		routes: map[string]string{
			learnEpicJQL:                            `[{"key":"RBI-7","fields":{"issuetype":{"id":"10000","name":"Эпик","subtask":false}}}]`,
			`cf[10103] = "RBI-1" ORDER BY key DESC`: fxList(fxRBI13),
		},
	}
	a := v2App(t, fj, []string{"RBI"})
	d := getIssueT(t, a, "RBI-1")
	if d.Issue.Type.Kind != KindEpic || d.Issue.Type.Name != "Эпик" || refKeys(d.Children) != "RBI-13" {
		t.Errorf("epic detail: type %+v children %s", d.Issue.Type, refKeys(d.Children))
	}
	kinds := map[string]string{}
	for _, c := range getBoardT(t, a, `{}`).Columns {
		for _, is := range c.Issues {
			kinds[is.Key] = is.Type.Kind
		}
	}
	if kinds["RBI-1"] != KindEpic || kinds["RBI-5"] != KindTask {
		t.Errorf("board kinds %v", kinds)
	}
}

// Without that query (Jira rejects it), the ids returned by the epics list
// of getEpics are learned and used by later calls.
func TestEpicTypeIdLearnedFromEpicsList(t *testing.T) {
	fj := &fakeJira{
		typesJSON:   typesWithoutEnglishEpic,
		issueBodies: map[string]string{"RBI-1": epicDetailBody()},
		routes: map[string]string{
			treeEpicsJQL:  localizedEpic(fxList(fxRBI1)),
			treeOrphanJQL: "[]",
			`cf[10103] in ("RBI-1") ORDER BY key DESC`: fxList(fxRBI13),
			`cf[10103] = "RBI-1" ORDER BY key DESC`:    fxList(fxRBI13),
		},
	}
	a := v2App(t, fj, []string{"RBI"})
	if d := getIssueT(t, a, "RBI-1"); d.Issue.Type.Kind != KindTask {
		t.Fatalf("precondition: nothing learned yet, kind %s", d.Issue.Type.Kind)
	}
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 1 || r.Epics[0].Epic.Type.Kind != KindEpic {
		t.Fatalf("epics %+v", r.Epics)
	}
	if d := getIssueT(t, a, "RBI-1"); d.Issue.Type.Kind != KindEpic || refKeys(d.Children) != "RBI-13" {
		t.Errorf("after getEpics: kind %s children %s", d.Issue.Type.Kind, refKeys(d.Children))
	}
}

// --- agile boards: partial results (QR-3) -----------------------------------------------

// cancellingTransport answers like the fake Jira, except that the board
// lists of the projects in block wait until the others are answered, then
// cancel the caller's context and fail with its error (a timeout midway).
type cancellingTransport struct {
	*fakeJira
	block    map[string]bool
	others   int // board lists to answer before cancelling
	answered chan struct{}
	cancel   context.CancelFunc
}

func (c *cancellingTransport) Do(ctx context.Context, r *jira.Request) (*jira.Response, error) {
	u, _ := url.Parse(r.URL)
	p := u.Query().Get("projectKeyOrId")
	if u.Path == "/rest/agile/1.0/board" && c.block[p] {
		for i := 0; i < c.others; i++ {
			<-c.answered
		}
		c.cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	resp, err := c.fakeJira.Do(ctx, r)
	if u.Path == "/rest/agile/1.0/board" && p != "" {
		c.answered <- struct{}{}
	}
	return resp, err
}

func agileApp(t *testing.T, tr *cancellingTransport, keys []string) *App {
	t.Helper()
	fj := tr.fakeJira
	fj.typesJSON, fj.routes, fj.worklogs = fxIssueTypes, map[string]string{learnStoryJQL: fxLearnStory}, map[string][]jira.Worklog{}
	var ps []string
	fj.agileBoards = map[string]string{}
	for i, k := range keys {
		ps = append(ps, fmt.Sprintf(`{"key":%q,"name":%q}`, k, k))
		fj.agileBoards[k] = fmt.Sprintf(`[{"id":%d,"name":"B%s","type":"scrum","location":{"projectKey":%q}}]`, 100+i, k, k)
	}
	fj.projectsJSON = "[" + strings.Join(ps, ",") + "]"
	clock := &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)}
	fj.clock = clock
	a := openApp(t, t.TempDir(), tr, clock, nil)
	if _, err := a.cfg.UpdateTrackedProjects("https://sd.local", func([]string) []string { return keys }); err != nil {
		t.Fatal(err)
	}
	return a
}

// A timeout after some projects were read returns their boards, uncached.
func TestAgileBoardsTimeoutReturnsPartialUncached(t *testing.T) {
	keys := []string{"P0", "P1", "P2", "P3"} // ≤ 4 workers: all requested at once
	tr := &cancellingTransport{fakeJira: &fakeJira{}, block: map[string]bool{"P2": true}, others: 3, answered: make(chan struct{}, 8)}
	a := agileApp(t, tr, keys)
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	res, err := a.Call(ctx, "getAgileBoards", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range res.([]AgileBoardDTO) {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	if got := strings.Join(ids, ","); got != "100,101,103" {
		t.Errorf("partial boards %s", got)
	}
	// Not cached: the next call reads the projects again (now completely).
	tr.block = nil
	boardLists := func() int {
		n := 0
		for _, r := range tr.requests("/rest/agile/1.0/board?") {
			if strings.Contains(r, "projectKeyOrId=") {
				n++
			}
		}
		return n
	}
	before := boardLists()
	if before != 3 {
		t.Errorf("%d board lists answered in the first call, want 3", before)
	}
	res, err = a.Call(context.Background(), "getAgileBoards", nil)
	if err != nil || len(res.([]AgileBoardDTO)) != 4 {
		t.Fatalf("second call %v %v", res, err)
	}
	if after := boardLists(); after != before+4 {
		t.Errorf("second call read %d board lists, want 4 (the partial result must not be cached)", after-before)
	}
}

// A timeout before any project was read is an error, not an empty list.
func TestAgileBoardsTimeoutWithoutBoardsIsAnError(t *testing.T) {
	keys := []string{"P0", "P1"}
	tr := &cancellingTransport{fakeJira: &fakeJira{}, block: map[string]bool{"P0": true, "P1": true}, answered: make(chan struct{}, 8)}
	a := agileApp(t, tr, keys)
	ctx, cancel := context.WithCancel(context.Background())
	tr.cancel = cancel
	res, err := a.Call(ctx, "getAgileBoards", nil)
	if err == nil {
		t.Fatalf("got %v, want an error", res)
	}
	// Nothing cached either.
	tr.block = nil
	if res, err := a.Call(context.Background(), "getAgileBoards", nil); err != nil || len(res.([]AgileBoardDTO)) != 2 {
		t.Errorf("second call %v %v", res, err)
	}
}
