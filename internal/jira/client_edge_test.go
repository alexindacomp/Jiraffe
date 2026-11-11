package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// pagingSearch is a fake /search endpoint over total issues that honours
// startAt and maxResults, capping pages at serverMax like a real Jira does.
func pagingSearch(total, serverMax int) func(r *Request) (*Response, error) {
	return func(r *Request) (*Response, error) {
		u, err := url.Parse(r.URL)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		start, _ := strconv.Atoi(q.Get("startAt"))
		max, _ := strconv.Atoi(q.Get("maxResults"))
		if max > serverMax {
			max = serverMax
		}
		var keys []string
		for i := start; i < total && i < start+max; i++ {
			keys = append(keys, fmt.Sprintf(`{"key":"P-%d"}`, i+1))
		}
		body := fmt.Sprintf(`{"startAt":%d,"maxResults":%d,"total":%d,"issues":[%s]}`, start, max, total, strings.Join(keys, ","))
		return &Response{StatusCode: 200, Body: []byte(body)}, nil
	}
}

func queryOf(t *testing.T, r *Request) url.Values {
	t.Helper()
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatalf("bad url %q: %v", r.URL, err)
	}
	return u.Query()
}

func TestSearchPagination(t *testing.T) {
	tests := []struct {
		name        string
		total       int
		serverMax   int
		limit       int
		wantCount   int
		wantStarts  []string
		wantMaxRess []string
	}{
		{"250 issues in pages of 100", 250, 100, 0, 250, []string{"0", "100", "200"}, []string{"100", "100", "100"}},
		{"limit cuts the last page", 250, 100, 150, 150, []string{"0", "100"}, []string{"100", "50"}},
		{"server caps pages at 50", 120, 50, 0, 120, []string{"0", "50", "100"}, []string{"100", "100", "100"}},
		{"exactly one full page", 100, 100, 0, 100, []string{"0"}, []string{"100"}},
		{"no issues", 0, 100, 0, 0, []string{"0"}, []string{"100"}},
		{"default limit is 500", 1000, 100, 0, 500, []string{"0", "100", "200", "300", "400"}, []string{"100", "100", "100", "100", "100"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTransport{fn: pagingSearch(tc.total, tc.serverMax)}
			got, err := New("https://sd.local", "t", ft).Search(context.Background(), "project = P", SearchOptions{Limit: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("got %d issues, want %d", len(got), tc.wantCount)
			}
			for i, is := range got {
				if is.Key != fmt.Sprintf("P-%d", i+1) {
					t.Fatalf("issue %d is %s: order or duplicates broken", i, is.Key)
				}
			}
			var starts, maxes []string
			for _, r := range ft.reqs {
				q := queryOf(t, r)
				starts = append(starts, q.Get("startAt"))
				maxes = append(maxes, q.Get("maxResults"))
			}
			if strings.Join(starts, ",") != strings.Join(tc.wantStarts, ",") || strings.Join(maxes, ",") != strings.Join(tc.wantMaxRess, ",") {
				t.Errorf("startAt %v maxResults %v, want %v %v", starts, maxes, tc.wantStarts, tc.wantMaxRess)
			}
		})
	}
}

func TestSearchStopsOnEmptyPageAndFailsOnPageError(t *testing.T) {
	// A Jira that claims more issues than it returns must not loop forever.
	ft := &fakeTransport{fn: func(*Request) (*Response, error) {
		return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":100,"total":5000,"issues":[]}`)}, nil
	}}
	got, err := New("https://sd.local", "t", ft).Search(context.Background(), "x", SearchOptions{})
	if err != nil || len(got) != 0 || len(ft.reqs) != 1 {
		t.Fatalf("got %d issues, err %v, %d requests", len(got), err, len(ft.reqs))
	}

	// An error on page 2 fails the whole search (no silently partial worklog).
	page := pagingSearch(250, 100)
	ft = &fakeTransport{fn: func(r *Request) (*Response, error) {
		if queryOf(t, r).Get("startAt") == "100" {
			return &Response{StatusCode: 503, Body: []byte(`Service Unavailable`)}, nil
		}
		return page(r)
	}}
	got, err = New("https://sd.local", "t", ft).Search(context.Background(), "x", SearchOptions{})
	if !IsCode(err, CodeServer) || got != nil {
		t.Fatalf("got %d issues, err %v", len(got), err)
	}
}

func TestSearchJQLIsURLEncoded(t *testing.T) {
	jqls := []string{
		`status = "В работе" AND assignee = currentUser()`,
		`status in ("В роботі", "Предоставлено решение", "Закрыт автоматически")`,
		`summary ~ "a+b & c=d #1 50%" ORDER BY updated DESC`,
		`(assignee = currentUser() OR status changed BY currentUser() DURING (startOfDay(), now())) AND (status changed DURING (startOfDay(), now()) OR statusCategory = "In Progress")`,
	}
	for _, jql := range jqls {
		ft := &fakeTransport{fn: pagingSearch(0, 100)}
		if _, err := New("https://sd.local", "t", ft).Search(context.Background(), jql, SearchOptions{Fields: []string{"summary", "status"}, Expand: "changelog"}); err != nil {
			t.Fatal(err)
		}
		r := ft.reqs[0]
		if i := strings.IndexFunc(r.URL, func(c rune) bool { return c > 0x7e || c <= ' ' }); i >= 0 {
			t.Errorf("URL has a raw non-ASCII/space byte at %d: %s", i, r.URL)
		}
		q := queryOf(t, r)
		if q.Get("jql") != jql {
			t.Errorf("jql round trip:\n got  %q\n want %q", q.Get("jql"), jql)
		}
		if q.Get("fields") != "summary,status" || q.Get("expand") != "changelog" || len(q) != 5 {
			t.Errorf("query params %v", q)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	long := `{"errorMessages":["` + strings.Repeat("x", 700) + `"]}`
	tests := []struct {
		name     string
		resp     Response
		code     string
		kind     string
		status   int
		contains string
		detail   string
	}{
		{"401", Response{StatusCode: 401}, CodeUnauthorized, "", 401, "HTTP 401", ""},
		{"403 with message", Response{StatusCode: 403, Body: []byte(`{"message":"CAPTCHA required"}`)}, CodeForbidden, "", 403, "CAPTCHA required", "CAPTCHA required"},
		{"404", Response{StatusCode: 404, Body: []byte(`{"errorMessages":["Issue Does Not Exist"],"errors":{}}`)}, CodeNotFound, "", 404, "Issue Does Not Exist", "Issue Does Not Exist"},
		{"400 field errors", Response{StatusCode: 400, Body: []byte(`{"errorMessages":[],"errors":{"timeLogged":"You must indicate the time spent working."}}`)}, CodeBadRequest, "", 400, "timeLogged: You must indicate", "timeLogged: You must indicate the time spent working."},
		{"409", Response{StatusCode: 409}, CodeBadRequest, "", 409, "HTTP 409", ""},
		{"429", Response{StatusCode: 429}, CodeBadRequest, "", 429, "HTTP 429", ""},
		{"500 html", Response{StatusCode: 500, Body: []byte(`<html><body>Internal Server Error</body></html>`)}, CodeServer, "", 500, "HTTP 500", ""},
		{"502 empty", Response{StatusCode: 502}, CodeServer, "", 502, "HTTP 502", ""},
		{"503 json", Response{StatusCode: 503, Body: []byte(`{"errorMessages":["maintenance"]}`)}, CodeServer, "", 503, "maintenance", "maintenance"},
		{"long detail is truncated", Response{StatusCode: 400, Body: []byte(long)}, CodeBadRequest, "", 400, "xxx…", strings.Repeat("x", 500) + "…"},
		{"301 to https", Response{StatusCode: 301, Location: "https://sd.local/rest/api/2/myself"}, CodeBadResponse, KindRedirect, 301, "redirected the request to sd.local", "sd.local"},
		{"302 without Location", Response{StatusCode: 302}, CodeBadResponse, KindRedirect, 302, "redirected the request;", ""},
		{"200 html login page", Response{StatusCode: 200, Body: []byte("\n  <!DOCTYPE html><html>Log in</html>")}, CodeBadResponse, KindHTML, 200, "HTML page", ""},
		{"200 malformed json", Response{StatusCode: 200, Body: []byte(`{"name":`)}, CodeBadResponse, KindJSON, 200, "unexpected response", ""},
		{"200 wrong json type", Response{StatusCode: 200, Body: []byte(`[1,2]`)}, CodeBadResponse, KindJSON, 200, "unexpected response", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.resp
			ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &resp, nil }}
			_, err := New("https://sd.local", "s3cr3t-token", ft).Myself(context.Background())
			var je *Error
			if !errors.As(err, &je) {
				t.Fatalf("got %#v", err)
			}
			if je.Code != tc.code || je.Kind != tc.kind || je.Status != tc.status || !strings.Contains(je.Message, tc.contains) {
				t.Errorf("got code=%s kind=%s status=%d msg=%q", je.Code, je.Kind, je.Status, je.Message)
			}
			if tc.detail != "" && je.Detail != tc.detail {
				t.Errorf("detail %q, want %q", je.Detail, tc.detail)
			}
			if strings.Contains(je.Error(), "s3cr3t-token") {
				t.Error("token leaked into the error")
			}
		})
	}
}

