package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// CR-1: a tracked key Jira no longer knows (access revoked, archived,
// renamed) must not break the queries, and must stay untrackable.
func TestStaleTrackedProjectDoesNotBreakQueries(t *testing.T) {
	fj := treeJira() // RBI routes; any JQL mentioning GONE is answered with HTTP 400
	fj.projectsJSON = `[{"key":"RBI","name":"BI"}]`
	fj.myOpen = strp(fxList(fx{key: "RBI-12", assignee: "alex"}))
	fj.routes[specAllJQL("RBI")] = fxList(fxRBI12)
	fj.agileBoards = map[string]string{"RBI": `[{"id":880,"name":"BI","type":"scrum"}]`}
	fj.agileStatus = map[string]int{"GONE": 400}
	a := v2App(t, fj, []string{"RBI", "GONE"})
	ctx := context.Background()

	if b := getBoardT(t, a, `{"scope":"all"}`); strings.Join(boardKeys(b), ",") != "RBI-12" {
		t.Errorf("all board %v", boardKeys(b))
	}
	if r := getEpicsT(t, a, `{}`); len(r.Epics) != 2 {
		t.Errorf("epics %+v", r)
	}
	if _, err := a.Call(ctx, "getStories", nil); err != nil {
		t.Errorf("stories: %v", err)
	}
	res, err := a.Call(ctx, "getAgileBoards", nil)
	if err != nil || len(res.([]AgileBoardDTO)) != 1 {
		t.Errorf("agile boards %v %v", res, err)
	}
	if n := len(fj.requests("/rest/agile/1.0/board?projectKeyOrId=GONE")); n != 0 {
		t.Errorf("stale project queried for boards %d times", n)
	}
	for _, q := range fj.searched() {
		if strings.Contains(q, "GONE") {
			t.Errorf("stale key in JQL: %s", q)
		}
	}
	// The projects list shows it so it can be untracked.
	res, err = a.Call(ctx, "getProjects", nil)
	if err != nil {
		t.Fatal(err)
	}
	var gone *ProjectDTO
	for _, p := range res.([]ProjectDTO) {
		if p.Key == "GONE" {
			p := p
			gone = &p
		}
	}
	if gone == nil || !gone.Tracked || gone.Name != "GONE" || gone.MyOpen != 0 || gone.Available {
		t.Fatalf("stale project entry %+v in %+v", gone, res)
	}
	for _, p := range res.([]ProjectDTO) {
		if p.Key == "RBI" && !p.Available {
			t.Errorf("listed project not available: %+v", p)
		}
	}
	if js, _ := json.Marshal(gone); !strings.Contains(string(js), `"available":false`) {
		t.Errorf("JSON %s", js)
	}
	if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"GONE","tracked":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(a.cfg.Get().TrackedProjects, ","); got != "RBI" {
		t.Errorf("tracked %s", got)
	}
	// Only stale keys tracked ⇒ like no tracked projects.
	b := v2App(t, &fakeJira{projectsJSON: `[{"key":"RBI","name":"BI"}]`}, []string{"GONE"})
	if r := getEpicsT(t, b, `{}`); len(r.Epics) != 0 {
		t.Errorf("epics %+v", r)
	}
	if bd := getBoardT(t, b, `{"scope":"all"}`); len(bd.Columns) != 0 {
		t.Errorf("all board %+v", bd)
	}
}

// CR-1: only a missing agile API means "no sprints"; a 404 for one project
// is skipped. Many projects are read with bounded concurrency, in order.
func TestAgileBoardsPerProjectFailuresAndOrder(t *testing.T) {
	boards := map[string]string{}
	var keys []string
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("P%d", i)
		keys = append(keys, k)
		boards[k] = fmt.Sprintf(`[{"id":%d,"name":"B%d","type":"scrum","location":{"projectKey":%q}}]`, 100+i, i, k)
	}
	fj := &fakeJira{agileBoards: boards, agileStatus: map[string]int{"P3": 404, "P4": 403, "P5": 400}}
	a := v2App(t, fj, keys)
	res, err := a.Call(context.Background(), "getAgileBoards", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, b := range res.([]AgileBoardDTO) {
		ids = append(ids, fmt.Sprint(b.ID))
	}
	if got := strings.Join(ids, ","); got != "100,101,102,106,107,108,109" {
		t.Errorf("boards %s", got)
	}
	// Without the agile API: one probe, no per-project requests.
	fj2 := &fakeJira{agileMissing: true}
	b := v2App(t, fj2, keys)
	if res, err := b.Call(context.Background(), "getAgileBoards", nil); err != nil || len(res.([]AgileBoardDTO)) != 0 {
		t.Errorf("no agile API: %v %v", res, err)
	}
	if n := len(fj2.requests("/rest/agile/1.0/board?")); n != 1 {
		t.Errorf("%d agile requests without the API", n)
	}
}

// CR-2: a hidden or deleted key does not fail the key lookups.
func TestKeyLookupsSkipUnknownKeys(t *testing.T) {
	fj := &fakeJira{
		issues: fxList(fx{key: "RBI-1", assignee: "alex", epic: "RBI-100"}, fx{key: "RBI-2", assignee: "alex", epic: "RBI-404"}),
		byKey:  fxPool(fx{key: "RBI-100", typ: "epic", name: "Platform", color: "ghx-label-4"}),
	}
	a := v2App(t, fj, []string{"RBI"})
	b := getBoardT(t, a, `{}`)
	chips := map[string]string{}
	for _, c := range b.Columns {
		for _, is := range c.Issues {
			chips[is.Key] = is.Epic.Name
		}
	}
	if chips["RBI-1"] != "Platform" || chips["RBI-2"] != "" {
		t.Errorf("epic chips %v", chips)
	}
	for _, r := range fj.requests("/rest/api/2/search?") {
		if strings.Contains(r, "issuekey") && !strings.Contains(r, "validateQuery=false") {
			t.Errorf("key lookup validates the query: %s", r)
		}
	}
}

