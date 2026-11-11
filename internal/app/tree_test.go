package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	treeEpicsJQL   = `issuetype = Epic AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`
	treeOrphanJQL  = `issuetype = Story AND project in ("RBI") AND cf[10103] is EMPTY AND statusCategory != Done ORDER BY key DESC`
	treeChildJQL   = `cf[10103] in ("RBI-2","RBI-1") ORDER BY key DESC`
	treeStoriesJQL = `issuetype = Story AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`
)

// The tree of the fixtures (me = alex):
//
//	RBI-1 epic "Platform" (in progress, bob)
//	  RBI-10 story (in progress, bob, 3 SP) — sub-task RBI-11 (done, alex), blocks RBI-12, linked to story RBI-20
//	  RBI-20 story (to do, bob) — linked to RBI-12 and to story RBI-10
//	  RBI-12 task (in progress, alex) — linked to both stories: not an epic-level task
//	  RBI-13 task (to do, carol), RBI-14 task (done, bob) — epic-level tasks
//	RBI-2 epic (to do, bob), no children
//	RBI-30 story without epic (to do, unassigned) — linked to RBI-31 (to do, bob)
var (
	fxRBI1  = fx{key: "RBI-1", typ: "epic", cat: "indeterminate", assignee: "bob", name: "Platform", color: "ghx-label-4"}
	fxRBI2  = fx{key: "RBI-2", typ: "epic", cat: "new", assignee: "bob"}
	fxRBI10 = fx{key: "RBI-10", typ: "story", cat: "indeterminate", assignee: "bob", epic: "RBI-1", sp: 3,
		links: []string{"out:blocks:RBI-12:task:indeterminate", "out:relates:RBI-20:story:new"}, subtasks: []string{"RBI-11:done"}}
	fxRBI11 = fx{key: "RBI-11", typ: "sub", cat: "done", assignee: "alex"}
	fxRBI12 = fx{key: "RBI-12", typ: "task", cat: "indeterminate", assignee: "alex", epic: "RBI-1",
		links: []string{"in:blocks:RBI-10:story:indeterminate", "in:relates:RBI-20:story:new"}}
	fxRBI13 = fx{key: "RBI-13", cat: "new", assignee: "carol", epic: "RBI-1"}
	fxRBI14 = fx{key: "RBI-14", cat: "done", assignee: "bob", epic: "RBI-1"}
	fxRBI20 = fx{key: "RBI-20", typ: "story", cat: "new", assignee: "bob", epic: "RBI-1",
		links: []string{"in:relates:RBI-12:task:indeterminate", "in:relates:RBI-10:story:indeterminate"}}
	fxRBI30 = fx{key: "RBI-30", typ: "story", cat: "new", links: []string{"out:relates:RBI-31:task:new"}}
	fxRBI31 = fx{key: "RBI-31", cat: "new", assignee: "bob"}
)

func treeJira() *fakeJira {
	return &fakeJira{
		routes: map[string]string{
			treeEpicsJQL:   fxList(fxRBI2, fxRBI1),
			treeOrphanJQL:  fxList(fxRBI30),
			treeChildJQL:   fxList(fxRBI20, fxRBI14, fxRBI13, fxRBI12, fxRBI10),
			treeStoriesJQL: fxList(fxRBI30, fxRBI20, fxRBI10),
		},
		byKey: fxPool(fxRBI1, fxRBI11, fxRBI12, fxRBI31),
	}
}

func refKeys(refs []IssueRef) string {
	var k []string
	for _, r := range refs {
		k = append(k, r.Key)
	}
	return strings.Join(k, ",")
}

func storyKeys(s []StoryNode) string {
	var k []string
	for _, n := range s {
		k = append(k, n.Story.Key+"["+refKeys(n.Tasks)+"]")
	}
	return strings.Join(k, ",")
}