func TestEmptySuccessBodiesAreOK(t *testing.T) {
	for _, code := range []int{200, 201, 204} {
		ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &Response{StatusCode: code, Body: []byte("  ")}, nil }}
		if err := New("https://sd.local", "t", ft).DoTransition(context.Background(), "A-1", "31"); err != nil {
			t.Errorf("%d: %v", code, err)
		}
	}
}

func TestCancelledContextIsATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft := &fakeTransport{fn: func(*Request) (*Response, error) { return nil, errors.New("operation aborted") }}
	_, err := New("https://sd.local", "t", ft).Myself(ctx)
	var je *Error
	if !errors.As(err, &je) || je.Code != CodeNetwork || je.Kind != NetTimeout {
		t.Fatalf("got %#v", err)
	}
}

func TestInvalidIssueKeysAreRejectedBeforeSending(t *testing.T) {
	ctx := context.Background()
	for _, key := range []string{"", "A-1/../../myself", "A-1?jql=x", "A-1#x", "A-1 ", "../A-1", "A-1%2F", "АБ-1", strings.Repeat("A", 62) + "-12"} {
		ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &Response{StatusCode: 200, Body: []byte(`{}`)}, nil }}
		c := New("https://sd.local", "t", ft)
		calls := map[string]error{}
		_, calls["GetIssue"] = c.GetIssue(ctx, key, nil)
		_, calls["Worklogs"] = c.Worklogs(ctx, key)
		calls["AddWorklog"] = c.AddWorklog(ctx, key, 60, time.Now())
		_, calls["Transitions"] = c.Transitions(ctx, key)
		calls["DoTransition"] = c.DoTransition(ctx, key, "1")
		for name, err := range calls {
			var je *Error
			if !errors.As(err, &je) || je.Code != CodeBadRequest || je.Kind != KindInvalidKey {
				t.Errorf("%s(%q): got %#v", name, key, err)
			}
		}
		if len(ft.reqs) != 0 {
			t.Errorf("%q: %d requests were sent", key, len(ft.reqs))
		}
	}
}