// CR-2: a failing chunk does not stop the following ones.
func TestSearchKeysContinuesAfterAFailedChunk(t *testing.T) {
	var keys []string
	pool := map[string]string{}
	for i := 1; i <= 150; i++ {
		k := fmt.Sprintf("RBI-%d", i)
		keys = append(keys, k)
		pool[k] = fx{key: k}.json()
	}
	fj := &fakeJira{byKey: pool}
	first := true
	fj.status = func(r *jira.Request) int {
		if strings.Contains(r.URL, "issuekey") && first {
			first = false
			return 500
		}
		return 0
	}
	c := jira.New("https://sd.local", "t", fj)
	issues, truncated, err := searchKeys(context.Background(), c, keys, []string{"summary"}, 1000)
	if len(issues) == 0 {
		t.Fatalf("no issues (truncated=%v, err=%v)", truncated, err)
	}
	if err == nil || truncated || len(issues) != 50 || issues[0].Key != "RBI-101" {
		t.Errorf("got %d issues (first %v), truncated=%v, err=%v", len(issues), issues[0].Key, truncated, err)
	}
}

// CR-3: a transient discovery failure is retried soon; a complete discovery
// is reused for an hour; concurrent first calls discover once.
func TestMetadataDiscoveryRetriesAndIsShared(t *testing.T) {
	fj := &fakeJira{}
	a := v2App(t, fj, []string{"RBI"})
	clock := a.clock.(*fakeClock)
	failTypes := true
	fj.status = func(r *jira.Request) int {
		if strings.HasSuffix(r.URL, "/rest/api/2/issuetype") && failTypes {
			return 502
		}
		return 0
	}
	ctx := context.Background()
	c, id, _ := a.client()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.metaFor(ctx, c, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(fj.requests("/rest/api/2/field")); n != 1 {
		t.Errorf("concurrent discovery ran %d times", n)
	}
	failTypes = false
	clock.t = clock.t.Add(30 * time.Second)
	m, _ := a.metaFor(ctx, c, id)
	if len(fj.requests("/rest/api/2/field")) != 1 || m.epicTypes["10000"] {
		t.Errorf("retried too early")
	}
	clock.t = clock.t.Add(time.Minute)
	m, _ = a.metaFor(ctx, c, id)
	if len(fj.requests("/rest/api/2/field")) != 2 || !m.epicTypes["10000"] || !m.storyTypes["10001"] {
		t.Errorf("not retried after a transient failure: %+v", m)
	}
	clock.t = clock.t.Add(50 * time.Minute)
	a.metaFor(ctx, c, id)
	if n := len(fj.requests("/rest/api/2/field")); n != 2 {
		t.Errorf("complete metadata rediscovered within the hour (%d)", n)
	}
}

// CR-4: sprints are cached per board; force refetches.
func TestGetSprintsIsCached(t *testing.T) {
	fj := &fakeJira{sprints: map[int][]string{880: {sprintJSON(201, "active"), sprintJSON(101, "closed")}}}
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	for _, p := range []string{`{"boardId":880}`, `{"boardId":880}`} {
		if _, err := a.Call(ctx, "getSprints", json.RawMessage(p)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(fj.requests("/rest/agile/1.0/board/880/sprint?")); n != 2 {
		t.Errorf("%d sprint requests for two calls (want 2: open + closed once)", n)
	}
	if _, err := a.Call(ctx, "getSprints", json.RawMessage(`{"boardId":880,"force":true}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(fj.requests("/rest/agile/1.0/board/880/sprint?")); n != 4 {
		t.Errorf("force: %d requests", n)
	}
}

// CR-5: issue changes refresh the counts, not the project list.
func TestIssueChangesKeepTheProjectList(t *testing.T) {
	fj := &fakeJira{issues: fxList(fx{key: "RBI-1", assignee: "alex"})}
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	if _, err := a.Call(ctx, "getProjects", nil); err != nil {
		t.Fatal(err)
	}
	a.invalidateBoard() // as after a transition or new comments
	if _, err := a.Call(ctx, "getProjects", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(fj.requests("/rest/api/2/project")); n != 1 {
		t.Errorf("project list fetched %d times", n)
	}
	if n := countJQL(fj, specMyOpenJQL); n != 2 {
		t.Errorf("counts fetched %d times, want 2", n)
	}
}

// CR-7: the real validation message is reported.
func TestTooManyTrackedProjectsMessage(t *testing.T) {
	var keys, ps []string
	for i := 0; i <= 200; i++ {
		k := fmt.Sprintf("P%d", i)
		if i < 200 {
			keys = append(keys, k)
		}
		ps = append(ps, fmt.Sprintf(`{"key":%q,"name":%q}`, k, k))
	}
	fj := &fakeJira{projectsJSON: "[" + strings.Join(ps, ",") + "]"}
	a := v2App(t, fj, keys)
	_, err := a.Call(context.Background(), "setProjectTracked", json.RawMessage(`{"key":"P200","tracked":true}`))
	if e := ToError(err); e.Code != CodeValidation || e.Message != "Слишком много проектов (не больше 200)" || len(e.Fields) != 1 || e.Fields[0].Field != "trackedProjects" {
		t.Errorf("%+v", e)
	}
}
