package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// QA tests for v0.3 (SPEC.md "# v0.3"): gaps left by v3_test.go.

// advance moves the app's fake clock forward.
func advance(t *testing.T, a *App, d time.Duration) {
	t.Helper()
	c, ok := a.clock.(*fakeClock)
	if !ok {
		t.Fatalf("clock %T is not a fakeClock", a.clock)
	}
	c.t = c.t.Add(d)
}

// searchJQL returns the JQL of a search request ("" for other requests).
func searchJQL(r *jira.Request) string {
	u, err := url.Parse(r.URL)
	if err != nil || u.Path != "/rest/api/2/search" {
		return ""
	}
	return u.Query().Get("jql")
}

func requestPath(r *jira.Request) string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return u.Path
}

// --- Board, project mode ---------------------------------------------------------

// Every layout column is in the JSON with "issues":[] when empty; with all
// issues placed there is no "other" column; a status listed in two board
// columns fills the first; an issue status without an id is placed by name.
func TestQAProjectBoardEmptyColumnsAndNoOtherColumn(t *testing.T) {
	fj := mbJira()
	fj.boardConfigs[673] = `{"id":673,"name":"ETL board","columnConfig":{"columns":[
	 {"name":"To Do","statuses":[{"id":"1"}]},
	 {"name":"Also ToDo","statuses":[{"id":"1"},{"id":"6"}]},
	 {"name":"In Work","statuses":[{"id":"3"}]},
	 {"name":"Rejected","statuses":[{"id":"10200"}]},
	 {"name":"Done","statuses":[{"id":"10"},{"id":"6000"}]}]}}`
	fj.routes[specMBAllJQL] = issueList(
		withStatus(fx{key: "MB-1"}, stToDo),
		withStatus(fx{key: "MB-2"}, `{"name":"In Progress","statusCategory":{"key":"indeterminate"}}`), // no id
		withStatus(fx{key: "MB-3"}, stDone), // done within 14 days: stays on the board
	)
	a := v2App(t, fj, []string{"MB"})
	b := getBoardT(t, a, `{"project":"MB","scope":"all"}`)

	want := strings.Join([]string{
		"col-0/To Do/new[1:ToDo:new]=MB-1",
		"col-1/Also ToDo/indeterminate[1:ToDo:new 6:ForReview:indeterminate]=",
		"col-2/In Work/indeterminate[3:In Progress:indeterminate]=MB-2",
		"col-3/Rejected/done[10200:Reject:done]=",
		"col-4/Done/done[10:Готово:done 6000:Закрыт:done]=MB-3",
	}, "\n")
	if got := columnsSummary(b.Columns); got != want {
		t.Errorf("columns:\n%s\nwant:\n%s", got, want)
	}

	js, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Columns []struct {
			ID       string          `json:"id"`
			Issues   json.RawMessage `json:"issues"`
			Statuses json.RawMessage `json:"statuses"`
			Status   json.RawMessage `json:"status"`
		} `json:"columns"`
	}
	if err := json.Unmarshal(js, &wire); err != nil {
		t.Fatal(err)
	}
	empty := map[string]bool{"col-1": true, "col-3": true}
	for _, c := range wire.Columns {
		if c.ID == otherColumnID {
			t.Errorf("an \"other\" column without issues: %s", js)
		}
		if empty[c.ID] && string(c.Issues) != "[]" {
			t.Errorf("empty column %s: issues %s, want []", c.ID, c.Issues)
		}
		if len(c.Statuses) == 0 || string(c.Statuses) == "null" || len(c.Status) == 0 {
			t.Errorf("column %s lacks statuses/status: %s", c.ID, js)
		}
	}
	if len(wire.Columns) != 5 {
		t.Errorf("%d columns in JSON, want 5", len(wire.Columns))
	}
}