func getEpicsT(t *testing.T, a *App, params string) *EpicsResult {
	t.Helper()
	res, err := a.Call(context.Background(), "getEpics", json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	return res.(*EpicsResult)
}

func TestEpicTree(t *testing.T) {
	fj := treeJira()
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 2 || r.Truncated {
		t.Fatalf("epics %+v", r)
	}
	e1, e2 := r.Epics[0], r.Epics[1]
	if e1.Epic.Key != "RBI-1" || e2.Epic.Key != "RBI-2" {
		t.Fatalf("epic order %s, %s (in progress before to do)", e1.Epic.Key, e2.Epic.Key)
	}
	if e1.Epic.Name != "Platform" || e1.Epic.Color != "ghx-label-4" || e1.Epic.Type.Kind != KindEpic || e2.Epic.Name != "S RBI-2" {
		t.Errorf("epic refs %+v / %+v", e1.Epic, e2.Epic)
	}
	// Stories with their tasks (sub-tasks + linked non-epic/non-story issues);
	// RBI-12 is under both stories, and not repeated at the epic level.
	if got := storyKeys(e1.Stories); got != "RBI-10[RBI-12,RBI-11],RBI-20[RBI-12]" {
		t.Errorf("stories %s", got)
	}
	if got := refKeys(e1.Tasks); got != "RBI-13,RBI-14" {
		t.Errorf("epic-level tasks %s", got)
	}
	if e1.Progress != (Progress{Done: 2, InProgress: 2, Todo: 2, Total: 6}) {
		t.Errorf("epic progress %+v (every distinct descendant once)", e1.Progress)
	}
	s10 := e1.Stories[0]
	if s10.Progress != (Progress{Done: 1, InProgress: 1, Total: 2}) || s10.StoryPoints == nil || *s10.StoryPoints != 3 || s10.Epic != nil {
		t.Errorf("story RBI-10 %+v", s10)
	}
	if len(s10.Tasks) != 2 {
		t.Fatalf("story RBI-10 tasks %s", refKeys(s10.Tasks))
	}
	if sub := s10.Tasks[1]; sub.Type.Kind != KindSubtask || !sub.Mine || sub.Assignee == nil {
		t.Errorf("sub-task %+v (assignee read with a key search)", sub)
	}
	if len(e2.Stories) != 0 || len(e2.Tasks) != 0 || e2.Progress != (Progress{}) {
		t.Errorf("empty epic %+v", e2)
	}
	if got := storyKeys(r.OrphanStories); got != "RBI-30[RBI-31]" {
		t.Errorf("orphan stories %s", got)
	}
	js, _ := json.Marshal(r)
	for _, want := range []string{`"orphanStories":[`, `"fetchedAt":"`, `"truncated":false`, `"progress":{"done":2,"inProgress":2,"todo":2,"total":6}`,
		`"epic":{"key":"RBI-1",`, `"name":"Platform","color":"ghx-label-4"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s", want)
		}
	}

	// Only mine: RBI-1 has my tasks; RBI-2 and the orphan story have none.
	m := getEpicsT(t, a, `{"scope":"mine"}`)
	if len(m.Epics) != 1 || m.Epics[0].Epic.Key != "RBI-1" || len(m.OrphanStories) != 0 {
		t.Errorf("mine: %d epics, orphans %s", len(m.Epics), storyKeys(m.OrphanStories))
	}
	// Mine keeps the whole epic (progress unchanged).
	if m.Epics[0].Progress.Total != 6 || len(m.Epics[0].Tasks) != 2 {
		t.Errorf("mine epic %+v", m.Epics[0])
	}

	// Cached per parameters; force refetches.
	before := fj.searches
	getEpicsT(t, a, `{}`)
	if fj.searches != before {
		t.Errorf("cached epics refetched")
	}
	getEpicsT(t, a, `{"force":true}`)
	if fj.searches == before {
		t.Errorf("force did not refetch")
	}
	if _, err := a.Call(context.Background(), "getEpics", json.RawMessage(`{"scope":"x"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("bad scope %v", err)
	}
}

func TestEpicsIncludeDoneUsesAnotherQuery(t *testing.T) {
	fj := treeJira()
	fj.routes[`issuetype = Epic AND project in ("RBI") ORDER BY key DESC`] = fxList(fx{key: "RBI-3", typ: "epic", cat: "done"})
	fj.routes[`issuetype = Story AND project in ("RBI") AND cf[10103] is EMPTY ORDER BY key DESC`] = "[]"
	fj.routes[`cf[10103] in ("RBI-3") ORDER BY key DESC`] = "[]"
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{"includeDone":true}`)
	if len(r.Epics) != 1 || r.Epics[0].Epic.Status.Category != "done" {
		t.Errorf("epics %+v", r.Epics)
	}
}

func TestEpicsAreTruncatedAt300(t *testing.T) {
	var epics []fx
	var keys []string
	for i := 301; i >= 1; i-- {
		epics = append(epics, fx{key: fmt.Sprintf("RBI-%d", i), typ: "epic"})
	}
	for i := 301; i > 1; i-- {
		keys = append(keys, fmt.Sprintf(`"RBI-%d"`, i))
	}
	fj := treeJira()
	fj.routes[treeEpicsJQL] = fxList(epics...)
	for i := 0; i < len(keys); i += keyChunk {
		end := i + keyChunk
		if end > len(keys) {
			end = len(keys)
		}
		fj.routes["cf[10103] in ("+strings.Join(keys[i:end], ",")+") ORDER BY key DESC"] = "[]"
	}
	a := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, a, `{}`)
	if len(r.Epics) != 300 || !r.Truncated {
		t.Errorf("%d epics, truncated=%v", len(r.Epics), r.Truncated)
	}
}

func TestEpicsWithoutTrackedProjectsOrEpicLink(t *testing.T) {
	a := v2App(t, &fakeJira{myOpen: strp("[]")}, nil)
	if r := getEpicsT(t, a, `{}`); len(r.Epics) != 0 || len(r.OrphanStories) != 0 {
		t.Errorf("no tracked projects: %+v", r)
	}
	// A Jira without Jira Software fields: stories are all orphans, no crash.
	fj := treeJira()
	fj.fieldsJSON = `[]`
	fj.routes[`issuetype = Story AND project in ("RBI") AND statusCategory != Done ORDER BY key DESC`] = fxList(fxRBI30)
	b := v2App(t, fj, []string{"RBI"})
	r := getEpicsT(t, b, `{}`)
	if len(r.Epics) != 2 || len(r.Epics[0].Stories) != 0 || storyKeys(r.OrphanStories) != "RBI-30[RBI-31]" || r.Epics[0].Epic.Name != "S RBI-1" {
		t.Errorf("without fields: %+v", r)
	}
}

func TestStories(t *testing.T) {
	fj := treeJira()
	a := v2App(t, fj, []string{"RBI"})
	res, err := a.Call(context.Background(), "getStories", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r := res.(*StoriesResult)
	if got := storyKeys(r.Stories); got != "RBI-10[RBI-12,RBI-11],RBI-30[RBI-31],RBI-20[RBI-12]" {
		t.Errorf("stories %s", got)
	}
	if e := r.Stories[0].Epic; e == nil || e.Key != "RBI-1" || e.Type.Kind != KindEpic || e.Name != "Platform" || e.Color != "ghx-label-4" || r.Stories[1].Epic != nil {
		t.Errorf("epic refs %+v / %+v", r.Stories[0].Epic, r.Stories[1].Epic)
	}
	if js, _ := json.Marshal(r.Stories[0]); !strings.Contains(string(js), `"epic":{"key":"RBI-1",`) || !strings.Contains(string(js), `"name":"Platform","color":"ghx-label-4"}`) {
		t.Errorf("story JSON %s", js)
	}
	// An epic Jira does not return (hidden/deleted): a bare reference.
	fj.byKey = fxPool(fxRBI11, fxRBI12, fxRBI31)
	a.invalidateCaches()
	res, err = a.Call(context.Background(), "getStories", json.RawMessage(`{"force":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if e := res.(*StoriesResult).Stories[0].Epic; e == nil || e.Key != "RBI-1" || e.Name != "" || e.Type.Kind != KindEpic {
		t.Errorf("bare epic ref %+v", e)
	}
	if r.Stories[2].Progress != (Progress{InProgress: 1, Total: 1}) || r.Truncated {
		t.Errorf("RBI-20 %+v truncated=%v", r.Stories[2], r.Truncated)
	}
	res, err = a.Call(context.Background(), "getStories", json.RawMessage(`{"scope":"mine"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := storyKeys(res.(*StoriesResult).Stories); got != "RBI-10[RBI-12,RBI-11],RBI-20[RBI-12]" {
		t.Errorf("mine stories %s", got)
	}
}
