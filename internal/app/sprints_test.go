package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sprintJSON(id int, state string) string {
	return fmt.Sprintf(`{"id":%d,"name":"S%d","state":%q,"originBoardId":880,"startDate":"2026-11-03T10:00:00.000+02:00","goal":"g%d"}`, id, id, state, id)
}

func TestGetSprintsOrdering(t *testing.T) {
	var list []string
	for i := 1001; i <= 1060; i++ { // 60 closed sprints, oldest first: two pages
		list = append(list, sprintJSON(i, "closed"))
	}
	list = append(list, sprintJSON(302, "future"), sprintJSON(201, "active"), sprintJSON(301, "future"))
	fj := &fakeJira{sprints: map[int][]string{880: list}, kanban: map[int]bool{881: true}}
	a := v2App(t, fj, []string{"RBI"})
	ctx := context.Background()
	res, err := a.Call(ctx, "getSprints", json.RawMessage(`{"boardId":880}`))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range res.([]SprintDTO) {
		ids = append(ids, fmt.Sprintf("%d:%s", s.ID, s.State))
	}
	// Active, then future (Jira's order), then the last 5 closed, newest first.
	if got := strings.Join(ids, ","); got != "201:active,302:future,301:future,1060:closed,1059:closed,1058:closed,1057:closed,1056:closed" {
		t.Errorf("sprints %s", got)
	}
	if s := res.([]SprintDTO)[0]; s.BoardID != 880 || s.Goal != "g201" || !sameInstant(s.StartDate, "2026-11-03T10:00:00+02:00") || s.EndDate != "" {
		t.Errorf("sprint %+v", s)
	}
	if n := len(fj.requests("/rest/agile/1.0/board/880/sprint?")); n != 3 { // open: 1 page, closed: 2 pages
		t.Errorf("%d sprint requests", n)
	}
	// A kanban board has no sprints.
	res, err = a.Call(ctx, "getSprints", json.RawMessage(`{"boardId":881}`))
	if err != nil || len(res.([]SprintDTO)) != 0 {
		t.Errorf("kanban: %v %v", res, err)
	}
	if b, _ := json.Marshal(res); string(b) != "[]" {
		t.Errorf("kanban JSON %s", b)
	}
	for _, p := range []string{`{"boardId":0}`, `{}`, `{"boardId":"880"}`} {
		if _, err := a.Call(ctx, "getSprints", json.RawMessage(p)); ToError(err).Code != CodeBadParams {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestGetAgileBoards(t *testing.T) {
	fj := &fakeJira{agileBoards: map[string]string{
		"RBI": `[{"id":880,"name":"BI DEV Agile board","type":"scrum","location":{"projectKey":"RBI"}},{"id":881,"name":"Shared","type":"kanban"}]`,
		"OPS": `[{"id":881,"name":"Shared","type":"kanban"},{"id":900,"name":"Ops","type":"scrum","location":{"projectKey":"OPS"}}]`,
		"XYZ": `[{"id":999,"name":"Untracked","type":"scrum"}]`,
	}}
	a := v2App(t, fj, []string{"RBI", "OPS"})
	ctx := context.Background()
	res, err := a.Call(ctx, "getAgileBoards", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range res.([]AgileBoardDTO) {
		got = append(got, fmt.Sprintf("%d:%s:%s", b.ID, b.Type, b.ProjectKey))
	}
	if s := strings.Join(got, ","); s != "881:kanban:OPS,900:scrum:OPS,880:scrum:RBI" {
		t.Errorf("boards %s (tracked projects only, deduped by id)", s)
	}
	n := len(fj.requests("/rest/agile/1.0/board?"))
	if _, err := a.Call(ctx, "getAgileBoards", nil); err != nil || len(fj.requests("/rest/agile/1.0/board?")) != n {
		t.Errorf("not cached: %v", err)
	}

	// Without the agile API (no Jira Software): [] and no error.
	fj2 := &fakeJira{agileMissing: true}
	b := v2App(t, fj2, []string{"RBI"})
	res, err = b.Call(ctx, "getAgileBoards", json.RawMessage(`{"force":true}`))
	if err != nil || len(res.([]AgileBoardDTO)) != 0 {
		t.Errorf("no agile API: %v %v", res, err)
	}
}