func TestWorklogsPagination(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		if queryOf(t, r).Get("startAt") == "0" {
			return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":2,"total":3,"worklogs":[{"id":"1","timeSpentSeconds":60},{"id":"2","timeSpentSeconds":120}]}`)}, nil
		}
		return &Response{StatusCode: 200, Body: []byte(`{"startAt":2,"maxResults":2,"total":3,"worklogs":[{"id":"3","timeSpentSeconds":180}]}`)}, nil
	}}
	got, err := New("https://sd.local", "t", ft).Worklogs(context.Background(), "A-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].ID != "3" || len(ft.reqs) != 2 || queryOf(t, ft.reqs[1]).Get("startAt") != "2" {
		t.Fatalf("got %+v after %d requests", got, len(ft.reqs))
	}
	if !strings.HasPrefix(ft.reqs[0].URL, "https://sd.local/rest/api/2/issue/A-1/worklog?") {
		t.Errorf("url %s", ft.reqs[0].URL)
	}

	// Old Jira without paging fields returns everything at once: one request.
	ft = &fakeTransport{fn: func(*Request) (*Response, error) {
		return &Response{StatusCode: 200, Body: []byte(`{"worklogs":[{"id":"1"},{"id":"2"}]}`)}, nil
	}}
	got, err = New("https://sd.local", "t", ft).Worklogs(context.Background(), "A-1")
	if err != nil || len(got) != 2 || len(ft.reqs) != 1 {
		t.Fatalf("old jira: %+v %v %d requests", got, err, len(ft.reqs))
	}
}

