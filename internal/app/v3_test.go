package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

// Queries written out from SPEC.md v0.3.
const (
	specMBAllJQL  = `project = "MB" AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`
	specMBMineJQL = `project = "MB" AND ((assignee = currentUser() AND statusCategory != Done) OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d)) ORDER BY Rank ASC`
	specMBOpenJQL = `project in ("MB") AND (sprint in openSprints() OR sprint in futureSprints()) ORDER BY Rank ASC`
	specMBBackJQL = `project in ("MB") AND statusCategory != Done AND (sprint is EMPTY OR sprint not in (openSprints(), futureSprints())) ORDER BY Rank ASC`
)

// Statuses of the MB project (like the real Jira: "Reject", "Закрыт").
const (
	stToDo   = `{"id":"1","name":"ToDo","statusCategory":{"key":"new"}}`
	stInProg = `{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}}`
	stReview = `{"id":"6","name":"ForReview","statusCategory":{"key":"indeterminate"}}`
	stDone   = `{"id":"10","name":"Готово","statusCategory":{"key":"done"}}`
	stReject = `{"id":"10200","name":"Reject","statusCategory":{"key":"done"}}`
	stClosed = `{"id":"6000","name":"Закрыт","statusCategory":{"key":"done"}}`
	stTest   = `{"id":"7","name":"ForTesting","statusCategory":{"key":"indeterminate"}}`
	stWeird  = `{"id":"555","name":"Weird","statusCategory":{"key":"indeterminate"}}`
)

func typeStatuses(sub bool, statuses ...string) string {
	return fmt.Sprintf(`{"id":"3","name":"T","subtask":%v,"statuses":[%s]}`, sub, strings.Join(statuses, ","))
}

// withStatus renders x with an arbitrary status.
func withStatus(x fx, status string) string {
	if x.cat == "" {
		x.cat = "new"
	}
	return strings.Replace(x.json(), `"status":`+fxStatus[x.cat], `"status":`+status, 1)
}

func issueList(items ...string) string { return "[" + strings.Join(items, ",") + "]" }

const (
	mbBoards = `[{"id":880,"name":"BI DEV Agile board","type":"scrum","location":{"projectKey":"CMA"}},` +
		`{"id":673,"name":"ETL board","type":"kanban","location":{"projectKey":"MB"}}]`
	// Board 673: an empty "Backlog" column, a multi-status column, columns
	// with statuses outside MB's workflow (removed while MB's statuses are
	// known, looked up otherwise). Ids as strings and numbers.
	mbConfig673 = `{"id":673,"name":"ETL board","columnConfig":{"columns":[
	 {"name":"Backlog","statuses":[]},
	 {"name":"To Do","statuses":[{"id":"1","self":"x"}]},
	 {"name":"In Work","statuses":[{"id":"3"},{"id":"6"}]},
	 {"name":"Check","statuses":[{"id":99999},{"id":"10200"}]},
	 {"name":"Lost","statuses":[{"id":"77777"}]},
	 {"name":"Done","statuses":[{"id":"10"},{"id":"6000"}]}]}}`
	// Board 880 is shared by MB and CMA: "Testing" holds a CMA-only status,
	// "Rejected" an MB-only one, "Done" one of each.
	mbConfig880 = `{"id":880,"name":"BI DEV Agile board","columnConfig":{"columns":[
	 {"name":"Open","statuses":[{"id":"1"},{"id":"3"}]},
	 {"name":"Testing","statuses":[{"id":"7"}]},
	 {"name":"Rejected","statuses":[{"id":"10200"}]},
	 {"name":"Done","statuses":[{"id":"6000"},{"id":"10"}]}]}}`
)

