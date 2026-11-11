package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func boardKeys(b *Board) []string {
	var keys []string
	for _, c := range b.Columns {
		for _, is := range c.Issues {
			keys = append(keys, is.Key)
		}
	}
	return keys
}

func getBoardT(t *testing.T, a *App, params string) *Board {
	t.Helper()
	res, err := a.Call(context.Background(), "getBoard", json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	return res.(*Board)
}

func TestTrackedProjectsAreInitialisedLazilyAndPersisted(t *testing.T) {
	mine := fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "OPS-7", assignee: "alex"}, fx{key: "RBI-2", assignee: "alex"})
	fj := &fakeJira{issues: mine}
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)}
	a := openApp(t, dir, fj, clock, nil)
	if a.cfg.Get().TrackedProjects != nil {
		t.Fatal("must start uninitialised")
	}
	b := getBoardT(t, a, `{}`)
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,RBI" {
		t.Fatalf("tracked %q", got)
	}
	if len(boardKeys(b)) != 3 || countJQL(fj, specMyOpenJQL) != 1 {
		t.Errorf("board %v, init queries %d", boardKeys(b), countJQL(fj, specMyOpenJQL))
	}
	// Persisted: a restarted app does not initialise again.
	b2 := openApp(t, dir, fj, clock, nil)
	if got := strings.Join(b2.cfg.Get().TrackedProjects, ","); got != "OPS,RBI" {
		t.Errorf("after restart %q", got)
	}
	getBoardT(t, b2, `{"force":true}`)
	if _, err := b2.Call(context.Background(), "getProjects", nil); err != nil {
		t.Fatal(err)
	}
	if n := countJQL(fj, specMyOpenJQL); n != 2 { // the second one is getProjects' myOpen count
		t.Errorf("init repeated: %d my-open queries", n)
	}
	res, _ := b2.Call(context.Background(), "getSettings", nil)
	if js, _ := json.Marshal(res); !strings.Contains(string(js), `"trackedProjects":["OPS","RBI"]`) {
		t.Errorf("settings %s", js)
	}
}

func TestTrackedProjectsStayUninitialisedWithoutOpenIssues(t *testing.T) {
	fj := &fakeJira{myOpen: strp("[]"), routes: map[string]string{specBoardJQL: fxList(fx{key: "RBI-1", assignee: "alex", cat: "done"})}}
	a := v2App(t, fj, nil)
	b := getBoardT(t, a, `{}`)
	if a.cfg.Get().TrackedProjects != nil {
		t.Errorf("persisted %v", a.cfg.Get().TrackedProjects)
	}
	// Nothing to filter by: the "mine" board is not filtered, "all" is empty.
	if len(boardKeys(b)) != 1 {
		t.Errorf("mine board %v", boardKeys(b))
	}
	if all := getBoardT(t, a, `{"scope":"all"}`); len(all.Columns) != 0 || all.Scope != "all" {
		t.Errorf("all board %+v", all)
	}
}

