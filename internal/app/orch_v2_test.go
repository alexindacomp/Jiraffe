package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jiraffe/internal/jira"
)

// QA-1: a Jira answering 400 to the field list degrades like 404/403.
func TestFieldListBadRequestDegrades(t *testing.T) {
	fj := &fakeJira{issues: fxList(fx{key: "RBI-1", assignee: "alex", epic: "RBI-100", sp: 3}), routes: map[string]string{
		`issuetype = Epic AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`:  fxList(fx{key: "RBI-100", typ: "epic"}),
		`issuetype = Story AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`: "[]", // no Epic Link field: no "is EMPTY"
	}}
	fj.status = func(r *jira.Request) int {
		if strings.HasSuffix(r.URL, "/rest/api/2/field") {
			return 400
		}
		return 0
	}
	a := v2App(t, fj, []string{"RBI"})
	b := getBoardT(t, a, `{}`)
	if keys := boardKeys(b); len(keys) != 1 {
		t.Fatalf("board %v", keys)
	}
	is := b.Columns[0].Issues[0]
	if is.Epic != nil || is.StoryPoints != nil || is.Type.Kind != KindTask {
		t.Errorf("without custom fields: %+v", is)
	}
	res, err := a.Call(context.Background(), "getEpics", nil)
	if err != nil {
		t.Fatalf("getEpics: %v", err)
	}
	if r := res.(*EpicsResult); len(r.Epics) != 1 || len(r.Epics[0].Stories)+len(r.Epics[0].Tasks) != 0 {
		t.Errorf("epics without Epic Link %+v", r)
	}
}

// ORCH-1: the "mine" board reports my issues hidden by project tracking.
func TestMineBoardReportsHiddenIssues(t *testing.T) {
	fj := &fakeJira{
		issues: fxList(fx{key: "RBI-1", assignee: "alex"}, fx{key: "XYZ-1", assignee: "alex"}, fx{key: "XYZ-2", assignee: "alex"},
			fx{key: "ABC-7", assignee: "alex"}),
		routes: map[string]string{specAllJQL("RBI"): fxList(fx{key: "RBI-1"})},
	}
	a := v2App(t, fj, []string{"RBI"})
	b := getBoardT(t, a, `{}`)
	if b.Hidden == nil || b.Hidden.Count != 3 || strings.Join(b.Hidden.Projects, ",") != "ABC,XYZ" || strings.Join(boardKeys(b), ",") != "RBI-1" {
		t.Errorf("hidden %+v, board %v", b.Hidden, boardKeys(b))
	}
	js, _ := json.Marshal(b)
	if !strings.Contains(string(js), `"hidden":{"count":3,"projects":["ABC","XYZ"]}`) {
		t.Errorf("JSON %s", js)
	}
	if all := getBoardT(t, a, `{"scope":"all"}`); all.Hidden != nil {
		t.Errorf("all board has hidden %+v", all.Hidden)
	}
	if js, _ := json.Marshal(getBoardT(t, a, `{"scope":"all"}`)); strings.Contains(string(js), "hidden") {
		t.Errorf("all JSON %s", js)
	}

	// Nothing hidden: always present for "mine", zero and [].
	c := v2App(t, &fakeJira{issues: fxList(fx{key: "RBI-1", assignee: "alex"})}, []string{"RBI"})
	if js, _ := json.Marshal(getBoardT(t, c, `{}`)); !strings.Contains(string(js), `"hidden":{"count":0,"projects":[]}`) {
		t.Errorf("JSON %s", js)
	}
}

// ORCH-2: a new boardJql invalidates the cached "mine" board at once.
func TestBoardJQLChangeInvalidatesTheMineBoard(t *testing.T) {
	const custom = `assignee = currentUser() ORDER BY key ASC`
	fj := &fakeJira{
		issues: fxList(fx{key: "RBI-1", assignee: "alex"}),
		routes: map[string]string{custom: fxList(fx{key: "RBI-2", assignee: "alex"}, fx{key: "RBI-1", assignee: "alex"})},
	}
	a := v2App(t, fj, []string{"RBI"})
	var events []string
	a.emit = func(typ string, _ any) { events = append(events, typ) }
	if keys := boardKeys(getBoardT(t, a, `{}`)); strings.Join(keys, ",") != "RBI-1" {
		t.Fatalf("board %v", keys)
	}
	if _, err := a.Call(context.Background(), "saveSettings", json.RawMessage(`{"boardJql":"`+custom+`"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(events, ","), "board") {
		t.Errorf("events %v", events)
	}
	if keys := boardKeys(getBoardT(t, a, `{}`)); strings.Join(keys, ",") != "RBI-2,RBI-1" {
		t.Errorf("stale board after boardJql change: %v", keys)
	}
	// Unrelated settings keep the cache.
	before := fj.searches
	if _, err := a.Call(context.Background(), "saveSettings", json.RawMessage(`{"worklogTime":"18:30"}`)); err != nil {
		t.Fatal(err)
	}
	getBoardT(t, a, `{}`)
	if fj.searches != before {
		t.Errorf("unrelated change refetched the board")
	}
}