func mbJira() *fakeJira {
	return &fakeJira{
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"MB","name":"MB"},{"key":"CMA","name":"CMA"}]`,
		agileBoards:  map[string]string{"MB": mbBoards},
		boardConfigs: map[int]string{673: mbConfig673, 880: mbConfig880},
		projectStatuses: map[string]string{"MB": "[" + typeStatuses(false, stToDo, stInProg, stReject, stDone) + "," +
			typeStatuses(true, stToDo, stReview, stClosed) + "]"},
		statuses: map[string]string{"99999": `{"id":"99999","name":"Checked","statusCategory":{"key":"indeterminate"}}`},
		routes: map[string]string{specMBAllJQL: issueList(
			withStatus(fx{key: "MB-1", assignee: "alex"}, stToDo),
			withStatus(fx{key: "MB-2", assignee: "bob"}, stReview),
			withStatus(fx{key: "MB-3", assignee: "bob"}, stDone),
			withStatus(fx{key: "MB-4", assignee: "bob"}, stWeird),
			withStatus(fx{key: "MB-5"}, stReject),
		)},
	}
}

// columnsSummary renders columns as id/name/category[statuses]=issues.
func columnsSummary(cols []Column) string {
	var out []string
	for _, c := range cols {
		var st, keys []string
		for _, s := range c.Statuses {
			st = append(st, s.ID+":"+s.Name+":"+s.Category)
		}
		for _, is := range c.Issues {
			keys = append(keys, is.Key)
		}
		out = append(out, fmt.Sprintf("%s/%s/%s[%s]=%s", c.ID, c.Name, c.Category, strings.Join(st, " "), strings.Join(keys, " ")))
	}
	return strings.Join(out, "\n")
}

func TestProjectBoardAgileLayout(t *testing.T) {
	fj := mbJira()
	a := v2App(t, fj, []string{"RBI"}) // MB is not tracked: not required
	b := getBoardT(t, a, `{"project":"mb","scope":"all"}`)
	// 99999 and 77777 are not MB statuses: removed without a lookup, "Lost"
	// is left without statuses and dropped.
	want := strings.Join([]string{
		"col-1/To Do/new[1:ToDo:new]=MB-1",
		"col-2/In Work/indeterminate[3:In Progress:indeterminate 6:ForReview:indeterminate]=MB-2",
		"col-3/Check/done[10200:Reject:done]=MB-5",
		"col-5/Done/done[10:Готово:done 6000:Закрыт:done]=MB-3",
		"other//indeterminate[555:Weird:indeterminate]=MB-4",
	}, "\n")
	if got := columnsSummary(b.Columns); got != want {
		t.Errorf("columns:\n%s\nwant:\n%s", got, want)
	}
	for _, c := range b.Columns {
		if c.Status != c.Statuses[0] {
			t.Errorf("column %s: status %+v is not the first status", c.ID, c.Status)
		}
	}
	l := b.Layout
	if l == nil || l.Source != LayoutAgile || l.Project != "MB" || l.BoardID != 673 || l.BoardName != "ETL board" {
		t.Fatalf("layout %+v", l)
	}
	var boards []string
	for _, x := range l.Boards {
		boards = append(boards, fmt.Sprintf("%d:%s:%s", x.ID, x.Type, x.ProjectKey))
	}
	if got := strings.Join(boards, ","); got != "673:kanban:MB,880:scrum:CMA" {
		t.Errorf("boards %s (all boards of the project, by id)", got)
	}
	if b.Hidden != nil || b.Scope != ScopeAll || b.Truncated {
		t.Errorf("board %+v", b)
	}
	js, _ := json.Marshal(b)
	for _, want := range []string{`"layout":{"source":"agile","project":"MB","boardId":673,"boardName":"ETL board","boards":[`,
		`{"id":"col-3","name":"Check","category":"done","statuses":[{"id":"10200","name":"Reject","category":"done"}],"issues":[{`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("JSON lacks %s:\n%s", want, js)
		}
	}
	if strings.Contains(string(js), "hidden") {
		t.Errorf("hidden in project mode: %s", js)
	}

	// The layout is cached: after issue changes only the issues are read
	// again; a forced reload also re-reads the layout and the board list.
	requests := func(n int) {
		t.Helper()
		for _, path := range []string{"/rest/agile/1.0/board/673/configuration", "/rest/api/2/project/MB/statuses", "/rest/agile/1.0/board?"} {
			if got := len(fj.requests(path)); got != n {
				t.Errorf("%s requested %d times, want %d", path, got, n)
			}
		}
	}
	a.invalidateBoard()
	getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	requests(1)
	getBoardT(t, a, `{"project":"MB","scope":"all","force":true}`)
	requests(2)
	if n := countJQL(fj, specMBAllJQL); n != 3 {
		t.Errorf("issue query ran %d times", n)
	}
	if n := len(fj.requests("/rest/api/2/status/")); n != 0 {
		t.Errorf("%d status lookups although MB's statuses are known", n)
	}
}

// Without the project statuses, board status ids are looked up once each;
// an id Jira does not know becomes name = id, category indeterminate.
func TestProjectBoardStatusLookup(t *testing.T) {
	fj := mbJira()
	fj.projectStatuses = nil // 404
	fj.boardConfigs[673] = `{"id":673,"columnConfig":{"columns":[{"name":"To Do","statuses":[{"id":"1"}]},
	 {"name":"Check","statuses":[{"id":99999},{"id":"1"}]},{"name":"Lost","statuses":[{"id":"77777"}]}]}}`
	fj.statuses["1"] = stToDo
	fj.routes[specMBAllJQL] = issueList(withStatus(fx{key: "MB-1"}, stToDo), withStatus(fx{key: "MB-2"}, stWeird))
	a := v2App(t, fj, []string{"MB"})
	b := getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	want := strings.Join([]string{
		"col-0/To Do/new[1:ToDo:new]=MB-1",
		"col-1/Check/indeterminate[99999:Checked:indeterminate 1:ToDo:new]=",
		"col-2/Lost/indeterminate[77777:77777:indeterminate]=",
		"other//indeterminate[555:Weird:indeterminate]=MB-2",
	}, "\n")
	if got := columnsSummary(b.Columns); got != want || b.Layout.Source != LayoutAgile {
		t.Errorf("%s columns:\n%s", b.Layout.Source, got)
	}
	a.invalidateBoard()
	getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	for _, id := range []string{"1", "99999", "77777"} {
		if n := len(fj.requests("/rest/api/2/status/" + id)); n != 1 {
			t.Errorf("status %s looked up %d times", id, n)
		}
	}
}

// A shared board shows each project only the statuses of its own workflow;
// a board with none of them falls back to the workflow.
func TestProjectBoardSharedBoard(t *testing.T) {
	fj := mbJira()
	fj.agileBoards["CMA"] = `[{"id":880,"name":"BI DEV Agile board","type":"scrum","location":{"projectKey":"CMA"}},{"id":881,"name":"MB only","type":"scrum"}]`
	fj.boardConfigs[881] = `{"id":881,"columnConfig":{"columns":[{"name":"A","statuses":[{"id":"10200"}]},{"name":"B","statuses":[{"id":"6"}]}]}}`
	fj.projectStatuses["CMA"] = "[" + typeStatuses(false, stTest, stToDo, stClosed) + "," + typeStatuses(true, stToDo, stInProg) + "]"
	fj.routes[`project = "CMA" AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`] = issueList(
		withStatus(fx{key: "CMA-1"}, stTest), withStatus(fx{key: "CMA-2"}, stWeird))
	a := v2App(t, fj, []string{"MB", "CMA"})

	b := getBoardT(t, a, `{"project":"CMA","scope":"all"}`)
	want := strings.Join([]string{
		"col-0/Open/indeterminate[1:ToDo:new 3:In Progress:indeterminate]=",
		"col-1/Testing/indeterminate[7:ForTesting:indeterminate]=CMA-1",
		"col-3/Done/done[6000:Закрыт:done]=",
		"other//indeterminate[555:Weird:indeterminate]=CMA-2",
	}, "\n")
	if got := columnsSummary(b.Columns); got != want || b.Layout.BoardID != 880 {
		t.Errorf("CMA on board %d:\n%s", b.Layout.BoardID, got)
	}
	b = getBoardT(t, a, `{"project":"MB","scope":"all","boardId":880}`)
	want = strings.Join([]string{
		"col-0/Open/indeterminate[1:ToDo:new 3:In Progress:indeterminate]=MB-1",
		"col-2/Rejected/done[10200:Reject:done]=MB-5",
		"col-3/Done/done[6000:Закрыт:done 10:Готово:done]=MB-3",
		"other//indeterminate[6:ForReview:indeterminate 555:Weird:indeterminate]=MB-2 MB-4",
	}, "\n")
	if got := columnsSummary(b.Columns); got != want || b.Layout.BoardID != 880 {
		t.Errorf("MB on board %d:\n%s", b.Layout.BoardID, got)
	}
	if n := len(fj.requests("/rest/api/2/status/")); n != 0 {
		t.Errorf("%d status lookups for other projects' statuses", n)
	}
	// Board 881 has no CMA status at all: workflow columns, but the chosen
	// board and the picker stay so another board can be chosen.
	b = getBoardT(t, a, `{"project":"CMA","scope":"all","boardId":881}`)
	if l := b.Layout; l.Source != LayoutWorkflow || l.BoardID != 881 || l.BoardName != "MB only" || len(l.Boards) != 2 ||
		len(b.Columns) != 5 || b.Columns[0].ID != "1" {
		t.Errorf("%+v\n%s", b.Layout, columnsSummary(b.Columns))
	}
}

// Layout builds are serialized per project, and waiting respects ctx.
func TestLayoutLockPerProject(t *testing.T) {
	a := v2App(t, &fakeJira{}, []string{"MB"})
	unlockA, err := a.lockLayout(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	unlockB, err := a.lockLayout(context.Background(), "B") // another project: not blocked
	if err != nil {
		t.Fatal(err)
	}
	unlockB()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.lockLayout(ctx, "A"); err != context.Canceled {
		t.Errorf("waiting with a cancelled ctx: %v", err)
	}
	// A second build of A waits until the first one ends.
	started, got := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		unlock, err := a.lockLayout(context.Background(), "A")
		if err == nil {
			unlock()
		}
		got <- err
	}()
	<-started
	select {
	case err := <-got:
		t.Fatalf("second build of A did not wait for the first one (err %v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlockA()
	select {
	case err := <-got:
		if err != nil {
			t.Errorf("waiter after unlock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter not released by unlock")
	}
}

// A Jira 400 on a project board query stays bad_request: only an unknown
// project is project_unavailable.
func TestProjectBoardJiraRejectionIsBadRequest(t *testing.T) {
	fj := mbJira()
	delete(fj.routes, specMBAllJQL) // the fake rejects unknown JQL with HTTP 400 (both orderings)
	a := v2App(t, fj, []string{"MB"})
	_, err := a.Call(context.Background(), "getBoard", json.RawMessage(`{"project":"MB","scope":"all"}`))
	if e := ToError(err); e.Code != jira.CodeBadRequest {
		t.Errorf("Jira 400: %+v", e)
	}
	_, err = a.Call(context.Background(), "getBoard", json.RawMessage(`{"project":"NOPE","scope":"all"}`))
	if e := ToError(err); e.Code != CodeProjectUnavailable {
		t.Errorf("unknown project: %+v", e)
	}
}

// getSprintGroups{force} re-reads the board and sprint lists, so a sprint
// closed meanwhile shows up; issue changes alone keep those lists.
func TestSprintGroupsForceRereadsClosedSprints(t *testing.T) {
	fj := includeClosedJira()
	fj.routes[closedSprintJQL(104)] = issueList(withStatus(fx{key: "MB-21"}, stDone))
	for _, id := range []int{106, 105, 103, 102, 101} {
		fj.routes[closedSprintJQL(id)] = "[]"
	}
	a := v2App(t, fj, []string{"MB"})
	closed := func(params string) string {
		t.Helper()
		g := sprintGroupsT(t, a, params)
		return groupsSummary(g.Groups[5:])
	}
	if got := closed(`{"project":"MB","includeClosed":true}`); got != "104:closed=MB-21" {
		t.Fatalf("closed %s", got)
	}
	// Sprint 107 is completed in Jira.
	fj.sprints[673] = append(fj.sprints[673], closedSprintJSON(107, 7))
	fj.routes[closedSprintJQL(107)] = issueList(withStatus(fx{key: "MB-50"}, stDone))
	boardLists := len(fj.requests("/rest/agile/1.0/board?"))
	a.invalidateBoard()
	if got := closed(`{"project":"MB","includeClosed":true}`); got != "104:closed=MB-21" {
		t.Errorf("sprint lists re-read without force: %s", got)
	}
	if got := closed(`{"project":"MB","includeClosed":true,"force":true}`); got != "107:closed=MB-50,104:closed=MB-21" {
		t.Errorf("after force: %s", got)
	}
	if n := len(fj.requests("/rest/agile/1.0/board?")); n != boardLists+1 {
		t.Errorf("board list read %d times more, want 1", n-boardLists)
	}
}

func TestProjectBoardChoosesBoard(t *testing.T) {
	fj := mbJira()
	a := v2App(t, fj, []string{"RBI"})
	// A shared board of the project can be picked.
	if b := getBoardT(t, a, `{"project":"MB","scope":"all","boardId":880}`); b.Layout.BoardID != 880 {
		t.Errorf("board 880: chose %d", b.Layout.BoardID)
	}
	// A board of another project: the default choice (located in MB).
	if b := getBoardT(t, a, `{"project":"MB","scope":"all","boardId":999}`); b.Layout.BoardID != 673 {
		t.Errorf("unknown board id: chose %d", b.Layout.BoardID)
	}

	boards := func(js string) []jira.AgileBoard {
		var bs []jira.AgileBoard
		if err := json.Unmarshal([]byte(js), &bs); err != nil {
			t.Fatal(err)
		}
		return bs
	}
	sorted := boards(`[{"id":5,"name":"a"},{"id":7,"name":"b","location":{"projectKey":"CMA"}},{"id":9,"name":"c","location":{"projectKey":"mb"}}]`)
	for _, c := range []struct {
		want, got int
	}{
		{9, chooseBoard(sorted, "MB", 0).ID},  // located in the project
		{7, chooseBoard(sorted, "MB", 7).ID},  // requested and listed
		{9, chooseBoard(sorted, "MB", 42).ID}, // requested, not listed
		{5, chooseBoard(sorted[:2], "MB", 0).ID},
	} {
		if c.got != c.want {
			t.Errorf("chose %d, want %d", c.got, c.want)
		}
	}
}

// Without agile boards the columns are the project's workflow statuses:
// union over issue types (sub-tasks too), by category, then first appearance.
func TestProjectBoardWorkflowFallback(t *testing.T) {
	cma := "[" + typeStatuses(false, stTest, stToDo, stClosed) + "," + typeStatuses(true, stToDo, stInProg) + "]"
	for _, c := range []struct {
		fj         *fakeJira
		layoutJSON string
	}{
		{&fakeJira{agileMissing: true}, `{"source":"workflow","project":"CMA","boards":[]}`},               // no Jira Software
		{&fakeJira{agileBoards: map[string]string{}}, `{"source":"workflow","project":"CMA","boards":[]}`}, // no boards
		// The configuration of the chosen board fails (404): the board and
		// the picker stay.
		{&fakeJira{agileBoards: map[string]string{"CMA": mbBoards}}, `{"source":"workflow","project":"CMA","boardId":880,"boardName":"BI DEV Agile board",` +
			`"boards":[{"id":673,"name":"ETL board","type":"kanban","projectKey":"MB"},{"id":880,"name":"BI DEV Agile board","type":"scrum","projectKey":"CMA"}]}`},
	} {
		fj := c.fj
		fj.projectsJSON = `[{"key":"CMA","name":"CMA"}]`
		fj.projectStatuses = map[string]string{"CMA": cma}
		fj.routes = map[string]string{`project = "CMA" AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`: issueList(
			withStatus(fx{key: "CMA-1"}, stInProg), withStatus(fx{key: "CMA-2"}, stWeird))}
		fj.boardConfigs = map[int]string{}
		a := v2App(t, fj, []string{"CMA"})
		b := getBoardT(t, a, `{"project":"CMA","scope":"all"}`)
		want := strings.Join([]string{
			"1/ToDo/new[1:ToDo:new]=",
			"7/ForTesting/indeterminate[7:ForTesting:indeterminate]=",
			"3/In Progress/indeterminate[3:In Progress:indeterminate]=CMA-1",
			"6000/Закрыт/done[6000:Закрыт:done]=",
			"other//indeterminate[555:Weird:indeterminate]=CMA-2",
		}, "\n")
		if got := columnsSummary(b.Columns); got != want {
			t.Errorf("columns:\n%s", got)
		}
		if js, _ := json.Marshal(b.Layout); string(js) != c.layoutJSON {
			t.Errorf("layout JSON %s\nwant %s", js, c.layoutJSON)
		}
	}

	// Neither boards nor statuses: columns from the issues, like without a project.
	fj := &fakeJira{agileMissing: true, projectsJSON: `[{"key":"CMA","name":"CMA"}]`,
		routes: map[string]string{`project = "CMA" AND (statusCategory != Done OR updated >= -14d) ORDER BY Rank ASC`: issueList(
			withStatus(fx{key: "CMA-1"}, stInProg), withStatus(fx{key: "CMA-2"}, stToDo))}}
	b := getBoardT(t, v2App(t, fj, []string{"CMA"}), `{"project":"CMA","scope":"all"}`)
	if got := columnsSummary(b.Columns); b.Layout.Source != LayoutIssues || got != "1/ToDo/new[1:ToDo:new]=CMA-2\n3/In Progress/indeterminate[3:In Progress:indeterminate]=CMA-1" {
		t.Errorf("%s: %s", b.Layout.Source, got)
	}
}

func TestProjectBoardJQL(t *testing.T) {
	for _, c := range []struct{ scope, boardJQL, want string }{
		{ScopeAll, specBoardJQL, `project = "MB" AND (statusCategory != Done OR updated >= -14d)`},
		{ScopeMine, specBoardJQL, strings.TrimSuffix(specMBMineJQL, rankOrder)},
		{ScopeMine, `labels = x AND summary ~ "order by" order   BY created DESC`,
			`project = "MB" AND ((labels = x AND summary ~ "order by") OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d))`},
		{ScopeMine, `reporter = currentUser() OR assignee = currentUser()`,
			`project = "MB" AND ((reporter = currentUser() OR assignee = currentUser()) OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d))`},
		{ScopeMine, `summary ~ 'it\'s ORDER BY'`,
			`project = "MB" AND ((summary ~ 'it\'s ORDER BY') OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d))`},
		{ScopeMine, ` ORDER BY Rank`, `project = "MB" AND assignee = currentUser() AND (statusCategory != Done OR updated >= -14d)`},
		{ScopeMine, `myorder by x`, `project = "MB" AND ((myorder by x) OR (assignee = currentUser() AND statusCategory = Done AND updated >= -14d))`},
	} {
		if got := ProjectBoardJQL(c.scope, "MB", c.boardJQL); got != c.want {
			t.Errorf("%s %q:\n got %s\nwant %s", c.scope, c.boardJQL, got, c.want)
		}
	}
	// The default "mine" board of a project.
	fj := mbJira()
	fj.routes[specMBMineJQL] = issueList(withStatus(fx{key: "MB-1", assignee: "alex"}, stToDo))
	b := getBoardT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB"}`)
	if keys := boardKeys(b); b.Scope != ScopeMine || strings.Join(keys, ",") != "MB-1" || b.Hidden != nil {
		t.Errorf("mine: %v hidden %+v", keys, b.Hidden)
	}
}

