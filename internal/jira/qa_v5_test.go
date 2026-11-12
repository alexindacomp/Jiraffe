package jira

// QA tests for the v0.5 create/edit meta client: paging edges (servers
// that ignore startAt, report neither isLast nor total, or cut pages short),
// the legacy fallback (only on 404, project filtering, case of the key) and
// that an unpaged legacy answer is never used for another project.

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func qaTypes(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf(`{"id":"%d","name":"T%d"}`, i, i))
	}
	return out
}

// pagedServer answers the paged createmeta type list with body(start).
func pagedServer(body func(start int) string) *fakeTransport {
	return &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		if u.Path != "/rest/api/2/issue/createmeta/RBI/issuetypes" {
			return &Response{StatusCode: 404}, nil
		}
		start, _ := strconv.Atoi(u.Query().Get("startAt"))
		return &Response{StatusCode: 200, Body: []byte(body(start))}, nil
	}}
}

func TestV5QAMetaPagingEdges(t *testing.T) {
	all := qaTypes(250)
	slice := func(start, n int) string {
		end := min(start+n, len(all))
		start = min(start, len(all))
		return strings.Join(all[start:end], ",")
	}
	tests := []struct {
		name     string
		body     func(start int) string
		types    int // types returned
		maxReqs  int // requests at most
		firstIDs string
	}{
		{"isLast only, pages of 100", func(s int) string {
			return fmt.Sprintf(`{"startAt":%d,"maxResults":100,"isLast":%v,"values":[%s]}`, s, s+100 >= 250, slice(s, 100))
		}, 200, 2, "1,2"}, // capped at maxMetaTypes
		{"neither isLast nor total: stops at an empty page", func(s int) string {
			return fmt.Sprintf(`{"startAt":%d,"maxResults":100,"values":[%s]}`, s, slice(s, 100))
		}, 200, 3, "1,2"},
		{"server ignores startAt (same first page forever)", func(int) string {
			return fmt.Sprintf(`{"startAt":0,"maxResults":100,"isLast":false,"values":[%s]}`, slice(0, 100))
		}, 100, 2, "1,2"}, // the repeated page is recognised and dropped
		{"empty first page, not last", func(int) string {
			return `{"startAt":0,"maxResults":100,"isLast":false,"values":[]}`
		}, 0, 1, ""},
		{"total smaller than the values sent", func(s int) string {
			return fmt.Sprintf(`{"startAt":%d,"maxResults":100,"total":3,"values":[%s]}`, s, slice(s, 5))
		}, 5, 1, "1,2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := pagedServer(tc.body)
			got, err := New("https://sd.local", "t", ft).CreateMetaIssueTypes(context.Background(), "RBI")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.types || len(ft.reqs) > tc.maxReqs {
				t.Errorf("%d types in %d requests, want %d in ≤ %d", len(got), len(ft.reqs), tc.types, tc.maxReqs)
			}
			seen := map[string]bool{}
			for _, ty := range got {
				if seen[ty.ID] {
					t.Errorf("type %s listed twice", ty.ID)
				}
				seen[ty.ID] = true
			}
			if len(got) >= 2 && got[0].ID+","+got[1].ID != tc.firstIDs {
				t.Errorf("first ids %s,%s", got[0].ID, got[1].ID)
			}
		})
	}
}

// The legacy endpoint is only a fallback for 404 (Jira < 8.4): other errors
// of the paged endpoint are reported as they are.
func TestV5QALegacyOnlyOn404(t *testing.T) {
	for _, status := range []int{400, 401, 403, 500, 503} {
		legacy := 0
		ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
			u, _ := url.Parse(r.URL)
			if u.Path == "/rest/api/2/issue/createmeta" {
				legacy++
				return &Response{StatusCode: 200, Body: []byte(`{"projects":[{"key":"RBI","issuetypes":[{"id":"3"}]}]}`)}, nil
			}
			return &Response{StatusCode: status, Body: []byte(`{"errorMessages":["x"]}`)}, nil
		}}
		c := New("https://sd.local", "t", ft)
		if _, err := c.CreateMetaIssueTypes(context.Background(), "RBI"); err == nil || legacy != 0 {
			t.Errorf("HTTP %d: err %v, legacy calls %d", status, err, legacy)
		}
		if _, err := c.CreateMetaFields(context.Background(), "RBI", "3"); err == nil || legacy != 0 {
			t.Errorf("HTTP %d fields: err %v, legacy calls %d", status, err, legacy)
		}
	}
}