func TestTransitionRequestBody(t *testing.T) {
	ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &Response{StatusCode: 204}, nil }}
	if err := New("https://sd.local/", "tok", ft).DoTransition(context.Background(), "A-1", "31"); err != nil {
		t.Fatal(err)
	}
	r := ft.reqs[0]
	var body map[string]map[string]string
	if err := json.Unmarshal(r.Body, &body); err != nil || body["transition"]["id"] != "31" {
		t.Errorf("body %s", r.Body)
	}
	if r.Method != "POST" || r.URL != "https://sd.local/rest/api/2/issue/A-1/transitions" || r.Headers["Content-Type"] != "application/json" {
		t.Errorf("request %s %s %v", r.Method, r.URL, r.Headers)
	}
}

func TestParseTimeFormats(t *testing.T) {
	want := time.Date(2026, 11, 9, 12, 24, 46, 0, time.UTC)
	tests := []struct {
		in    string
		want  time.Time
		milli int
	}{
		{"2026-11-09T14:24:46.807+0200", want, 807},
		{"2026-11-09T14:24:46.000+0200", want, 0},
		{"2026-11-09T14:24:46+0200", want, 0},
		{"2026-11-09T14:24:46.807+02:00", want, 807},
		{"2026-11-09T12:24:46.807Z", want, 807},
		{"2026-11-09T12:24:46Z", want, 0},
		{"2026-11-09T12:24:46.807+0000", want, 807},
		{"2026-11-09T07:24:46.807-0500", want, 807},
		{"2026-11-09 14:24:46.807+0200", want, 807},
		{"  2026-11-09T14:24:46.807+0200\n", want, 807},
		// Europe/Kyiv summer time (EEST, +03:00).
		{"2026-07-01T12:00:00.000+0300", time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC), 0},
		// Both sides of the spring DST switch (2026-03-29 03:00 EET -> 04:00 EEST) are one second apart.
		{"2026-03-29T04:00:00.000+0300", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC), 0},
		{"2026-03-29T02:59:59.000+0200", time.Date(2026, 3, 29, 0, 59, 59, 0, time.UTC), 0},
	}
	for _, tc := range tests {
		got, err := ParseTime(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if !got.Truncate(time.Second).Equal(tc.want) || got.Nanosecond() != tc.milli*int(time.Millisecond) {
			t.Errorf("%q: got %v, want %v +%dms", tc.in, got.UTC(), tc.want, tc.milli)
		}
	}
	for _, bad := range []string{"", "2026-11-09", "14:24:46", "09.11.2026 14:24", "2026-13-09T14:24:46.807+0200", "null"} {
		if _, err := ParseTime(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestFormatTimeForWorklogStarted(t *testing.T) {
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("EET", 2*3600)), "2026-11-11T09:00:00.000+0200"},
		{time.Date(2026, 7, 1, 9, 0, 0, 0, time.FixedZone("EEST", 3*3600)), "2026-07-01T09:00:00.000+0300"},
		{time.Date(2026, 11, 11, 7, 0, 0, 0, time.UTC), "2026-11-11T07:00:00.000+0000"}, // never "Z": Jira rejects it
		{time.Date(2026, 11, 11, 2, 0, 0, 0, time.FixedZone("EST", -5*3600)), "2026-11-11T02:00:00.000-0500"},
		{time.Date(2026, 11, 11, 15, 0, 12, 345678901, time.FixedZone("EET", 2*3600)), "2026-11-11T15:00:12.345+0200"},
		{time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("IST", 5*3600+30*60)), "2026-11-11T09:00:00.000+0530"},
	}
	for _, tc := range tests {
		got := FormatTime(tc.t)
		if got != tc.want {
			t.Errorf("FormatTime(%v) = %s, want %s", tc.t, got, tc.want)
		}
		back, err := ParseTime(got)
		if err != nil || !back.Equal(tc.t.Truncate(time.Millisecond)) {
			t.Errorf("round trip %s: %v %v", got, back, err)
		}
	}
}