// The "mine" project board follows a custom boardJql (its ORDER BY dropped,
// quoted "ORDER BY" kept), and saving a new boardJql drops the cached
// project board and the cached sprint groups at once.
func TestQAProjectBoardCustomBoardJQLAndInvalidation(t *testing.T) {
	const (
		custom   = `labels = "a b" AND summary ~ "x ORDER BY y" ORDER BY priority DESC`
		customQL = `project = "MB" AND ((labels = "a b" AND summary ~ "x ORDER BY y") OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d)) ORDER BY Rank ASC`
	)
	fj := mbJira()
	fj.routes[specMBMineJQL] = issueList(withStatus(fx{key: "MB-1", assignee: "alex"}, stToDo))
	fj.routes[customQL] = issueList(withStatus(fx{key: "MB-7", assignee: "bob"}, stInProg))
	fj.routes[specMBOpenJQL] = "[]"
	fj.routes[specMBBackJQL] = "[]"
	a := v2App(t, fj, []string{"MB"})
	ctx := context.Background()

	if keys := boardKeys(getBoardT(t, a, `{"project":"MB"}`)); strings.Join(keys, ",") != "MB-1" {
		t.Fatalf("default mine board %v", keys)
	}
	sprintGroupsT(t, a, `{"project":"MB"}`)

	params, _ := json.Marshal(map[string]string{"boardJql": custom})
	if _, err := a.Call(ctx, "saveSettings", params); err != nil {
		t.Fatal(err)
	}
	b := getBoardT(t, a, `{"project":"MB"}`) // not forced: the cache was dropped
	if keys := boardKeys(b); strings.Join(keys, ",") != "MB-7" {
		t.Errorf("board after boardJql change %v; queries %v", keys, fj.searched())
	}
	if b.Layout == nil || b.Layout.Source != LayoutAgile {
		t.Errorf("layout %+v", b.Layout)
	}
	sprintGroupsT(t, a, `{"project":"MB"}`)
	if n := countJQL(fj, specMBOpenJQL); n != 2 {
		t.Errorf("sprint groups after a boardJql change: open query ran %d times, want 2", n)
	}
	// The layout is metadata: a boardJql change does not re-read it.
	if n := len(fj.requests("/rest/agile/1.0/board/673/configuration")); n != 1 {
		t.Errorf("configuration read %d times", n)
	}
}

// Only a 400 means "no Rank": a server error of the ranked query is
// reported, not retried by updated, and nothing is cached.
func TestQAProjectBoardRankServerErrorIsNotRetried(t *testing.T) {
	fj := mbJira()
	byUpdated := strings.TrimSuffix(specMBAllJQL, rankOrder) + " ORDER BY updated DESC"
	var ranked atomic.Int32
	var failing atomic.Bool
	failing.Store(true)
	fj.status = func(r *jira.Request) int {
		if searchJQL(r) == specMBAllJQL {
			ranked.Add(1)
			if failing.Load() {
				return 500
			}
		}
		return 0
	}
	a := v2App(t, fj, []string{"MB"})
	_, err := a.Call(context.Background(), "getBoard", json.RawMessage(`{"project":"MB","scope":"all"}`))
	if err == nil {
		t.Fatal("a 500 of the board query was swallowed")
	}
	if countJQL(fj, byUpdated) != 0 {
		t.Errorf("retried by updated after a 500: %v", fj.searched())
	}
	failing.Store(false)
	b := getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	if len(boardKeys(b)) != 5 || ranked.Load() != 2 {
		t.Errorf("after recovery: keys %v, ranked queries %d", boardKeys(b), ranked.Load())
	}
}

// A project board with more than 500 issues is truncated at 500.
func TestQAProjectBoardTruncated(t *testing.T) {
	fj := mbJira()
	items := make([]string, 0, projectBoardMax+1)
	for i := 0; i <= projectBoardMax; i++ {
		items = append(items, withStatus(fx{key: fmt.Sprintf("MB-%d", 1000+i)}, stToDo))
	}
	fj.routes[specMBAllJQL] = issueList(items...)
	b := getBoardT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB","scope":"all"}`)
	if !b.Truncated || len(boardKeys(b)) != 500 {
		t.Errorf("truncated %v with %d issues, want true with 500", b.Truncated, len(boardKeys(b)))
	}
}

// A layout that fell back because of a transient failure is rebuilt after
// a minute; one that fell back for a permanent reason (404) is kept for an
// hour, while the issues are re-read every minute.
func TestQAProjectLayoutRetryAfterTransientFailure(t *testing.T) {
	const configPath = "/rest/agile/1.0/board/673/configuration"
	t.Run("transient", func(t *testing.T) {
		fj := mbJira()
		var failing atomic.Bool
		failing.Store(true)
		fj.status = func(r *jira.Request) int {
			if requestPath(r) == configPath && failing.Load() {
				return 500
			}
			return 0
		}
		a := v2App(t, fj, []string{"MB"})
		b := getBoardT(t, a, `{"project":"MB","scope":"all"}`)
		if b.Layout.Source != LayoutWorkflow || b.Layout.BoardID != 673 {
			t.Fatalf("with a failing configuration: %+v", b.Layout)
		}
		failing.Store(false)
		advance(t, a, metaRetryTTL+time.Second)
		if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Layout.Source != LayoutAgile {
			t.Errorf("not rebuilt after a transient failure: %+v", b.Layout)
		}
	})
	t.Run("permanent", func(t *testing.T) {
		fj := mbJira()
		delete(fj.boardConfigs, 673) // 404
		a := v2App(t, fj, []string{"MB"})
		if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Layout.Source != LayoutWorkflow {
			t.Fatalf("layout %+v", b.Layout)
		}
		fj.mu.Lock()
		fj.boardConfigs[673] = mbConfig673
		fj.mu.Unlock()
		advance(t, a, metaRetryTTL+time.Second) // the board (60 s) expired, the layout not
		if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Layout.Source != LayoutWorkflow {
			t.Errorf("a permanent fallback was rebuilt after a minute: %+v", b.Layout)
		}
		if n := countJQL(fj, specMBAllJQL); n != 2 {
			t.Errorf("issues read %d times, want 2 (board TTL expired)", n)
		}
		if n := len(fj.requests(configPath)); n != 1 {
			t.Errorf("configuration read %d times within the hour", n)
		}
		advance(t, a, layoutTTL)
		if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Layout.Source != LayoutAgile {
			t.Errorf("not rebuilt after an hour: %+v", b.Layout)
		}
	})
}