func TestBoardScopesAreFilteredByTrackedProjects(t *testing.T) {
	allJQL := specAllJQL("OPS", "RBI")
	if got := AllBoardJQL([]string{"OPS", "RBI"}); got != allJQL {
		t.Fatalf("AllBoardJQL %q", got)
	}
	fj := &fakeJira{
		issues: fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "XYZ-1", assignee: "alex"}, fx{key: "OPS-2", assignee: "alex", cat: "indeterminate"}),
		routes: map[string]string{allJQL: fxList(
			fx{key: "RBI-1", assignee: "alex", epic: "RBI-100", sp: 3},
			fx{key: "RBI-5", assignee: "bob", typ: "story"},
			fx{key: "OPS-3"},
			fx{key: "XYZ-9", assignee: "bob"}, // an untracked project slipped in: post-filtered
		)},
		byKey: fxPool(fx{key: "RBI-100", typ: "epic", name: "Platform", color: "ghx-label-4"}),
	}
	a := v2App(t, fj, []string{"RBI", "OPS"})
	mine := getBoardT(t, a, `{"scope":"mine"}`)
	if got := strings.Join(boardKeys(mine), ","); got != "RBI-1,OPS-2" || mine.Scope != "mine" || mine.Truncated {
		t.Errorf("mine board %s %+v", got, mine)
	}
	all := getBoardT(t, a, `{"scope":"all"}`)
	if got := strings.Join(boardKeys(all), ","); got != "RBI-1,RBI-5,OPS-3" || all.Scope != "all" {
		t.Fatalf("all board %s", got)
	}
	is := map[string]IssueDTO{}
	for _, c := range all.Columns {
		for _, i := range c.Issues {
			is[i.Key] = i
		}
	}
	r1, r5, o3 := is["RBI-1"], is["RBI-5"], is["OPS-3"]
	if !r1.Mine || r1.Assignee == nil || r1.Assignee.Name != "alex" || r1.Type.Kind != KindTask || r1.Type.Name != "Задача" ||
		r1.StoryPoints == nil || *r1.StoryPoints != 3 || r1.Epic == nil || *r1.Epic != (EpicChipDTO{Key: "RBI-100", Name: "Platform", Color: "ghx-label-4"}) {
		t.Errorf("RBI-1 %+v (epic %+v)", r1, r1.Epic)
	}
	if r5.Mine || r5.Assignee.DisplayName != "Bob" || r5.Type.Kind != KindStory || r5.Epic != nil || r5.StoryPoints != nil {
		t.Errorf("RBI-5 %+v", r5)
	}
	if o3.Mine || o3.Assignee != nil || o3.ID == "" {
		t.Errorf("OPS-3 %+v", o3)
	}
	// JSON contract of an Issue.
	js, _ := json.Marshal(r1)
	for _, want := range []string{`"id":`, `"type":{"name":"Задача","kind":"task"}`, `"assignee":{"name":"alex","displayName":"Alex"}`,
		`"mine":true`, `"epic":{"key":"RBI-100","name":"Platform","color":"ghx-label-4"}`, `"storyPoints":3`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("issue JSON lacks %s: %s", want, js)
		}
	}

	// Cached per scope.
	before := fj.searches
	getBoardT(t, a, `{"scope":"mine"}`)
	getBoardT(t, a, `{"scope":"all"}`)
	getBoardT(t, a, `{}`)
	if fj.searches != before {
		t.Errorf("cached boards refetched: %d searches", fj.searches-before)
	}
	getBoardT(t, a, `{"scope":"all","force":true}`)
	if fj.searches != before+1 {
		t.Errorf("force: %d searches", fj.searches-before)
	}
	if _, err := a.Call(context.Background(), "getBoard", json.RawMessage(`{"scope":"everyone"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("bad scope: %v", err)
	}
}

func TestMineBoardIsTruncatedAt300(t *testing.T) {
	var items []fx
	for i := 1; i <= 301; i++ {
		items = append(items, fx{key: fmt.Sprintf("RBI-%d", i), assignee: "alex"})
	}
	fj := &fakeJira{issues: fxList(items...)}
	a := v2App(t, fj, []string{"RBI"})
	b := getBoardT(t, a, `{}`)
	if n := len(boardKeys(b)); n != 300 || !b.Truncated {
		t.Errorf("%d issues, truncated=%v", n, b.Truncated)
	}
}

func TestProjectsAndSetProjectTracked(t *testing.T) {
	fj := &fakeJira{
		issues: fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "RBI-2", assignee: "alex"}, fx{key: "OPS-1", assignee: "alex"}),
		projectsJSON: `[
		 {"id":"1","key":"XYZ","name":"Archive","projectCategory":{"name":"Old"}},
		 {"id":"2","key":"RBI","name":"BI","lead":{"name":"carol","displayName":"Carol"}},
		 {"id":"3","key":"OPS","name":"operations"},
		 {"id":"4","key":"ABC","name":"Alpha"}]`,
	}
	a := v2App(t, fj, nil)
	var events []string
	a.emit = func(typ string, _ any) { events = append(events, typ) }
	ctx := context.Background()
	res, err := a.Call(ctx, "getProjects", nil)
	if err != nil {
		t.Fatal(err)
	}
	list := func(ps []ProjectDTO) string {
		var out []string
		for _, p := range ps {
			out = append(out, fmt.Sprintf("%s:%v:%d", p.Key, p.Tracked, p.MyOpen))
		}
		return strings.Join(out, ",")
	}
	ps := res.([]ProjectDTO)
	if got := list(ps); got != "RBI:true:2,OPS:true:1,ABC:false:0,XYZ:false:0" {
		t.Errorf("projects %s", got)
	}
	if ps[0].Lead != "Carol" || ps[3].Category != "Old" {
		t.Errorf("lead/category %+v", ps)
	}
	if countJQL(fj, specMyOpenJQL) != 1 {
		t.Errorf("my-open queried %d times (counts and lazy init share one query)", countJQL(fj, specMyOpenJQL))
	}

	fj.routes[specAllJQL("OPS", "RBI")] = "[]"
	getBoardT(t, a, `{"scope":"all"}`) // cached until tracking changes
	res, err = a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"xyz","tracked":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := list(res.([]ProjectDTO)); got != "XYZ:true:0,RBI:true:2,OPS:true:1,ABC:false:0" {
		t.Errorf("after tracking XYZ: %s", got)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,RBI,XYZ" {
		t.Errorf("persisted %s", got)
	}
	if !strings.Contains(strings.Join(events, ","), "board") {
		t.Errorf("events %v", events)
	}
	fj.routes[specAllJQL("OPS", "RBI", "XYZ")] = "[]"
	getBoardT(t, a, `{"scope":"all"}`)
	if n := countJQL(fj, specAllJQL("OPS", "RBI", "XYZ")); n != 1 {
		t.Errorf("board cache not invalidated: %d", n)
	}
	if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"RBI","tracked":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,XYZ" {
		t.Errorf("after untracking %s", got)
	}
	for _, p := range []string{`{"key":"NOPE","tracked":true}`, `{"key":"A\") OR (1=1","tracked":true}`, `{"key":"","tracked":false}`} {
		if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(p)); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", p, err)
		}
	}
	// saveSettings cannot overwrite the list with a stale copy.
	if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"trackedProjects":null,"worklogTime":"18:30"}`)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "OPS,XYZ" {
		t.Errorf("saveSettings changed tracking: %s", got)
	}
}

