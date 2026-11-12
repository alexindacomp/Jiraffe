package jira

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCreateMetaPaging(t *testing.T) {
	var types []string
	for i := 1; i <= 7; i++ {
		types = append(types, fmt.Sprintf(`{"id":"%d","name":"T%d","subtask":%v}`, i, i, i == 7))
	}
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		if u.Path != "/rest/api/2/issue/createmeta/RBI/issuetypes" {
			return &Response{StatusCode: 404}, nil
		}
		start, _ := strconv.Atoi(u.Query().Get("startAt"))
		end := start + 3 // the server pages by 3 whatever is asked
		if end > len(types) {
			end = len(types)
		}
		// total only, no isLast (some Jira versions)
		return &Response{StatusCode: 200, Body: []byte(fmt.Sprintf(`{"startAt":%d,"maxResults":3,"total":7,"values":[%s]}`,
			start, strings.Join(types[start:end], ",")))}, nil
	}}
	got, err := New("https://sd.local", "t", ft).CreateMetaIssueTypes(context.Background(), "RBI")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 || got[6].ID != "7" || !got[6].Subtask || len(ft.reqs) != 3 {
		t.Errorf("%d types %+v in %d requests", len(got), got, len(ft.reqs))
	}
	if q := ft.reqs[1].URL; !strings.Contains(q, "startAt=3") || !strings.Contains(q, "maxResults=100") {
		t.Errorf("second page %s", q)
	}
}