// Concurrent boards of one project share a single layout build; boards of
// different projects run alongside (run with -race).
func TestQAConcurrentProjectBoards(t *testing.T) {
	const cmaAll = `project = "CMA" AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`
	fj := mbJira()
	fj.routes[specMBMineJQL] = issueList(withStatus(fx{key: "MB-1", assignee: "alex"}, stToDo))
	fj.agileBoards["CMA"] = `[{"id":937,"name":"CMA dev board","type":"kanban","location":{"projectKey":"CMA"}}]`
	fj.boardConfigs[937] = `{"id":937,"columnConfig":{"columns":[{"name":"New","statuses":[{"id":"1"}]},{"name":"Test","statuses":[{"id":"7"}]}]}}`
	fj.projectStatuses["CMA"] = "[" + typeStatuses(false, stToDo, stTest) + "]"
	fj.routes[cmaAll] = issueList(withStatus(fx{key: "CMA-1"}, stTest))

	const workers = 8
	var started sync.WaitGroup
	started.Add(workers)
	allStarted := make(chan struct{})
	go func() { started.Wait(); close(allStarted) }()
	var once sync.Once
	// The first MB layout build waits until every caller has started, so
	// the others find it in progress.
	fj.fail = func(r *jira.Request) error {
		if requestPath(r) == "/rest/api/2/project/MB/statuses" {
			once.Do(func() { <-allStarted })
		}
		return nil
	}
	a := v2App(t, fj, []string{"MB", "CMA"})
	// Warm the project list so all callers go straight to the layout.
	if _, err := a.Call(context.Background(), "getProjects", nil); err != nil {
		t.Fatal(err)
	}

	type res struct {
		params string
		b      *Board
		err    error
	}
	out := make(chan res, workers)
	for i := 0; i < workers; i++ {
		params := []string{`{"project":"MB","scope":"all"}`, `{"project":"MB","scope":"mine"}`, `{"project":"CMA","scope":"all"}`}[i%3]
		go func() {
			started.Done()
			r, err := a.Call(context.Background(), "getBoard", json.RawMessage(params))
			b, _ := r.(*Board)
			out <- res{params, b, err}
		}()
	}
	for i := 0; i < workers; i++ {
		r := <-out
		if r.err != nil {
			t.Errorf("%s: %v", r.params, r.err)
			continue
		}
		wantSource, wantProject := LayoutAgile, "MB"
		if strings.Contains(r.params, "CMA") {
			wantProject = "CMA"
		}
		if r.b.Layout == nil || r.b.Layout.Source != wantSource || r.b.Layout.Project != wantProject {
			t.Errorf("%s: layout %+v", r.params, r.b.Layout)
		}
	}
	if n := len(fj.requests("/rest/agile/1.0/board/673/configuration")); n != 1 {
		t.Errorf("MB configuration read %d times by concurrent boards, want 1", n)
	}
	if n := len(fj.requests("/rest/api/2/project/MB/statuses")); n != 1 {
		t.Errorf("MB statuses read %d times, want 1", n)
	}
	if n := len(fj.requests("/rest/agile/1.0/board/937/configuration")); n != 1 {
		t.Errorf("CMA configuration read %d times, want 1", n)
	}
}

// --- parameters through the dispatcher -------------------------------------------

