package jira

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDiscoverV6Fields(t *testing.T) {
	tests := []struct {
		name string
		defs string
		want FieldIDs
	}{
		{"real Jira names", `[
		 {"id":"customfield_13619","name":"Merge_requests","schema":{"type":"string","custom":"x:textarea"}},
		 {"id":"customfield_13702","name":"Branches","schema":{"type":"string","custom":"x:textarea"}},
		 {"id":"customfield_10400","name":"Developer","schema":{"type":"array","items":"user"}},
		 {"id":"customfield_10402","name":"Reviewer","schema":{"type":"array","items":"user"}},
		 {"id":"customfield_10401","name":"QA Engineer","schema":{"type":"array","items":"user"}}]`,
			FieldIDs{MergeRequests: "customfield_13619", Branches: "customfield_13702", Developer: "customfield_10400",
				Reviewer: "customfield_10402", QA: "customfield_10401"}},
		{"clause names, case and spaces", `[
		 {"id":"customfield_1","name":"MR","clauseNames":[" merge requests "]},
		 {"id":"customfield_2","name":"x","clauseNames":["BRANCHES"]},
		 {"id":"customfield_3","name":" developer "}]`,
			FieldIDs{MergeRequests: "customfield_1", Branches: "customfield_2", Developer: "customfield_3"}},
		{"QA Engineer wins over QA wherever it is", `[
		 {"id":"customfield_7","name":"QA"},
		 {"id":"customfield_8","name":"QA Engineer"}]`,
			FieldIDs{QA: "customfield_8"}},
		{"QA as the fallback", `[{"id":"customfield_9","name":"qa"}]`, FieldIDs{QA: "customfield_9"}},
		{"similar names do not match", `[
		 {"id":"customfield_10","name":"Developers"},
		 {"id":"customfield_11","name":"Merge request"},
		 {"id":"customfield_12","name":"QA Story Points"},
		 {"id":"customfield_13","name":"Branch"}]`,
			FieldIDs{QAStoryPoints: "customfield_12"}},
	}
	for _, tc := range tests {
		var defs []FieldDef
		if err := json.Unmarshal([]byte(tc.defs), &defs); err != nil {
			t.Fatal(err)
		}
		if got := DiscoverFields(defs); got != tc.want {
			t.Errorf("%s: %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestPanelClientCalls(t *testing.T) {
	var calls []string
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL, "https://sd.local")+" "+string(r.Body))
		switch {
		case strings.Contains(r.URL, "/issueLinkType"):
			return &Response{StatusCode: 200, Body: []byte(`{"issueLinkTypes":[{"id":"10000","name":"Blocks","inward":"is blocked by","outward":"blocks","self":"x"}]}`)}, nil
		case strings.Contains(r.URL, "/user/search"):
			return &Response{StatusCode: 200, Body: []byte(`[{"name":"bob","displayName":"Bob"}]`)}, nil
		case strings.HasSuffix(r.URL, "/comment"):
			return &Response{StatusCode: 201, Body: []byte(`{"id":"77","body":"hi","author":{"name":"alex"},"created":"2026-11-12T10:00:00.000+0200"}`)}, nil
		}
		return &Response{StatusCode: 201}, nil
	}}
	c := New("https://sd.local", "t", ft)
	ctx := context.Background()
	if lt, err := c.LinkTypes(ctx); err != nil || len(lt) != 1 || lt[0] != (LinkType{ID: "10000", Name: "Blocks", Inward: "is blocked by", Outward: "blocks"}) {
		t.Errorf("link types %+v %v", lt, err)
	}
	if err := c.CreateIssueLink(ctx, "10000", "RBI-1", "RBI-2"); err != nil {
		t.Error(err)
	}
	if err := c.DeleteIssueLink(ctx, "501"); err != nil {
		t.Error(err)
	}
	if us, err := c.SearchUsers(ctx, "bo", 20); err != nil || len(us) != 1 {
		t.Errorf("users %+v %v", us, err)
	}
	if cm, err := c.AddComment(ctx, "RBI-1", "hi"); err != nil || cm.ID != "77" {
		t.Errorf("comment %+v %v", cm, err)
	}
	want := []string{
		"GET /rest/api/2/issueLinkType ",
		`POST /rest/api/2/issueLink {"inwardIssue":{"key":"RBI-1"},"outwardIssue":{"key":"RBI-2"},"type":{"id":"10000"}}`,
		"DELETE /rest/api/2/issueLink/501 ",
		"GET /rest/api/2/user/search?maxResults=20&username=bo ",
		`POST /rest/api/2/issue/RBI-1/comment {"body":"hi"}`,
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	n := len(calls)
	for name, err := range map[string]error{
		"link type not numeric": c.CreateIssueLink(ctx, "x", "RBI-1", "RBI-2"),
		"link bad key":          c.CreateIssueLink(ctx, "1", "RBI 1", "RBI-2"),
		"delete bad id":         c.DeleteIssueLink(ctx, "../1"),
		"comment bad key":       func() error { _, err := c.AddComment(ctx, "x", "b"); return err }(),
	} {
		if !IsCode(err, CodeBadRequest) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(calls) != n {
		t.Errorf("invalid arguments reached Jira: %v", calls[n:])
	}
}