func TestCommentAlertsOnlyForTrackedProjects(t *testing.T) {
	clock := &fakeClock{t: t0()}
	cj := &fakeJira{pollIssues: []pollIssue{}}
	a := openApp(t, t.TempDir(), cj, clock, nil)
	if _, err := a.cfg.UpdateTrackedProjects("https://sd.local", func([]string) []string { return []string{"A"} }); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil { // first poll
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
	if got := strings.Join(notificationIDs(a), ","); got != "1" {
		t.Fatalf("notifications %s, want only the tracked project's comment", got)
	}
	// Tracking C later does not replay its old comment.
	if _, err := a.cfg.UpdateTrackedProjects("https://sd.local", func([]string) []string { return []string{"A", "C"} }); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(5 * time.Minute)
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(notificationIDs(a), ","); got != "1" {
		t.Errorf("notifications %s", got)
	}
	if countJQL(cj, specMyOpenJQL) != 0 {
		t.Error("initialised tracked projects must not be queried again")
	}
}

func TestWorklogIgnoresTrackedProjects(t *testing.T) {
	fj := &fakeJira{issues: issuesJSON(allDayA1)}
	a := v2App(t, fj, []string{"RBI"}) // A is not tracked; 09:00–10:00 so far
	if run := preview(t, a); run.TotalSeconds != 3600 || len(run.Items) != 1 {
		t.Errorf("run %+v", run)
	}
}