func TestQAProjectParamsThroughCall(t *testing.T) {
	fj := mbJira()
	fj.routes[specMBOpenJQL] = "[]"
	fj.routes[specMBBackJQL] = "[]"
	fj.routes[`issuetype = Epic AND project = "MB" AND statusCategory != Done ORDER BY key DESC`] = "[]"
	fj.routes[`issuetype = Story AND project = "MB" AND cf[10103] is EMPTY AND statusCategory != Done ORDER BY key DESC`] = "[]"
	fj.routes[`issuetype = Story AND project = "MB" AND statusCategory != Done ORDER BY key DESC`] = "[]"
	a := v2App(t, fj, []string{"RBI"}) // MB accessible, not tracked
	ctx := context.Background()
	methods := []string{"getBoard", "getSprintGroups", "getEpics", "getStories"}

	// Wrongly typed parameters are bad params, before any Jira search.
	for _, m := range methods {
		for _, p := range []string{`{"project":5}`, `{"project":["MB"]}`, `{"force":"yes","project":"MB"}`, `{"scope":"team","project":"MB"}`} {
			if _, err := a.Call(ctx, m, json.RawMessage(p)); ToError(err).Code != CodeBadParams {
				t.Errorf("%s %s: %v", m, p, err)
			}
		}
	}
	for _, p := range []string{`{"project":"MB","boardId":"673"}`, `{"project":"MB","boardId":1.5}`} {
		if _, err := a.Call(ctx, "getBoard", json.RawMessage(p)); ToError(err).Code != CodeBadParams {
			t.Errorf("getBoard %s: %v", p, err)
		}
	}
	if _, err := a.Call(ctx, "getSprintGroups", json.RawMessage(`{"project":"MB","includeClosed":"yes"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("includeClosed as a string: %v", err)
	}
	if q := fj.searched(); len(q) != 0 {
		t.Errorf("searches for invalid parameters: %v", q)
	}

	// An accessible, untracked project in any case and with blanks is
	// accepted by all four methods and queried as its upper-case key.
	for _, m := range methods {
		if _, err := a.Call(ctx, m, json.RawMessage(`{"project":"  mb ","scope":"all"}`)); err != nil {
			t.Errorf("%s untracked MB: %v", m, err)
		}
	}
	for _, q := range []string{specMBAllJQL, specMBOpenJQL, specMBBackJQL,
		`issuetype = Epic AND project = "MB" AND statusCategory != Done ORDER BY key DESC`,
		`issuetype = Story AND project = "MB" AND statusCategory != Done ORDER BY key DESC`} {
		if countJQL(fj, q) != 1 {
			t.Errorf("query not sent once: %s\nall: %v", q, fj.searched())
		}
	}
	if b := getBoardT(t, a, `{"project":"mb","scope":"all"}`); b.Layout == nil || b.Layout.Project != "MB" {
		t.Errorf("layout %+v", b.Layout)
	}
}

// The error for an inaccessible project follows the UI language chosen in
// the settings (TestProjectParamValidation covers the default language).
func TestQAProjectUnavailableIsLocalized(t *testing.T) {
	a := v2App(t, mbJira(), []string{"MB"})
	ctx := context.Background()
	for _, lang := range []string{"ru", "uk", "en"} {
		if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"language":"`+lang+`"}`)); err != nil {
			t.Fatal(err)
		}
		for _, m := range []string{"getBoard", "getSprintGroups", "getEpics", "getStories"} {
			_, err := a.Call(ctx, m, json.RawMessage(`{"project":"NOPE"}`))
			if e := a.ErrorFor(err); e.Code != CodeProjectUnavailable || e.Message != T(lang, "err_project_unavailable") {
				t.Errorf("%s %s: %+v", lang, m, e)
			}
		}
	}
}

// --- sprint groups ------------------------------------------------------------------

// The scope is part of the sprint groups cache key, and an accessible
// untracked project given in lower case is queried as its key.
func TestQASprintGroupsScopeCacheKeyAndUntrackedProject(t *testing.T) {
	mine := func(jql string) string { return strings.Replace(jql, rankOrder, mineClause+rankOrder, 1) }
	fj := sprintJira()
	fj.routes[mine(specMBOpenJQL)] = issueList(withSprints(fx{key: "MB-4", assignee: "alex"}, stToDo,
		legacySprint(202, "ACTIVE", "2026-10-27T10:00:00.000+02:00", "")))
	fj.routes[mine(specMBBackJQL)] = "[]"
	a := v2App(t, fj, []string{"RBI"}) // MB is not tracked
	if g := sprintGroupsT(t, a, `{"project":"mb","scope":"mine"}`); groupsSummary(g.Groups) != "202:active=MB-4" || g.Backlog != nil {
		t.Errorf("mine: %s backlog %v", groupsSummary(g.Groups), g.Backlog != nil)
	}
	if g := sprintGroupsT(t, a, `{"project":"MB","scope":"all"}`); len(g.Groups) != 5 || g.Backlog == nil {
		t.Errorf("all after mine: %s", groupsSummary(g.Groups))
	}
	sprintGroupsT(t, a, `{"project":"MB","scope":"mine"}`)
	sprintGroupsT(t, a, `{"project":"MB"}`) // scope defaults to "all"
	if countJQL(fj, specMBOpenJQL) != 1 || countJQL(fj, mine(specMBOpenJQL)) != 1 {
		t.Errorf("open queries: all %d, mine %d; want 1 each", countJQL(fj, specMBOpenJQL), countJQL(fj, mine(specMBOpenJQL)))
	}
}