// Without Jira Software ordering by Rank is rejected: retried by updated.
func TestProjectBoardRankFallback(t *testing.T) {
	fj := mbJira()
	byUpdated := strings.TrimSuffix(specMBAllJQL, rankOrder) + " ORDER BY updated DESC"
	fj.routes = map[string]string{byUpdated: issueList(withStatus(fx{key: "MB-1"}, stToDo))}
	b := getBoardT(t, v2App(t, fj, []string{"MB"}), `{"project":"MB","scope":"all"}`)
	if keys := boardKeys(b); strings.Join(keys, ",") != "MB-1" {
		t.Errorf("keys %v", keys)
	}
	if countJQL(fj, specMBAllJQL) != 1 || countJQL(fj, byUpdated) != 1 {
		t.Errorf("queries %v", fj.searched())
	}
}

func TestProjectParamValidation(t *testing.T) {
	fj := mbJira()
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	for _, m := range []string{"getBoard", "getSprintGroups", "getEpics", "getStories"} {
		for _, p := range []string{`"NOPE"`, `"MB\" OR project = X"`, `"M B"`} {
			_, err := a.Call(ctx, m, json.RawMessage(`{"project":`+p+`}`))
			if e := ToError(err); e.Code != CodeProjectUnavailable || e.Message != T(a.Language(), "err_project_unavailable") {
				t.Errorf("%s %s: %v", m, p, err)
			}
		}
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		if T(lang, "err_project_unavailable") == "err_project_unavailable" {
			t.Errorf("%s: no text", lang)
		}
	}
	if _, err := a.Call(ctx, "getBoard", json.RawMessage(`{"project":"MB","boardId":-1}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("negative board id: %v", err)
	}
	for _, q := range fj.searched() {
		if strings.Contains(q, "NOPE") || strings.Contains(q, "M B") {
			t.Errorf("invalid project reached JQL: %s", q)
		}
	}
}

func TestBoardCacheKeys(t *testing.T) {
	fj := mbJira()
	fj.issues = fxList(fx{key: "RBI-1", assignee: "alex"})
	fj.routes[specAllJQL("RBI")] = "[]"
	a := v2App(t, fj, []string{"RBI"})
	getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	if n := countJQL(fj, specMBAllJQL); n != 1 {
		t.Errorf("project board not cached: %d", n)
	}
	getBoardT(t, a, `{"project":"MB","scope":"all","boardId":880}`) // another board: another entry
	getBoardT(t, a, `{"scope":"all","boardId":880}`)                // board id ignored without a project
	getBoardT(t, a, `{"scope":"all"}`)
	if n := countJQL(fj, specMBAllJQL); n != 2 {
		t.Errorf("board id not in the cache key: %d", n)
	}
	if n := countJQL(fj, specAllJQL("RBI")); n != 1 {
		t.Errorf("tracked board: %d queries", n)
	}
	b := getBoardT(t, a, `{}`) // tracked projects: v0.2 columns, new shape
	if b.Layout != nil || b.Hidden == nil || len(b.Columns) != 1 {
		t.Fatalf("board %+v", b)
	}
	if c := b.Columns[0]; c.ID != "1" || c.Name != "ToDo" || c.Category != "new" || len(c.Statuses) != 1 || c.Statuses[0] != c.Status {
		t.Errorf("status column %+v", c)
	}
	// Issue changes drop every board, the layout stays.
	a.invalidateBoard()
	getBoardT(t, a, `{"project":"MB","scope":"all"}`)
	if n := countJQL(fj, specMBAllJQL); n != 3 || len(fj.requests("/rest/agile/1.0/board/673/configuration")) != 1 {
		t.Errorf("after invalidation: %d queries, %d configuration reads", n, len(fj.requests("/rest/agile/1.0/board/673/configuration")))
	}
}

// --- sprint groups -------------------------------------------------------------

func legacySprint(id int, state, start, complete string) string {
	if start == "" {
		start = "<null>"
	}
	if complete == "" {
		complete = "<null>"
	}
	return fmt.Sprintf("%q", fmt.Sprintf("com.atlassian.greenhopper.service.sprint.Sprint@1a%d[id=%d,rapidViewId=880,state=%s,name=Sprint %d,startDate=%s,endDate=<null>,completeDate=%s,sequence=%d,goal=Goal, part %d]",
		id, id, state, id, start, complete, id, id))
}

// withSprints renders x with Sprint field values.
func withSprints(x fx, status string, sprints ...string) string {
	return strings.Replace(withStatus(x, status), `"fields":{`, `"fields":{"customfield_10107":[`+strings.Join(sprints, ",")+`],`, 1)
}

func sprintGroupsT(t *testing.T, a *App, params string) *SprintGroups {
	t.Helper()
	res, err := a.Call(context.Background(), "getSprintGroups", json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	return res.(*SprintGroups)
}

func groupsSummary(gs []SprintGroup) string {
	var out []string
	for _, g := range gs {
		var keys []string
		for _, is := range g.Issues {
			keys = append(keys, is.Key)
		}
		out = append(out, fmt.Sprintf("%d:%s=%s", g.Sprint.ID, g.Sprint.State, strings.Join(keys, " ")))
	}
	return strings.Join(out, ",")
}

func sprintJira() *fakeJira {
	open := issueList(
		// Carried over from closed sprint 100: only in 201.
		withSprints(fx{key: "MB-1", assignee: "alex", sp: 2}, stDone, legacySprint(100, "CLOSED", "2026-10-20T10:00:00.000+02:00", "2026-11-02T10:00:00.000+02:00"),
			legacySprint(201, "ACTIVE", "2026-11-03T10:00:00.000+02:00", "")),
		// In two open sprints: in both groups.
		withSprints(fx{key: "MB-2", assignee: "bob", sp: 3}, stInProg, legacySprint(201, "ACTIVE", "2026-11-03T10:00:00.000+02:00", ""), legacySprint(301, "FUTURE", "", "")),
		withSprints(fx{key: "MB-3", assignee: "bob"}, stToDo, `{"id":302,"name":"Object sprint","state":"future","originBoardId":673,"goal":"g"}`),
		// Started earlier: the first active sprint.
		withSprints(fx{key: "MB-4", assignee: "alex"}, stToDo, legacySprint(202, "ACTIVE", "2026-10-27T10:00:00.000+02:00", "")),
		withSprints(fx{key: "MB-5", assignee: "bob"}, stToDo, legacySprint(300, "FUTURE", "", "")),
	)
	return &fakeJira{
		projectsJSON: `[{"key":"RBI","name":"BI"},{"key":"MB","name":"MB"}]`,
		routes: map[string]string{
			specMBOpenJQL: open,
			specMBBackJQL: issueList(withStatus(fx{key: "MB-9"}, stToDo)),
		},
	}
}

func TestSprintGroups(t *testing.T) {
	fj := sprintJira()
	a := v2App(t, fj, []string{"MB"})
	g := sprintGroupsT(t, a, `{"project":"MB"}`)
	if got := groupsSummary(g.Groups); got != "202:active=MB-4,201:active=MB-1 MB-2,300:future=MB-5,301:future=MB-2,302:future=MB-3" {
		t.Errorf("groups %s", got)
	}
	if !g.Available || g.Truncated {
		t.Errorf("available %v truncated %v", g.Available, g.Truncated)
	}
	s201 := g.Groups[1]
	if s := s201.Sprint; s.Name != "Sprint 201" || s.BoardID != 880 || s.Goal != "Goal, part 201" || !sameInstant(s.StartDate, "2026-11-03T10:00:00+02:00") {
		t.Errorf("sprint 201 %+v", s)
	}
	if s201.Progress != (Progress{Done: 1, InProgress: 1, Total: 2}) || s201.StoryPoints != (PointsDTO{Done: 2, Total: 5}) {
		t.Errorf("sprint 201 progress %+v points %+v", s201.Progress, s201.StoryPoints)
	}
	if s := g.Groups[4].Sprint; s.Name != "Object sprint" || s.BoardID != 673 {
		t.Errorf("object sprint %+v", s)
	}
	if g.Backlog == nil || g.Backlog.Sprint != nil || len(g.Backlog.Issues) != 1 || g.Backlog.Progress.Todo != 1 {
		t.Errorf("backlog %+v", g.Backlog)
	}
	js, _ := json.Marshal(g.Backlog)
	if strings.Contains(string(js), `"sprint"`) {
		t.Errorf("backlog JSON %s", js)
	}

	// "All projects" = the tracked ones; scope mine filters in JQL.
	fj.routes[strings.Replace(specMBOpenJQL, rankOrder, mineClause+rankOrder, 1)] = "[]"
	fj.routes[strings.Replace(specMBBackJQL, rankOrder, mineClause+rankOrder, 1)] = "[]"
	g = sprintGroupsT(t, a, `{"scope":"mine"}`)
	if len(g.Groups) != 0 || g.Backlog != nil || !g.Available {
		t.Errorf("mine %+v", g)
	}
	if js, _ := json.Marshal(g); !strings.Contains(string(js), `"groups":[],"backlog":null`) {
		t.Errorf("empty JSON %s", js)
	}
}

func closedSprintJSON(id, day int) string {
	return fmt.Sprintf(`{"id":%d,"name":"S%d","state":"closed","originBoardId":673,"completeDate":"2026-10-%02dT18:00:00.000+02:00"}`, id, id, day)
}

func closedSprintJQL(id int) string {
	return fmt.Sprintf(`project in ("MB") AND sprint = %d ORDER BY Rank ASC`, id)
}

// includeClosedJira serves board 673 with closed sprints whose completion
// dates are out of id order: newest first they are 104, 106, 105, 103, 102,
// 101.
func includeClosedJira() *fakeJira {
	fj := sprintJira()
	fj.agileBoards = map[string]string{"MB": `[{"id":674,"name":"Kanban","type":"kanban"},{"id":673,"name":"ETL","type":"scrum"}]`}
	fj.kanban = map[int]bool{674: true}
	fj.sprints = map[int][]string{673: {closedSprintJSON(101, 1), closedSprintJSON(102, 2), closedSprintJSON(103, 3), closedSprintJSON(105, 4),
		closedSprintJSON(106, 5), closedSprintJSON(104, 6)}}
	return fj
}

func TestSprintGroupsIncludeClosed(t *testing.T) {
	fj := includeClosedJira()
	fj.routes[closedSprintJQL(104)] = issueList(withStatus(fx{key: "MB-21"}, stDone))
	fj.routes[closedSprintJQL(106)] = "[]" // no MB issues (another project's sprint): skipped
	fj.routes[closedSprintJQL(105)] = issueList(withStatus(fx{key: "MB-20"}, stDone), withStatus(fx{key: "MB-23"}, stToDo))
	fj.routes[closedSprintJQL(103)] = issueList(withStatus(fx{key: "MB-22"}, stDone))
	a := v2App(t, fj, []string{"MB"})
	g := sprintGroupsT(t, a, `{"project":"MB","includeClosed":true}`)
	// Open sprints, then the 3 newest closed ones with issues.
	want := "202:active=MB-4,201:active=MB-1 MB-2,300:future=MB-5,301:future=MB-2,302:future=MB-3,104:closed=MB-21,105:closed=MB-20 MB-23,103:closed=MB-22"
	if got := groupsSummary(g.Groups); got != want {
		t.Errorf("groups\n got %s\nwant %s", got, want)
	}
	if s := g.Groups[5].Sprint; s.Name != "S104" || s.CompleteDate == "" || s.BoardID != 673 {
		t.Errorf("closed sprint metadata from the board: %+v", s)
	}
	if g105 := g.Groups[6]; g105.Progress != (Progress{Done: 1, Todo: 1, Total: 2}) || g105.Truncated {
		t.Errorf("sprint 105 %+v", g105)
	}
	if countJQL(fj, closedSprintJQL(102)) != 0 {
		t.Error("queried past 3 sprints with issues")
	}
	// Without includeClosed no closed sprint is read.
	before := len(fj.requests("/rest/agile/1.0/board/673/sprint"))
	sprintGroupsT(t, a, `{"project":"MB","force":true}`)
	if len(fj.requests("/rest/agile/1.0/board/673/sprint")) != before {
		t.Error("closed sprints read without includeClosed")
	}
}

// Each closed sprint has its own query and limit: a big older sprint does
// not cut the newest one, and only its own group is truncated.
func TestSprintGroupsClosedPerSprintLimit(t *testing.T) {
	fj := includeClosedJira()
	big := make([]string, 0, maxSprintIssues+1)
	for i := 0; i <= maxSprintIssues; i++ {
		big = append(big, withStatus(fx{key: fmt.Sprintf("MB-%d", 5000+i)}, stDone))
	}
	fj.routes[closedSprintJQL(104)] = issueList(withStatus(fx{key: "MB-21"}, stDone), withStatus(fx{key: "MB-22"}, stDone))
	fj.routes[closedSprintJQL(106)] = issueList(big...)
	fj.routes[closedSprintJQL(105)] = "[]"
	fj.routes[closedSprintJQL(103)] = "[]"
	fj.routes[closedSprintJQL(102)] = issueList(withStatus(fx{key: "MB-24"}, stDone))
	a := v2App(t, fj, []string{"MB"})
	g := sprintGroupsT(t, a, `{"project":"MB","includeClosed":true}`)
	closed := g.Groups[5:]
	if len(closed) != 3 || closed[0].Sprint.ID != 104 || closed[1].Sprint.ID != 106 || closed[2].Sprint.ID != 102 {
		t.Fatalf("closed groups %s", groupsSummary(closed))
	}
	if len(closed[0].Issues) != 2 || closed[0].Truncated || len(closed[1].Issues) != maxSprintIssues || !closed[1].Truncated || closed[2].Truncated {
		t.Errorf("104: %d issues truncated %v; 106: %d truncated %v", len(closed[0].Issues), closed[0].Truncated, len(closed[1].Issues), closed[1].Truncated)
	}
	if !g.Truncated || g.Groups[0].Truncated {
		t.Errorf("top truncated %v, open group truncated %v", g.Truncated, g.Groups[0].Truncated)
	}
}

// Closed sprints are optional: a failure leaves them out, keeps the open
// sprints and the backlog, and the result is not cached.
func TestSprintGroupsClosedFailureIsNotFatal(t *testing.T) {
	fj := includeClosedJira()
	fj.status = func(r *jira.Request) int {
		if u, err := url.Parse(r.URL); err == nil && u.Query().Get("jql") == closedSprintJQL(104) {
			return 500
		}
		return 0
	}
	a := v2App(t, fj, []string{"MB"})
	g := sprintGroupsT(t, a, `{"project":"MB","includeClosed":true}`)
	if got := groupsSummary(g.Groups); got != "202:active=MB-4,201:active=MB-1 MB-2,300:future=MB-5,301:future=MB-2,302:future=MB-3" || g.Backlog == nil {
		t.Errorf("groups %s backlog %v", got, g.Backlog)
	}
	if js, _ := json.Marshal(g); !g.ClosedUnavailable || !strings.Contains(string(js), `"closedUnavailable":true`) {
		t.Errorf("failure not reported: %s", js)
	}
	sprintGroupsT(t, a, `{"project":"MB","includeClosed":true}`)
	if n := countJQL(fj, specMBOpenJQL); n != 2 {
		t.Errorf("a result without its closed sprints was cached (%d open queries)", n)
	}
	absent := func(params string) *SprintGroups {
		t.Helper()
		g := sprintGroupsT(t, a, params)
		if js, _ := json.Marshal(g); g.ClosedUnavailable || strings.Contains(string(js), "closedUnavailable") {
			t.Errorf("%s: %s", params, js)
		}
		return g
	}
	absent(`{"project":"MB"}`) // includeClosed not requested
	// The closed sprints can be read again (calls are sequential).
	fj.status = nil
	fj.routes[closedSprintJQL(104)] = issueList(withStatus(fx{key: "MB-21"}, stDone))
	for _, id := range []int{106, 105, 103, 102, 101} {
		fj.routes[closedSprintJQL(id)] = "[]"
	}
	if g := absent(`{"project":"MB","includeClosed":true}`); len(g.Groups) != 6 || g.Groups[5].Sprint.ID != 104 {
		t.Errorf("closed groups after recovery: %s", groupsSummary(g.Groups))
	}
}

func TestSprintGroupsUnavailable(t *testing.T) {
	fj := sprintJira()
	delete(fj.routes, specMBOpenJQL) // the fake rejects unknown JQL (400), like a Jira without openSprints()
	a := v2App(t, fj, []string{"MB"})
	g := sprintGroupsT(t, a, `{"project":"MB"}`)
	if g.Available || len(g.Groups) != 0 || g.Backlog != nil {
		t.Errorf("%+v", g)
	}
	if js, _ := json.Marshal(g); !strings.Contains(string(js), `"groups":[],"backlog":null`) || !strings.Contains(string(js), `"available":false`) {
		t.Errorf("JSON %s", js)
	}
	// No Sprint field (no Jira Software): unavailable without a query.
	fj2 := sprintJira()
	fj2.fieldsJSON = `[{"id":"summary","name":"Summary","schema":{"type":"string"}}]`
	if g := sprintGroupsT(t, v2App(t, fj2, []string{"MB"}), `{}`); g.Available {
		t.Errorf("without a Sprint field: %+v", g)
	}
	if countJQL(fj2, specMBOpenJQL) != 0 {
		t.Error("queried without a Sprint field")
	}
}

func TestSprintGroupsCache(t *testing.T) {
	fj := sprintJira()
	fj.projectsJSON = `[{"key":"MB","name":"MB"},{"key":"OPS","name":"Ops"}]`
	a := v2App(t, fj, []string{"MB"})
	ctx := context.Background()
	sprintGroupsT(t, a, `{"project":"MB"}`)
	sprintGroupsT(t, a, `{"project":"MB"}`)
	sprintGroupsT(t, a, `{}`) // same projects, another key
	if n := countJQL(fj, specMBOpenJQL); n != 2 {
		t.Errorf("open sprint query ran %d times", n)
	}
	sprintGroupsT(t, a, `{"project":"MB","force":true}`)
	if n := countJQL(fj, specMBOpenJQL); n != 3 {
		t.Errorf("force: %d", n)
	}
	// A transition drops the sprint groups like the boards.
	if _, err := a.Call(ctx, "transition", json.RawMessage(`{"key":"MB-1","transitionId":"31"}`)); err != nil {
		t.Fatal(err)
	}
	sprintGroupsT(t, a, `{"project":"MB"}`)
	if n := countJQL(fj, specMBOpenJQL); n != 4 {
		t.Errorf("after a transition: %d", n)
	}
	if _, err := a.Call(ctx, "setProjectTracked", json.RawMessage(`{"key":"OPS","tracked":false}`)); err != nil {
		t.Fatal(err)
	}
	sprintGroupsT(t, a, `{"project":"MB"}`)
	if n := countJQL(fj, specMBOpenJQL); n != 5 {
		t.Errorf("after a tracking change: %d", n)
	}
	if _, err := a.Call(ctx, "getSprint", json.RawMessage(`{"sprintId":1}`)); ToError(err).Code != CodeUnknownMethod {
		t.Errorf("getSprint still exists: %v", err)
	}
}

// --- epics & stories with a project ----------------------------------------------

func TestEpicsAndStoriesProjectFilter(t *testing.T) {
	const (
		opsEpics   = `issuetype = Epic AND project = "OPS" AND statusCategory != Done ORDER BY key DESC`
		opsOrphans = `issuetype = Story AND project = "OPS" AND cf[10103] is EMPTY AND statusCategory != Done ORDER BY key DESC`
		opsStories = `issuetype = Story AND project = "OPS" AND statusCategory != Done ORDER BY key DESC`
	)
	fj := treeJira()
	fj.projectsJSON = `[{"key":"RBI","name":"BI"},{"key":"OPS","name":"Ops"}]`
	fj.routes[opsEpics] = fxList(fx{key: "OPS-1", typ: "epic", assignee: "bob", name: "Ops epic", color: "ghx-label-2"})
	fj.routes[opsOrphans] = "[]"
	// Children are not filtered by project.
	fj.routes[`cf[10103] in ("OPS-1") ORDER BY key DESC`] = fxList(fx{key: "RBI-50", assignee: "bob", epic: "OPS-1"})
	fj.routes[opsStories] = fxList(fx{key: "OPS-2", typ: "story", links: []string{"out:relates:RBI-31:task:new"}})
	a := v2App(t, fj, []string{"RBI"}) // OPS is not tracked
	e := getEpicsT(t, a, `{"project":"OPS"}`)
	if len(e.Epics) != 1 || e.Epics[0].Epic.Key != "OPS-1" || refKeys(e.Epics[0].Tasks) != "RBI-50" {
		t.Errorf("epics %+v", e.Epics)
	}
	res, err := a.Call(context.Background(), "getStories", json.RawMessage(`{"project":"OPS"}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := res.(*StoriesResult); storyKeys(s.Stories) != "OPS-2[RBI-31]" {
		t.Errorf("stories %s", storyKeys(s.Stories))
	}
	// Cached per project: the tracked view is a separate entry.
	getEpicsT(t, a, `{"project":"OPS"}`)
	if countJQL(fj, opsEpics) != 1 {
		t.Error("project epics not cached")
	}
	if e := getEpicsT(t, a, `{}`); len(e.Epics) != 2 || countJQL(fj, treeEpicsJQL) != 1 {
		t.Errorf("tracked epics after a project view: %d epics", len(e.Epics))
	}
	for _, q := range fj.searched() {
		if strings.Contains(q, "OPS") && strings.Contains(q, "project in (") {
			t.Errorf("project mixed with tracked: %s", q)
		}
	}
}

// groupOpenSprints on its own: closed and duplicate entries are ignored,
// active sprints without a start date come after dated ones.
func TestGroupOpenSprintsOrder(t *testing.T) {
	it := func(key string, ss ...jira.Sprint) sprintIssue {
		return sprintIssue{issue: IssueDTO{Key: key, Status: StatusDTO{Category: "new"}}, sprints: ss}
	}
	s := func(id int, state, start string) jira.Sprint {
		return jira.Sprint{ID: id, State: state, StartDate: start}
	}
	gs := groupOpenSprints([]sprintIssue{
		it("A-1", s(5, "active", ""), s(5, "active", "")),
		it("A-2", s(9, "future", ""), s(4, "closed", "")),
		it("A-3", s(7, "active", "2026-11-03T10:00:00.000+0200"), s(8, "future", "")),
		it("A-4", s(6, "active", "2026-11-03T10:00:00.000+0200")),
	}, true)
	if got := groupsSummary(gs); got != "6:active=A-4,7:active=A-3,5:active=A-1,8:future=A-3,9:future=A-2" {
		t.Errorf("groups %s", got)
	}
	if !gs[0].Truncated {
		t.Error("groups of a truncated query are truncated")
	}
}

// Category of agile columns: shared category, else "done" for the last
// column (after dropping columns without statuses) with any done status,
// else "indeterminate".
func TestAgileColumnCategory(t *testing.T) {
	known := map[string]StatusDTO{
		"1":     {ID: "1", Name: "ToDo", Category: "new"},
		"3":     {ID: "3", Name: "In Progress", Category: "indeterminate"},
		"10":    {ID: "10", Name: "Готово", Category: "done"},
		"10200": {ID: "10200", Name: "Reject", Category: "done"},
	}
	col := func(name string, ids ...string) jira.BoardColumn {
		c := jira.BoardColumn{Name: name}
		for _, id := range ids {
			c.Statuses = append(c.Statuses, jira.StatusRef{ID: id})
		}
		return c
	}
	summary := func(cols []layoutColumn) string {
		var out []string
		for _, c := range cols {
			out = append(out, c.id+":"+c.category)
		}
		return strings.Join(out, ",")
	}
	// All statuses are known: no lookups, so no client is needed.
	cols, _ := agileColumns(context.Background(), nil, []jira.BoardColumn{
		col("Open", "1"),
		col("Mixed", "1", "10"), // not last, has a done status
		col("Work", "3"),
		col("Finish", "3", "10200"), // last after dropping "Empty"
		col("Empty"),
		col("Foreign", "7", "999"), // another project's statuses: dropped
	}, known, true)
	if got := summary(cols); got != "col-0:new,col-1:indeterminate,col-2:indeterminate,col-3:done" {
		t.Errorf("columns %s", got)
	}
	cols, _ = agileColumns(context.Background(), nil, []jira.BoardColumn{
		col("Done", "10", "10200"), // shared category
		col("Last", "1", "3"),      // last, mixed, nothing done
	}, known, true)
	if got := summary(cols); got != "col-0:done,col-1:indeterminate" {
		t.Errorf("columns %s", got)
	}
}

// Closed sprint candidates are all closed sprints of a board ordered by
// completion, not the last 5 of Jira's listing.
func TestSprintGroupsClosedCandidatesUseAllClosedSprints(t *testing.T) {
	t.Run("recently completed listed early", func(t *testing.T) {
		fj := sprintJira()
		fj.agileBoards = map[string]string{"MB": `[{"id":673,"name":"ETL","type":"scrum"}]`}
		// Jira lists 200 first, but it was completed last.
		fj.sprints = map[int][]string{673: {closedSprintJSON(200, 20), closedSprintJSON(201, 1), closedSprintJSON(202, 2),
			closedSprintJSON(203, 3), closedSprintJSON(204, 4), closedSprintJSON(205, 5)}}
		fj.routes[closedSprintJQL(200)] = issueList(withStatus(fx{key: "MB-40"}, stDone))
		fj.routes[closedSprintJQL(205)] = issueList(withStatus(fx{key: "MB-41"}, stDone))
		fj.routes[closedSprintJQL(204)] = issueList(withStatus(fx{key: "MB-42"}, stDone))
		a := v2App(t, fj, []string{"MB"})
		g := sprintGroupsT(t, a, `{"project":"MB","includeClosed":true}`)
		if got := groupsSummary(g.Groups[5:]); got != "200:closed=MB-40,205:closed=MB-41,204:closed=MB-42" || g.ClosedUnavailable {
			t.Errorf("closed groups %s (closedUnavailable %v)", got, g.ClosedUnavailable)
		}
		// getSprints itself still lists the last 5 of Jira's order.
		res, err := a.Call(context.Background(), "getSprints", json.RawMessage(`{"boardId":673}`))
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range res.([]SprintDTO) {
			ids = append(ids, fmt.Sprint(s.ID))
		}
		if got := strings.Join(ids, ","); got != "205,204,203,202,201" {
			t.Errorf("getSprints %s", got)
		}
	})
	t.Run("mine finds older sprints within the query cap", func(t *testing.T) {
		mine := func(id int) string { return strings.Replace(closedSprintJQL(id), rankOrder, mineClause+rankOrder, 1) }
		fj := sprintJira()
		fj.agileBoards = map[string]string{"MB": `[{"id":673,"name":"ETL","type":"scrum"}]`}
		fj.routes[strings.Replace(specMBOpenJQL, rankOrder, mineClause+rankOrder, 1)] = "[]"
		fj.routes[strings.Replace(specMBBackJQL, rankOrder, mineClause+rankOrder, 1)] = "[]"
		var list []string
		for id := 301; id <= 312; id++ { // completed in id order: 312 is the newest
			list = append(list, closedSprintJSON(id, id-300))
			fj.routes[mine(id)] = "[]"
		}
		fj.sprints = map[int][]string{673: list}
		// The 5 newest (312…308) have none of my issues; 307, 305 and 304 do.
		for _, id := range []int{307, 305, 304} {
			fj.routes[mine(id)] = issueList(withStatus(fx{key: fmt.Sprintf("MB-%d", id), assignee: "alex"}, stDone))
		}
		a := v2App(t, fj, []string{"MB"})
		g := sprintGroupsT(t, a, `{"project":"MB","scope":"mine","includeClosed":true}`)
		if got := groupsSummary(g.Groups); got != "307:closed=MB-307,305:closed=MB-305,304:closed=MB-304" {
			t.Errorf("groups %s", got)
		}
		if countJQL(fj, mine(303)) != 0 {
			t.Error("queried past 3 sprints with issues")
		}

		// Nothing of mine in the 10 newest: at most 10 queries, then stop.
		fj2 := sprintJira()
		fj2.agileBoards = fj.agileBoards
		fj2.sprints = fj.sprints
		for k, v := range fj.routes {
			fj2.routes[k] = v
		}
		for _, id := range []int{307, 305, 304} {
			fj2.routes[mine(id)] = "[]"
		}
		fj2.routes[mine(302)] = issueList(withStatus(fx{key: "MB-302", assignee: "alex"}, stDone)) // 11th newest
		g = sprintGroupsT(t, v2App(t, fj2, []string{"MB"}), `{"project":"MB","scope":"mine","includeClosed":true}`)
		n := 0
		for _, q := range fj2.searched() {
			if strings.Contains(q, "sprint = ") {
				n++
			}
		}
		if len(g.Groups) != 0 || n != maxClosedQueries || g.ClosedUnavailable {
			t.Errorf("groups %s, %d closed queries", groupsSummary(g.Groups), n)
		}
	})
}
