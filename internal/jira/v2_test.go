package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoverFields(t *testing.T) {
	var defs []FieldDef
	if err := json.Unmarshal([]byte(`[
	 {"id":"summary","name":"Summary","custom":false,"clauseNames":["summary"],"schema":{"type":"string","system":"summary"}},
	 {"id":"customfield_10001","name":"Story Points","custom":true,"clauseNames":["cf[10001]","Story Points"],"schema":{"type":"string","custom":"x:text"}},
	 {"id":"customfield_10002","name":"Story point estimate","custom":true,"clauseNames":["cf[10002]","Story point estimate"],"schema":{"type":"number"}},
	 {"id":"customfield_10103","name":"Epic Link","custom":true,"clauseNames":["cf[10103]","Epic Link"],"schema":{"type":"any","custom":"com.pyxis.greenhopper.jira:gh-epic-link"}},
	 {"id":"customfield_10105","name":"Epic Name","custom":true,"clauseNames":["Epic Name"],"schema":{"type":"string","custom":"com.pyxis.greenhopper.jira:gh-epic-label"}},
	 {"id":"customfield_10106","name":"Epic Color","custom":true,"clauseNames":["Epic Colour"],"schema":{"type":"string","custom":"com.pyxis.greenhopper.jira:gh-epic-color"}},
	 {"id":"customfield_10107","name":"Sprint","custom":true,"clauseNames":["Sprint"],"schema":{"type":"array","items":"string","custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
	 {"id":"customfield_10130","name":"Story Points","custom":true,"clauseNames":["cf[10130]","story points"],"schema":{"type":"number","custom":"x:float"}},
	 {"id":"customfield_10999","name":"Other SP","custom":true,"clauseNames":["Story Points"],"schema":{"type":"number"}},
	 {"id":"broken","name":"no schema"}
	]`), &defs); err != nil {
		t.Fatal(err)
	}
	got := DiscoverFields(defs)
	want := FieldIDs{EpicLink: "customfield_10103", EpicName: "customfield_10105", EpicColor: "customfield_10106",
		Sprint: "customfield_10107", StoryPoints: "customfield_10130"}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if l := strings.Join(got.List(), ","); l != "customfield_10103,customfield_10105,customfield_10106,customfield_10107,customfield_10130" {
		t.Errorf("list %s", l)
	}
	// A Jira without Jira Software: nothing found, nothing crashes.
	if got := DiscoverFields(defs[:1]); got != (FieldIDs{}) || len(got.List()) != 0 {
		t.Errorf("plain Jira: %+v", got)
	}
	for in, want := range map[string]string{"customfield_10103": "cf[10103]", "summary": "", "customfield_": "", "customfield_1x": "", "": ""} {
		if got := ClauseRef(in); got != want {
			t.Errorf("ClauseRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLegacySprint(t *testing.T) {
	s := "com.atlassian.greenhopper.service.sprint.Sprint@14b1c359[id=6338,rapidViewId=880,state=CLOSED," +
		"name=RBI Sprint 12, part 2,startDate=2026-10-27T10:00:00.000+02:00,endDate=2026-11-10T10:00:00.000+02:00," +
		"completeDate=<null>,activatedDate=2026-10-27T10:05:00.000+02:00,sequence=6338,goal=Ship a=b, then rest,autoStartStop=false]"
	got, ok := ParseLegacySprint(s)
	want := Sprint{ID: 6338, Name: "RBI Sprint 12, part 2", State: "closed", StartDate: "2026-10-27T10:00:00.000+02:00",
		EndDate: "2026-11-10T10:00:00.000+02:00", Goal: "Ship a=b, then rest", BoardID: 880}
	if !ok || got != want {
		t.Errorf("got %+v (%v)\nwant %+v", got, ok, want)
	}
	for _, bad := range []string{"", "Sprint@1", "x[]", "x[name=no id]", "x[id=abc,name=n]"} {
		if _, ok := ParseLegacySprint(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestParseSprintsBothForms(t *testing.T) {
	raw := json.RawMessage(`[
	 "com.atlassian.greenhopper.service.sprint.Sprint@1[id=1,rapidViewId=880,state=ACTIVE,name=S1,goal=<null>]",
	 {"id":2,"name":"S2","state":"FUTURE","boardId":881,"goal":"g"},
	 {"id":3,"name":"S3","state":"closed","originBoardId":882,"completeDate":"2026-11-01T10:00:00.000+02:00"},
	 "garbage", 42, {"name":"no id"}
	]`)
	got := ParseSprints(raw)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].ID != 1 || got[0].State != "active" || got[0].Goal != "" || got[0].BoardID != 880 ||
		got[1].ID != 2 || got[1].State != "future" || got[1].BoardID != 881 || got[1].Goal != "g" ||
		got[2].BoardID != 882 || got[2].CompleteDate == "" {
		t.Errorf("got %+v", got)
	}
	for _, r := range []string{``, `null`, `"x"`, `{}`} {
		if got := ParseSprints(json.RawMessage(r)); got != nil {
			t.Errorf("%s: %+v", r, got)
		}
	}
}

func TestCustomFieldValues(t *testing.T) {
	var is Issue
	if err := json.Unmarshal([]byte(`{"key":"A-1","fields":{"summary":"S","issuetype":{"id":"10001","name":"История","subtask":false},
	  "customfield_1":"E-1","customfield_2":5.5,"customfield_3":null,"customfield_4":{"value":"ghx-label-2"},"customfield_5":[1]}}`), &is); err != nil {
		t.Fatal(err)
	}
	f := &is.Fields
	if f.Summary != "S" || f.IssueType.ID != "10001" || f.CustomString("customfield_1") != "E-1" || f.CustomString("customfield_4") != "ghx-label-2" {
		t.Errorf("fields %+v", f)
	}
	if n := f.CustomNumber("customfield_2"); n == nil || *n != 5.5 {
		t.Errorf("number %v", n)
	}
	for _, id := range []string{"customfield_3", "customfield_5", "customfield_404", ""} {
		if f.CustomNumber(id) != nil || f.CustomString(id) != "" {
			t.Errorf("%s should be empty", id)
		}
	}
}

func TestBoardSprintsFollowsIsLast(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		if u.Path != "/rest/agile/1.0/board/880/sprint" || u.Query().Get("state") != "closed" {
			return &Response{StatusCode: 404}, nil
		}
		start, _ := strconv.Atoi(u.Query().Get("startAt"))
		var vals []string
		for i := start; i < start+50 && i < 120; i++ {
			vals = append(vals, fmt.Sprintf(`{"id":%d,"name":"S%d","state":"closed","originBoardId":880}`, i+1, i+1))
		}
		return &Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":%d,"maxResults":50,"isLast":%v,"values":[%s]}`,
			start, start+50 >= 120, strings.Join(vals, ",")))}, nil
	}}
	c := New("https://sd.local", "t", ft)
	got, err := c.BoardSprints(context.Background(), 880, "closed", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 120 || got[119].ID != 120 || got[0].BoardID != 880 || len(ft.reqs) != 3 {
		t.Errorf("got %d sprints in %d requests", len(got), len(ft.reqs))
	}
	if got, _ := c.BoardSprints(context.Background(), 880, "closed", 60); len(got) != 60 || len(ft.reqs) != 5 {
		t.Errorf("limit: %d sprints, %d requests", len(got), len(ft.reqs))
	}
}

func TestCommentsReturnsTheNewestPagesOldestFirst(t *testing.T) {
	const total = 230
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		start, _ := strconv.Atoi(u.Query().Get("startAt"))
		max, _ := strconv.Atoi(u.Query().Get("maxResults"))
		if max > 50 {
			max = 50
		}
		var cs []string
		for i := start; i < total && i < start+max; i++ {
			cs = append(cs, fmt.Sprintf(`{"id":"%d"}`, i+1))
		}
		return &Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"total":%d,"comments":[%s]}`,
			start, max, total, strings.Join(cs, ",")))}, nil
	}}
	got, err := New("https://sd.local", "t", ft).Comments(context.Background(), "A-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 200 || got[0].ID != "31" || got[199].ID != "230" {
		t.Errorf("got %d comments, %s … %s", len(got), got[0].ID, got[len(got)-1].ID)
	}
	if _, err := New("https://sd.local", "t", ft).Comments(context.Background(), "../x", 200); !IsCode(err, CodeBadRequest) {
		t.Errorf("invalid key: %v", err)
	}
}

func TestValidProjectKey(t *testing.T) {
	for k, want := range map[string]bool{"RBI": true, "A1_B": true, "a": true, "": false, "1AB": false, "A-1": false,
		`A"`: false, "A B": false, strings.Repeat("A", 65): false} {
		if ValidProjectKey(k) != want {
			t.Errorf("ValidProjectKey(%q) != %v", k, want)
		}
	}
}