// Sprint groups are cached for 2 minutes (SPEC, written out).
func TestQASprintGroupsTTL(t *testing.T) {
	fj := sprintJira()
	a := v2App(t, fj, []string{"MB"})
	sprintGroupsT(t, a, `{"project":"MB"}`)
	advance(t, a, 2*time.Minute-time.Second)
	sprintGroupsT(t, a, `{"project":"MB"}`)
	if n := countJQL(fj, specMBOpenJQL); n != 1 {
		t.Errorf("re-read before the TTL: %d open queries", n)
	}
	advance(t, a, 2*time.Second)
	sprintGroupsT(t, a, `{"project":"MB"}`)
	if n := countJQL(fj, specMBOpenJQL); n != 2 {
		t.Errorf("not re-read after the TTL: %d open queries", n)
	}
}

// A cancellation while the closed sprints are read fails the call instead
// of returning a result without them.
func TestQASprintGroupsCancelledDuringClosedSprints(t *testing.T) {
	fj := includeClosedJira()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fj.fail = func(r *jira.Request) error {
		if requestPath(r) == "/rest/agile/1.0/board/673/sprint" {
			cancel()
			return &jira.TransportError{Kind: jira.NetTimeout, Message: "cancelled"}
		}
		return nil
	}
	a := v2App(t, fj, []string{"MB"})
	res, err := a.Call(ctx, "getSprintGroups", json.RawMessage(`{"project":"MB","includeClosed":true}`))
	if err == nil {
		t.Errorf("cancelled call returned %+v", res)
	}
	if countJQL(fj, specMBOpenJQL) != 1 {
		t.Fatalf("the closed part was not reached: %v", fj.searched())
	}
}

// Without a project the closed sprints come from the boards of all tracked
// projects (a shared board's sprint once), queried for all of them.
func TestQASprintGroupsAllProjectsIncludeClosed(t *testing.T) {
	const (
		open = `project in ("MB","RBI") AND (sprint in openSprints() OR sprint in futureSprints()) ORDER BY Rank ASC`
		back = `project in ("MB","RBI") AND statusCategory != Done AND (sprint is EMPTY OR sprint not in (openSprints(), futureSprints())) ORDER BY Rank ASC`
	)
	closedQL := func(id int) string {
		return fmt.Sprintf(`project in ("MB","RBI") AND sprint = %d ORDER BY Rank ASC`, id)
	}
	fj := &fakeJira{
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"MB","name":"MB"}]`,
		agileBoards: map[string]string{
			"MB":  `[{"id":673,"name":"ETL","type":"scrum","location":{"projectKey":"MB"}},{"id":880,"name":"BI DEV","type":"scrum","location":{"projectKey":"RBI"}}]`,
			"RBI": `[{"id":880,"name":"BI DEV","type":"scrum","location":{"projectKey":"RBI"}}]`,
		},
		sprints: map[int][]string{
			673: {closedSprintJSON(103, 3), closedSprintJSON(104, 6)},
			880: {closedSprintJSON(104, 6), closedSprintJSON(107, 8), closedSprintJSON(108, 1)},
		},
		routes: map[string]string{
			open:          issueList(withSprints(fx{key: "RBI-1"}, stToDo, legacySprint(300, "FUTURE", "", ""))),
			back:          issueList(withStatus(fx{key: "MB-9"}, stToDo)),
			closedQL(107): issueList(withStatus(fx{key: "RBI-7"}, stDone)),
			closedQL(104): issueList(withStatus(fx{key: "MB-4"}, stDone), withStatus(fx{key: "RBI-4"}, stToDo)),
			closedQL(103): issueList(withStatus(fx{key: "MB-3"}, stDone)),
		},
	}
	a := v2App(t, fj, []string{"MB", "RBI"})
	g := sprintGroupsT(t, a, `{"includeClosed":true}`)
	if got := groupsSummary(g.Groups); got != "300:future=RBI-1,107:closed=RBI-7,104:closed=MB-4 RBI-4,103:closed=MB-3" {
		t.Errorf("groups %s", got)
	}
	if countJQL(fj, closedQL(104)) != 1 || countJQL(fj, closedQL(108)) != 0 {
		t.Errorf("closed queries %v", fj.searched())
	}
	if g.Backlog == nil || len(g.Backlog.Issues) != 1 || g.ClosedUnavailable {
		t.Errorf("backlog %+v closedUnavailable %v", g.Backlog, g.ClosedUnavailable)
	}
	if g104 := g.Groups[2]; g104.Progress != (Progress{Done: 1, Todo: 1, Total: 2}) {
		t.Errorf("sprint 104 progress %+v", g104.Progress)
	}
}

// A failing open sprint query is an error unless Jira rejects it (400 =
// no sprints); a failing backlog query is an error; neither is cached.
func TestQASprintGroupsQueryErrors(t *testing.T) {
	for _, failJQL := range []string{specMBOpenJQL, specMBBackJQL} {
		fj := sprintJira()
		var failing atomic.Bool
		failing.Store(true)
		fj.status = func(r *jira.Request) int {
			if searchJQL(r) == failJQL && failing.Load() {
				return 500
			}
			return 0
		}
		a := v2App(t, fj, []string{"MB"})
		res, err := a.Call(context.Background(), "getSprintGroups", json.RawMessage(`{"project":"MB"}`))
		if err == nil {
			t.Errorf("500 on %s: result %+v, want an error", failJQL, res)
		}
		failing.Store(false)
		if g := sprintGroupsT(t, a, `{"project":"MB"}`); !g.Available || len(g.Groups) != 5 || g.Backlog == nil {
			t.Errorf("after recovery from %s: available %v, %s", failJQL, g.Available, groupsSummary(g.Groups))
		}
	}
}

// The open sprint query is capped at 1000 (every open group and the result
// truncated), the backlog at 300 (only the backlog group truncated).
func TestQASprintGroupsTruncation(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		fj := sprintJira()
		items := make([]string, 0, maxSprintIssues+1)
		for i := 0; i <= maxSprintIssues; i++ {
			sp := legacySprint(201, "ACTIVE", "2026-11-03T10:00:00.000+02:00", "")
			if i%2 == 1 {
				sp = legacySprint(300, "FUTURE", "", "")
			}
			items = append(items, withSprints(fx{key: fmt.Sprintf("MB-%d", 2000+i)}, stToDo, sp))
		}
		fj.routes[specMBOpenJQL] = issueList(items...)
		g := sprintGroupsT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB"}`)
		if !g.Truncated || len(g.Groups) != 2 || !g.Groups[0].Truncated || !g.Groups[1].Truncated {
			t.Fatalf("truncated %v groups %d", g.Truncated, len(g.Groups))
		}
		if n := len(g.Groups[0].Issues) + len(g.Groups[1].Issues); n != maxSprintIssues {
			t.Errorf("%d issues grouped, want %d", n, maxSprintIssues)
		}
		if g.Backlog == nil || g.Backlog.Truncated {
			t.Errorf("backlog %+v", g.Backlog)
		}
	})
	t.Run("backlog", func(t *testing.T) {
		fj := sprintJira()
		items := make([]string, 0, maxBacklogIssues+1)
		for i := 0; i <= maxBacklogIssues; i++ {
			items = append(items, withStatus(fx{key: fmt.Sprintf("MB-%d", 3000+i)}, stToDo))
		}
		fj.routes[specMBBackJQL] = issueList(items...)
		g := sprintGroupsT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB"}`)
		if g.Backlog == nil || !g.Backlog.Truncated || len(g.Backlog.Issues) != maxBacklogIssues {
			t.Fatalf("backlog %v", g.Backlog != nil)
		}
		if g.Backlog.Issues[0].Key != "MB-3000" || g.Backlog.Issues[maxBacklogIssues-1].Key != "MB-3299" {
			t.Errorf("backlog not in query (Rank) order: %s … %s", g.Backlog.Issues[0].Key, g.Backlog.Issues[maxBacklogIssues-1].Key)
		}
		for _, gr := range g.Groups {
			if gr.Truncated {
				t.Errorf("open group %d truncated by the backlog", gr.Sprint.ID)
			}
		}
	})
}