func TestCreateMetaLegacy(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		q := u.Query()
		switch {
		case strings.HasPrefix(u.Path, "/rest/api/2/issue/createmeta/"):
			return &Response{StatusCode: 404, Body: []byte(`{"errorMessages":["null for uri"]}`)}, nil
		case u.Path == "/rest/api/2/issue/createmeta" && q.Get("projectKeys") == "LEG" && q.Get("issuetypeIds") == "3" &&
			q.Get("expand") == "projects.issuetypes.fields":
			return &Response{StatusCode: 200, Body: []byte(`{"projects":[{"key":"LEG","issuetypes":[{"id":"3","name":"Task","fields":{
			 "customfield_10010":{"name":"B","required":false},"summary":{"name":"Summary","required":true,"schema":{"type":"string","system":"summary"}},
			 "customfield_9":{"name":"A","required":true},"description":{"name":"Description"}}}]}]}`)}, nil
		case u.Path == "/rest/api/2/issue/createmeta" && q.Get("projectKeys") == "LEG":
			return &Response{StatusCode: 200, Body: []byte(`{"projects":[{"key":"OTHER","issuetypes":[{"id":"9"}]},{"key":"LEG","issuetypes":[{"id":"3","name":"Task"},{"id":"5","name":"Sub","subtask":true}]}]}`)}, nil
		}
		return &Response{StatusCode: 404}, nil
	}}
	c := New("https://sd.local", "t", ft)
	types, err := c.CreateMetaIssueTypes(context.Background(), "LEG")
	if err != nil || len(types) != 2 || types[1].ID != "5" || !types[1].Subtask {
		t.Fatalf("types %+v %v", types, err)
	}
	fields, err := c.CreateMetaFields(context.Background(), "LEG", "3")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, f := range fields {
		ids = append(ids, f.ID)
	}
	// Ids from the map keys; system fields first, custom ones by number.
	if strings.Join(ids, ",") != "description,summary,customfield_9,customfield_10010" || !fields[1].Required || fields[1].Schema.System != "summary" {
		t.Errorf("fields %v %+v", ids, fields)
	}
	if _, err := c.CreateMetaFields(context.Background(), "LEG", "7"); !IsCode(err, CodeNotFound) {
		t.Errorf("type not on the legacy screen: %v", err)
	}
	for _, bad := range [][2]string{{"a b", "3"}, {"LEG", "x"}, {"LEG", ""}} {
		if _, err := c.CreateMetaFields(context.Background(), bad[0], bad[1]); !IsCode(err, CodeBadRequest) {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestWritesAndFieldErrors(t *testing.T) {
	var calls []string
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		calls = append(calls, r.Method+" "+r.URL+" "+string(r.Body))
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL, "/rest/api/2/issue"):
			return &Response{StatusCode: 400, Body: []byte(`{"errorMessages":["Field 'x' cannot be set."],"errors":{"summary":"You must specify a summary."}}`)}, nil
		case r.Method == "PUT":
			return &Response{StatusCode: 204}, nil
		case strings.Contains(r.URL, "/editmeta"):
			return &Response{StatusCode: 200, Body: []byte(`{"fields":{"summary":{"required":true,"name":"Summary"}}}`)}, nil
		case strings.Contains(r.URL, "/user/assignable/search"):
			return &Response{StatusCode: 200, Body: []byte(`[{"name":"bob","displayName":"Bob"}]`)}, nil
		}
		return &Response{StatusCode: 204}, nil
	}}
	c := New("https://sd.local", "t", ft)
	ctx := context.Background()
	_, err := c.CreateIssue(ctx, map[string]any{"summary": ""})
	var je *Error
	if !asError(err, &je) || je.Code != CodeBadRequest || je.FieldErrors["summary"] != "You must specify a summary." ||
		len(je.Messages) != 1 || je.Messages[0] != "Field 'x' cannot be set." {
		t.Errorf("create error %#v", err)
	}
	if err := c.UpdateIssue(ctx, "RBI-1", map[string]any{"summary": "x"}); err != nil {
		t.Error(err)
	}
	if fs, err := c.EditMeta(ctx, "RBI-1"); err != nil || len(fs) != 1 || fs[0].ID != "summary" {
		t.Errorf("editmeta %+v %v", fs, err)
	}
	if us, err := c.AssignableUsers(ctx, "RBI", "", "b", 20); err != nil || len(us) != 1 {
		t.Errorf("users %+v %v", us, err)
	}
	if err := c.MoveToSprint(ctx, 12, []string{"RBI-1"}); err != nil {
		t.Error(err)
	}
	if err := c.MoveToBacklog(ctx, []string{"RBI-1"}); err != nil {
		t.Error(err)
	}
	want := []string{
		`POST https://sd.local/rest/api/2/issue {"fields":{"summary":""}}`,
		`PUT https://sd.local/rest/api/2/issue/RBI-1 {"fields":{"summary":"x"}}`,
		`GET https://sd.local/rest/api/2/issue/RBI-1/editmeta `,
		`GET https://sd.local/rest/api/2/user/assignable/search?maxResults=20&project=RBI&username=b `,
		`POST https://sd.local/rest/agile/1.0/sprint/12/issue {"issues":["RBI-1"]}`,
		`POST https://sd.local/rest/agile/1.0/backlog/issue {"issues":["RBI-1"]}`,
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	n := len(calls)
	for name, err := range map[string]error{
		"update bad key":     c.UpdateIssue(ctx, "RBI 1", nil),
		"sprint 0":           c.MoveToSprint(ctx, 0, nil),
		"assignable no args": func() error { _, err := c.AssignableUsers(ctx, "", "", "", 20); return err }(),
		"assignable bad key": func() error { _, err := c.AssignableUsers(ctx, "", "x", "", 20); return err }(),
		"editmeta bad key":   func() error { _, err := c.EditMeta(ctx, "../x"); return err }(),
	} {
		if !IsCode(err, CodeBadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(calls) != n {
		t.Errorf("invalid arguments reached Jira: %v", calls[n:])
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// QA-3: a server that ignores startAt answers the first page again; the
// list stops instead of repeating it up to the limit.
func TestCreateMetaPagingIgnoredStartAt(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":2,"total":50,"isLast":false,
		  "values":[{"id":"1","name":"A"},{"id":"2","name":"B"}]}`)}, nil
	}}
	got, err := New("https://sd.local", "t", ft).CreateMetaIssueTypes(context.Background(), "RBI")
	if err != nil || len(got) != 2 || got[1].ID != "2" || len(ft.reqs) != 2 {
		t.Errorf("%d types %+v in %d requests, %v", len(got), got, len(ft.reqs), err)
	}
}