// Legacy fallback: only the asked project's types and fields are used,
// whatever else (or whatever case) Jira returns; a 404 on a later page
// of the paged endpoint also falls back.
func TestV5QALegacyFiltering(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		u, _ := url.Parse(r.URL)
		q := u.Query()
		switch {
		case u.Path == "/rest/api/2/issue/createmeta/LEG/issuetypes" && q.Get("startAt") == "0":
			return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":1,"isLast":false,"values":[{"id":"99","name":"Paged"}]}`)}, nil
		case strings.HasPrefix(u.Path, "/rest/api/2/issue/createmeta/"):
			return &Response{StatusCode: 404}, nil
		case u.Path == "/rest/api/2/issue/createmeta" && q.Get("expand") == "":
			return &Response{StatusCode: 200, Body: []byte(`{"projects":[
			 {"key":"EVIL","issuetypes":[{"id":"1","name":"Evil"}]},
			 {"key":"leg","issuetypes":[{"id":"3","name":"Task"}]}]}`)}, nil
		case u.Path == "/rest/api/2/issue/createmeta":
			return &Response{StatusCode: 200, Body: []byte(`{"projects":[
			 {"key":"EVIL","issuetypes":[{"id":"3","fields":{"customfield_666":{"name":"Evil","required":true}}}]},
			 {"key":"LEG","issuetypes":[{"id":"4","fields":{"customfield_777":{"name":"Other type"}}},
			                            {"id":"3","fields":{"summary":{"name":"Summary","required":true}}}]}]}`)}, nil
		}
		return &Response{StatusCode: 404}, nil
	}}
	c := New("https://sd.local", "t", ft)
	types, err := c.CreateMetaIssueTypes(context.Background(), "LEG")
	if err != nil || len(types) != 1 || types[0].ID != "3" {
		t.Errorf("types %+v %v (the paged page before the 404 must not leak in, EVIL never)", types, err)
	}
	fields, err := c.CreateMetaFields(context.Background(), "LEG", "3")
	if err != nil || len(fields) != 1 || fields[0].ID != "summary" || !fields[0].Required {
		t.Errorf("fields %+v %v", fields, err)
	}
	// The legacy request is restricted to the project and the type.
	var legacyQueries []string
	for _, r := range ft.reqs {
		if u, _ := url.Parse(r.URL); u.Path == "/rest/api/2/issue/createmeta" {
			legacyQueries = append(legacyQueries, u.RawQuery)
		}
	}
	if len(legacyQueries) != 2 || legacyQueries[0] != "projectKeys=LEG" ||
		legacyQueries[1] != "expand=projects.issuetypes.fields&issuetypeIds=3&projectKeys=LEG" {
		t.Errorf("legacy queries %v", legacyQueries)
	}
}

// Field errors survive any 4xx status with an error body, and a body
// without them leaves both nil (so the app does not turn it into an empty
// validation error).
func TestV5QAFieldErrorsParsing(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		fields int
		msgs   int
	}{
		{400, `{"errorMessages":[],"errors":{}}`, 0, 0},
		{400, `{"errors":{"a":"x","b":"y"}}`, 2, 0},
		{400, `not json`, 0, 0},
		{403, `{"errorMessages":["no"]}`, 0, 1},
		{400, `{"errorMessages":["one","two"],"errors":{"summary":"s"}}`, 1, 2},
	} {
		ft := &fakeTransport{fn: func(*Request) (*Response, error) {
			return &Response{StatusCode: tc.status, Body: []byte(tc.body)}, nil
		}}
		err := New("https://sd.local", "t", ft).UpdateIssue(context.Background(), "RBI-1", map[string]any{})
		var je *Error
		if !asError(err, &je) || len(je.FieldErrors) != tc.fields || len(je.Messages) != tc.msgs {
			t.Errorf("%d %s: %#v", tc.status, tc.body, err)
		}
	}
}