// A transient failure reading a board's sprint list leaves the closed
// sprints out (closedUnavailable) but keeps the open sprints and backlog.
func TestQASprintGroupsClosedBoardListFailure(t *testing.T) {
	fj := includeClosedJira()
	fj.status = func(r *jira.Request) int {
		if requestPath(r) == "/rest/agile/1.0/board/673/sprint" {
			return 503
		}
		return 0
	}
	g := sprintGroupsT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB","includeClosed":true}`)
	if !g.ClosedUnavailable || len(g.Groups) != 5 || g.Backlog == nil || !g.Available {
		t.Errorf("closedUnavailable %v groups %s backlog %v", g.ClosedUnavailable, groupsSummary(g.Groups), g.Backlog != nil)
	}
	for _, gr := range g.Groups {
		if gr.Sprint.State == jira.SprintClosed {
			t.Errorf("closed sprint %d despite the failure", gr.Sprint.ID)
		}
	}
}

// --- epics & stories ----------------------------------------------------------------

// includeDone and the project combine; another project is another cache
// entry.
func TestQAEpicsStoriesProjectWithIncludeDone(t *testing.T) {
	const (
		opsEpicsAll   = `issuetype = Epic AND project = "OPS" ORDER BY key DESC`
		opsOrphansAll = `issuetype = Story AND project = "OPS" AND cf[10103] is EMPTY ORDER BY key DESC`
		opsStoriesAll = `issuetype = Story AND project = "OPS" ORDER BY key DESC`
		mbEpics       = `issuetype = Epic AND project = "MB" AND statusCategory != Done ORDER BY key DESC`
		mbOrphans     = `issuetype = Story AND project = "MB" AND cf[10103] is EMPTY AND statusCategory != Done ORDER BY key DESC`
	)
	fj := treeJira()
	fj.projectsJSON = `[{"key":"RBI","name":"BI"},{"key":"OPS","name":"Ops"},{"key":"MB","name":"MB"}]`
	fj.routes[opsEpicsAll] = fxList(fx{key: "OPS-1", typ: "epic", cat: "done", name: "Done epic", color: "ghx-label-2"})
	fj.routes[opsOrphansAll] = fxList(fx{key: "OPS-3", typ: "story", cat: "done"})
	fj.routes[`cf[10103] in ("OPS-1") ORDER BY key DESC`] = "[]"
	fj.routes[opsStoriesAll] = fxList(fx{key: "OPS-3", typ: "story", cat: "done"})
	fj.routes[mbEpics] = "[]"
	fj.routes[mbOrphans] = "[]"
	a := v2App(t, fj, []string{"RBI"})

	e := getEpicsT(t, a, `{"project":"OPS","includeDone":true}`)
	if len(e.Epics) != 1 || e.Epics[0].Epic.Key != "OPS-1" || len(e.OrphanStories) != 1 || e.OrphanStories[0].Story.Key != "OPS-3" {
		t.Errorf("epics %+v orphans %+v", e.Epics, e.OrphanStories)
	}
	res, err := a.Call(context.Background(), "getStories", json.RawMessage(`{"project":"OPS","includeDone":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := res.(*StoriesResult); storyKeys(s.Stories) != "OPS-3[]" {
		t.Errorf("stories %s", storyKeys(s.Stories))
	}
	if e := getEpicsT(t, a, `{"project":"MB"}`); len(e.Epics) != 0 || countJQL(fj, mbEpics) != 1 {
		t.Errorf("MB epics %+v", e.Epics)
	}
	getEpicsT(t, a, `{"project":"OPS","includeDone":true}`)
	if countJQL(fj, opsEpicsAll) != 1 {
		t.Error("project epics with includeDone not cached")
	}
}

// --- QR rework: "other" column, degraded layouts, request bounds -----------------

// The "other" column lists each unplaced status once, in first-seen order;
// its category is the shared one of those statuses, else indeterminate.
func TestQAProjectBoardOtherColumnStatuses(t *testing.T) {
	const (
		stArchived  = `{"id":"777","name":"Archived","statusCategory":{"key":"done"}}`
		stCancelled = `{"id":"778","name":"Cancelled","statusCategory":{"key":"done"}}`
		stParked    = `{"id":"779","name":"Parked","statusCategory":{"key":"new"}}`
	)
	for _, c := range []struct {
		name   string
		issues []string
		want   string
	}{
		{"all done", []string{
			withStatus(fx{key: "MB-6"}, stArchived),
			withStatus(fx{key: "MB-7"}, stCancelled),
			withStatus(fx{key: "MB-8"}, stArchived),
		}, "other//done[777:Archived:done 778:Cancelled:done]=MB-6 MB-7 MB-8"},
		{"new and done", []string{
			withStatus(fx{key: "MB-6"}, stParked),
			withStatus(fx{key: "MB-7"}, stArchived),
			withStatus(fx{key: "MB-8"}, stParked),
		}, "other//indeterminate[779:Parked:new 777:Archived:done]=MB-6 MB-7 MB-8"},
		{"all new", []string{
			withStatus(fx{key: "MB-6"}, stParked),
			withStatus(fx{key: "MB-7"}, stParked),
		}, "other//new[779:Parked:new]=MB-6 MB-7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fj := mbJira()
			fj.routes[specMBAllJQL] = issueList(append([]string{withStatus(fx{key: "MB-1"}, stToDo)}, c.issues...)...)
			b := getBoardT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB","scope":"all"}`)
			last := b.Columns[len(b.Columns)-1]
			if got := columnsSummary([]Column{last}); got != c.want {
				t.Errorf("other column %s, want %s", got, c.want)
			}
			if last.Status != last.Statuses[0] {
				t.Errorf("status %+v is not the first status", last.Status)
			}
		})
	}
}