func TestSearchAllReportsTotal(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":2,"total":7,"issues":[{"key":"A-1"},{"key":"A-2"}]}`)}, nil
	}}
	res, err := New("https://sd.local", "t", ft).SearchAll(context.Background(), "x", SearchOptions{Limit: 2})
	if err != nil || len(res.Issues) != 2 || res.Total != 7 || !res.Truncated() {
		t.Errorf("res %+v err %v", res, err)
	}
}

// CR-6: only top-level customfield_* keys are kept; nested values and text
// mentioning "customfield_" are not.
func TestCustomFieldsTopLevelOnly(t *testing.T) {
	var is Issue
	if err := json.Unmarshal([]byte(`{"key":"A-1","fields":{"summary":"\"customfield_9\" in text",
	  "comment":{"total":1,"comments":[{"id":"1","body":"x","customfield_7":"nested"}]},
	  "parent":{"key":"A-0","fields":{"customfield_8":"child"}},"customfield_1":"top"}}`), &is); err != nil {
		t.Fatal(err)
	}
	if len(is.Fields.Custom) != 1 || is.Fields.CustomString("customfield_1") != "top" || is.Fields.Summary != `"customfield_9" in text` {
		t.Errorf("custom %v", is.Fields.Custom)
	}
	if is.Fields.Parent == nil || is.Fields.Parent.Fields.CustomString("customfield_8") != "child" || len(is.Fields.Comment.Comments) != 1 {
		t.Errorf("nested issue %+v", is.Fields.Parent)
	}
	var plain Issue
	if err := json.Unmarshal([]byte(`{"key":"A-2","fields":{"summary":"s"}}`), &plain); err != nil || plain.Fields.Custom != nil {
		t.Errorf("plain %+v %v", plain.Fields, err)
	}
	if err := json.Unmarshal([]byte(`{"key":"A-3","fields":{"customfield_1":}}`), &plain); err == nil {
		t.Error("malformed JSON accepted")
	}
}