func TestUserSame(t *testing.T) {
	tests := []struct {
		name string
		a, b *User
		want bool
	}{
		{"same key", &User{Key: "JIRAUSER1", Name: "alex"}, &User{Key: "JIRAUSER1", Name: "alex.renamed"}, true},
		{"different key, same name", &User{Key: "JIRAUSER1", Name: "alex"}, &User{Key: "JIRAUSER2", Name: "alex"}, false},
		{"no key on author: name case-insensitive", &User{Key: "JIRAUSER1", Name: "alex"}, &User{Name: "ALEX"}, true},
		{"no key, other name", &User{Name: "alex"}, &User{Name: "bob"}, false},
		{"same display name only", &User{DisplayName: "Alex"}, &User{DisplayName: "Alex"}, false},
		{"empty names", &User{}, &User{}, false},
		{"nil author", &User{Name: "alex"}, nil, false},
		{"nil me", nil, &User{Name: "alex"}, false},
	}
	for _, tc := range tests {
		if got := tc.a.Same(tc.b); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

// QA-1: the error detail is cut at 500 bytes, which can split a multi-byte
// UTF-8 character (Cyrillic messages from this Jira) and put invalid UTF-8
// into the message shown to the user.
func TestLongCyrillicErrorDetailStaysValidUTF8(t *testing.T) {
	msg := "x" + strings.Repeat("Ошибка ", 120) // odd byte offset before the 2-byte runes
	ft := &fakeTransport{fn: func(*Request) (*Response, error) {
		return &Response{StatusCode: 400, Body: []byte(`{"errorMessages":["` + msg + `"]}`)}, nil
	}}
	_, err := New("https://sd.local", "t", ft).Myself(context.Background())
	var je *Error
	if !errors.As(err, &je) {
		t.Fatalf("got %#v", err)
	}
	if !utf8.ValidString(je.Detail) || !utf8.ValidString(je.Message) {
		t.Errorf("invalid UTF-8 in detail: %q", je.Detail[len(je.Detail)-8:])
	}
}

func TestIssueRequestShapes(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		call   func(c *Client) error
		path   string
		query  url.Values
		method string
	}{
		{"GetIssue with fields", func(c *Client) error { _, err := c.GetIssue(ctx, "A-1", []string{"summary", "status"}); return err },
			"/rest/api/2/issue/A-1", url.Values{"fields": {"summary,status"}}, "GET"},
		{"GetIssue without fields", func(c *Client) error { _, err := c.GetIssue(ctx, "A-1", nil); return err },
			"/rest/api/2/issue/A-1", url.Values{}, "GET"},
		{"IssueWithChangelog", func(c *Client) error { _, err := c.IssueWithChangelog(ctx, "A-1"); return err },
			"/rest/api/2/issue/A-1", url.Values{"fields": {"status,created"}, "expand": {"changelog"}}, "GET"},
		{"Transitions", func(c *Client) error { _, err := c.Transitions(ctx, "A-1"); return err },
			"/rest/api/2/issue/A-1/transitions", url.Values{}, "GET"},
		{"Worklogs", func(c *Client) error { _, err := c.Worklogs(ctx, "A-1"); return err },
			"/rest/api/2/issue/A-1/worklog", url.Values{"startAt": {"0"}, "maxResults": {"1000"}}, "GET"},
		{"Search with embedded worklogs", func(c *Client) error {
			_, err := c.Search(ctx, "worklogAuthor = currentUser()", SearchOptions{Fields: []string{"worklog"}, Limit: 500})
			return err
		}, "/rest/api/2/search", url.Values{"jql": {"worklogAuthor = currentUser()"}, "fields": {"worklog"}, "startAt": {"0"}, "maxResults": {"100"}}, "GET"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &Response{StatusCode: 200, Body: []byte(`{}`)}, nil }}
			if err := tc.call(New("https://sd.local/", "tok", ft)); err != nil {
				t.Fatal(err)
			}
			if len(ft.reqs) != 1 {
				t.Fatalf("%d requests", len(ft.reqs))
			}
			r := ft.reqs[0]
			u, _ := url.Parse(r.URL)
			if r.Method != tc.method || u.Scheme+"://"+u.Host != "https://sd.local" || u.Path != tc.path || !reflect.DeepEqual(u.Query(), tc.query) {
				t.Errorf("got %s %s, want %s %s?%s", r.Method, r.URL, tc.method, tc.path, tc.query.Encode())
			}
			if len(tc.query) == 0 && strings.Contains(r.URL, "?") {
				t.Errorf("empty query must not add '?': %s", r.URL)
			}
			if r.Headers["Authorization"] != "Bearer tok" || r.Headers["Accept"] != "application/json" || r.Body != nil {
				t.Errorf("headers %v body %q", r.Headers, r.Body)
			}
		})
	}
}