// A layout build interrupted by a cancellation fails the call and leaves
// nothing cached: the next call builds the agile layout.
func TestQAProjectLayoutCancelledBuildIsNotCached(t *testing.T) {
	fj := mbJira()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelling atomic.Bool
	fj.fail = func(r *jira.Request) error {
		if requestPath(r) == "/rest/api/2/project/MB/statuses" && ctx.Err() == nil {
			cancel()
			cancelling.Store(true)
		}
		if cancelling.Load() {
			return &jira.TransportError{Kind: jira.NetTimeout, Message: "cancelled"}
		}
		return nil
	}
	a := v2App(t, fj, []string{"MB"})
	if res, err := a.Call(ctx, "getBoard", json.RawMessage(`{"project":"MB","scope":"all"}`)); err == nil {
		t.Errorf("cancelled build returned %+v", res)
	}
	cancelling.Store(false)
	if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Layout == nil || b.Layout.Source != LayoutAgile {
		t.Errorf("after a cancelled build: %+v", b.Layout)
	}
}

// A status lookup that failed transiently (name = id) is retried after a
// minute, not kept for the layout's hour.
func TestQAProjectLayoutStatusLookupRetried(t *testing.T) {
	fj := mbJira()
	fj.projectStatuses = nil // 404: board statuses are looked up
	fj.boardConfigs[673] = `{"id":673,"columnConfig":{"columns":[{"name":"Check","statuses":[{"id":"99999"}]}]}}`
	var failing atomic.Bool
	failing.Store(true)
	fj.status = func(r *jira.Request) int {
		if requestPath(r) == "/rest/api/2/status/99999" && failing.Load() {
			return 503
		}
		return 0
	}
	a := v2App(t, fj, []string{"MB"})
	if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Columns[0].Statuses[0].Name != "99999" {
		t.Fatalf("with a failing lookup: %s", columnsSummary(b.Columns))
	}
	failing.Store(false)
	advance(t, a, metaRetryTTL+time.Second)
	if b := getBoardT(t, a, `{"project":"MB","scope":"all"}`); b.Columns[0].Statuses[0].Name != "Checked" {
		t.Errorf("lookup not retried: %s", columnsSummary(b.Columns))
	}
}