func TestDecodesTransitionsChangelogAndEmbeddedWorklogs(t *testing.T) {
	ctx := context.Background()
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		switch {
		case strings.Contains(r.URL, "/transitions"):
			return &Response{StatusCode: 200, Body: []byte(`{"transitions":[{"id":"31","name":"To review","to":{"id":"6","name":"ForReview","statusCategory":{"key":"indeterminate"}}}]}`)}, nil
		case strings.Contains(r.URL, "/search"):
			return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":100,"total":1,"issues":[{"key":"X-9","fields":{"worklog":{"startAt":0,"maxResults":20,"total":1,
				"worklogs":[{"id":"7","author":{"name":"alex","key":"alex"},"started":"2026-11-11T09:00:00.000+0200","timeSpentSeconds":3600}]}}}]}`)}, nil
		default:
			return &Response{StatusCode: 200, Body: []byte(`{"key":"A-1","fields":{"status":{"name":"In Progress"},"created":"2026-11-01T10:00:00.000+0200"},
				"changelog":{"startAt":0,"maxResults":1,"total":1,"histories":[{"id":"1","created":"2026-11-11T10:00:00.000+0200",
				"items":[{"field":"status","from":"1","fromString":"ToDo","to":"3","toString":"In Progress"}]}]}}`)}, nil
		}
	}}
	c := New("https://sd.local", "t", ft)
	tr, err := c.Transitions(ctx, "A-1")
	if err != nil || len(tr) != 1 || tr[0].ID != "31" || tr[0].To.Name != "ForReview" || tr[0].To.StatusCategory.Key != "indeterminate" {
		t.Errorf("transitions %+v %v", tr, err)
	}
	is, err := c.IssueWithChangelog(ctx, "A-1")
	if err != nil || is.Changelog == nil || is.Changelog.Total != 1 || is.Changelog.Histories[0].Items[0].ToString != "In Progress" || is.Fields.Created == "" {
		t.Errorf("issue %+v %v", is, err)
	}
	found, err := c.Search(ctx, "x", SearchOptions{Fields: []string{"worklog"}})
	if err != nil || len(found) != 1 || found[0].Fields.Worklog == nil || !found[0].Fields.Worklog.Complete() ||
		found[0].Fields.Worklog.Worklogs[0].TimeSpentSeconds != 3600 || found[0].Fields.Worklog.Worklogs[0].Author.Key != "alex" {
		t.Errorf("embedded worklogs %+v %v", found, err)
	}
}

func TestWorklogPageComplete(t *testing.T) {
	ws := func(n int) []Worklog { return make([]Worklog, n) }
	tests := []struct {
		name string
		p    WorklogPage
		want bool
	}{
		{"all embedded", WorklogPage{StartAt: 0, MaxResults: 20, Total: 3, Worklogs: ws(3)}, true},
		{"none", WorklogPage{StartAt: 0, MaxResults: 20, Total: 0}, true},
		{"exactly the page size", WorklogPage{StartAt: 0, MaxResults: 20, Total: 20, Worklogs: ws(20)}, true},
		{"more than the page", WorklogPage{StartAt: 0, MaxResults: 20, Total: 21, Worklogs: ws(20)}, false},
		{"fewer returned than total", WorklogPage{StartAt: 0, MaxResults: 20, Total: 5, Worklogs: ws(4)}, false},
		{"not the first page", WorklogPage{StartAt: 20, MaxResults: 20, Total: 5, Worklogs: ws(5)}, false},
		{"missing paging fields", WorklogPage{Total: 2, Worklogs: ws(2)}, false},
	}
	for _, tc := range tests {
		if got := tc.p.Complete(); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