// Request bounds: at most 50 agile boards per project, 50 status lookups
// per layout and 50 boards read for closed sprints; a result is still
// produced.
func TestQARequestBounds(t *testing.T) {
	boards51 := func(project string) string {
		var bs []string
		for id := 1001; id <= 1051; id++ {
			bs = append(bs, fmt.Sprintf(`{"id":%d,"name":"B%d","type":"scrum","location":{"projectKey":%q}}`, id, id, project))
		}
		return "[" + strings.Join(bs, ",") + "]"
	}
	for _, c := range []struct {
		name  string
		setup func(fj *fakeJira)
		run   func(t *testing.T, a *App, fj *fakeJira)
	}{
		{"boards of a project", func(fj *fakeJira) {
			fj.agileBoards["MB"] = boards51("MB")
			fj.boardConfigs[1001] = mbConfig673
		}, func(t *testing.T, a *App, fj *fakeJira) {
			b := getBoardT(t, a, `{"project":"MB","scope":"all","boardId":1051}`)
			l := b.Layout
			if len(l.Boards) != maxProjectBoards || l.Boards[0].ID != 1001 || l.Boards[len(l.Boards)-1].ID != 1050 {
				t.Errorf("%d boards", len(l.Boards))
			}
			if l.BoardID != 1001 || l.Source != LayoutAgile { // 1051 is past the bound
				t.Errorf("layout board %d source %s", l.BoardID, l.Source)
			}
		}},
		{"status lookups", func(fj *fakeJira) {
			fj.projectStatuses = nil
			var cols []string
			for i := 0; i < 51; i++ {
				cols = append(cols, fmt.Sprintf(`{"name":"C%d","statuses":[{"id":"%d"}]}`, i, 70000+i))
			}
			fj.boardConfigs[673] = `{"id":673,"columnConfig":{"columns":[` + strings.Join(cols, ",") + `]}}`
		}, func(t *testing.T, a *App, fj *fakeJira) {
			b := getBoardT(t, a, `{"project":"MB","scope":"all"}`)
			if n := len(fj.requests("/rest/api/2/status/")); n != maxStatusLookups {
				t.Errorf("%d status lookups, want %d", n, maxStatusLookups)
			}
			if b.Layout.Source != LayoutAgile || len(b.Columns) < 51 || b.Columns[50].Statuses[0].Name != "70050" {
				t.Errorf("%d columns, source %s", len(b.Columns), b.Layout.Source)
			}
		}},
		{"boards read for closed sprints", func(fj *fakeJira) {
			fj.agileBoards["MB"] = boards51("MB")
			fj.routes[specMBOpenJQL] = "[]"
			fj.routes[specMBBackJQL] = "[]"
		}, func(t *testing.T, a *App, fj *fakeJira) {
			g := sprintGroupsT(t, a, `{"includeClosed":true}`) // tracked projects' boards: not cut by the project bound
			if !g.Available || g.ClosedUnavailable {
				t.Errorf("available %v closedUnavailable %v", g.Available, g.ClosedUnavailable)
			}
			read := map[string]bool{}
			for _, p := range fj.requests("/rest/agile/1.0/board/") {
				if path, _, _ := strings.Cut(p, "?"); strings.HasSuffix(path, "/sprint") {
					read[path] = true
				}
			}
			if len(read) != maxClosedBoards || read["/rest/agile/1.0/board/1051/sprint"] {
				t.Errorf("sprint lists of %d boards read, want %d (1051 read: %v)", len(read), maxClosedBoards, read["/rest/agile/1.0/board/1051/sprint"])
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fj := mbJira()
			c.setup(fj)
			c.run(t, v2App(t, fj, []string{"MB"}), fj)
		})
	}
}
