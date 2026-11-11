package jira

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeTransport struct {
	reqs []*Request
	fn   func(r *Request) (*Response, error)
}

func (f *fakeTransport) Do(_ context.Context, r *Request) (*Response, error) {
	f.reqs = append(f.reqs, r)
	return f.fn(r)
}

func TestParseTime(t *testing.T) {
	for _, s := range []string{
		"2026-11-09T14:24:46.807+0200",
		"2026-11-09T14:24:46+0200",
		"2026-11-09T14:24:46.807+02:00",
		"2026-11-09T12:24:46.807Z",
	} {
		got, err := ParseTime(s)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if got.UTC().Hour() != 12 || got.Minute() != 24 {
			t.Errorf("%s: got %v", s, got.UTC())
		}
	}
	if _, err := ParseTime("yesterday"); err == nil {
		t.Error("expected error for garbage")
	}
	ts := time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("", 2*3600))
	if got := FormatTime(ts); got != "2026-11-11T09:00:00.000+0200" {
		t.Errorf("FormatTime = %s", got)
	}
}

func TestErrorsAreReadable(t *testing.T) {
	tests := []struct {
		status int
		body   string
		code   string
		substr string
	}{
		{401, ``, CodeUnauthorized, "token"},
		{400, `{"errorMessages":["Field 'x' does not exist"],"errors":{}}`, CodeBadRequest, "Field 'x' does not exist"},
		{500, `<html>oops</html>`, CodeServer, "HTTP 500"},
	}
	for _, tc := range tests {
		ft := &fakeTransport{fn: func(*Request) (*Response, error) {
			return &Response{StatusCode: tc.status, Body: []byte(tc.body)}, nil
		}}
		_, err := New("https://sd.local/", "secret", ft).Myself(context.Background())
		var je *Error
		if !errors.As(err, &je) || je.Code != tc.code || !strings.Contains(je.Message, tc.substr) {
			t.Errorf("status %d: got %#v", tc.status, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Error("token leaked into error message")
		}
	}

	ft := &fakeTransport{fn: func(*Request) (*Response, error) { return nil, errors.New("server name could not be resolved") }}
	_, err := New("https://sd.local", "t", ft).Myself(context.Background())
	if !IsCode(err, CodeNetwork) {
		t.Errorf("transport error: got %#v", err)
	}

	ft = &fakeTransport{fn: func(*Request) (*Response, error) {
		return &Response{StatusCode: 200, Body: []byte("<!DOCTYPE html><html>login</html>")}, nil
	}}
	_, err = New("https://sd.local", "t", ft).Myself(context.Background())
	if !IsCode(err, CodeBadResponse) {
		t.Errorf("html body: got %#v", err)
	}
}

func TestSearchPaginatesAndAuthenticates(t *testing.T) {
	ft := &fakeTransport{fn: func(r *Request) (*Response, error) {
		if strings.Contains(r.URL, "startAt=0") {
			return &Response{StatusCode: 200, Body: []byte(`{"startAt":0,"maxResults":2,"total":3,"issues":[{"key":"A-1"},{"key":"A-2"}]}`)}, nil
		}
		return &Response{StatusCode: 200, Body: []byte(`{"startAt":2,"maxResults":2,"total":3,"issues":[{"key":"A-3"}]}`)}, nil
	}}
	c := New("https://sd.local/", "tok", ft)
	got, err := c.Search(context.Background(), `assignee = currentUser() AND status = "In Progress"`, SearchOptions{Fields: []string{"summary", "status"}, Expand: "changelog"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2].Key != "A-3" {
		t.Fatalf("got %+v", got)
	}
	r := ft.reqs[0]
	if r.Headers["Authorization"] != "Bearer tok" || r.Method != "GET" {
		t.Errorf("request %+v", r)
	}
	if !strings.HasPrefix(r.URL, "https://sd.local/rest/api/2/search?") || !strings.Contains(r.URL, "expand=changelog") || !strings.Contains(r.URL, "fields=summary%2Cstatus") {
		t.Errorf("url %s", r.URL)
	}
}

func TestAddWorklogBody(t *testing.T) {
	ft := &fakeTransport{fn: func(*Request) (*Response, error) { return &Response{StatusCode: 201, Body: []byte(`{}`)}, nil }}
	started := time.Date(2026, 11, 11, 9, 0, 0, 0, time.FixedZone("", 2*3600))
	if err := New("https://sd.local", "t", ft).AddWorklog(context.Background(), "PROJ-7", 7200, started); err != nil {
		t.Fatal(err)
	}
	r := ft.reqs[0]
	if r.Method != "POST" || r.URL != "https://sd.local/rest/api/2/issue/PROJ-7/worklog" {
		t.Errorf("request %s %s", r.Method, r.URL)
	}
	body := string(r.Body)
	if !strings.Contains(body, `"timeSpentSeconds":7200`) || !strings.Contains(body, `"started":"2026-11-11T09:00:00.000+0200"`) {
		t.Errorf("body %s", body)
	}
}

func TestValidIssueKey(t *testing.T) {
	for _, k := range []string{"PROJ-1", "AB_C2-12345", "sd-7"} {
		if !ValidIssueKey(k) {
			t.Errorf("%s should be valid", k)
		}
	}
	for _, k := range []string{"", "PROJ", "PROJ-", "-1", "PROJ-1/../x", "PROJ-1a", "A B-1"} {
		if ValidIssueKey(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}

func TestRedirectIsReportedNotFollowed(t *testing.T) {
	ft := &fakeTransport{fn: func(*Request) (*Response, error) {
		return &Response{StatusCode: 302, Location: "https://sso.example/login?x=1"}, nil
	}}
	_, err := New("https://sd.local", "t", ft).Myself(context.Background())
	var je *Error
	if !errors.As(err, &je) || je.Code != CodeBadResponse || je.Kind != KindRedirect || je.Detail != "sso.example" {
		t.Fatalf("got %#v", err)
	}
	if len(ft.reqs) != 1 {
		t.Errorf("requests %d", len(ft.reqs))
	}
}

func TestTransportErrorKindIsKept(t *testing.T) {
	ft := &fakeTransport{fn: func(*Request) (*Response, error) {
		return nil, &TransportError{Kind: NetTLSUntrust, Message: "untrusted CA (WinHTTP 12045)"}
	}}
	_, err := New("https://sd.local", "t", ft).Myself(context.Background())
	var je *Error
	if !errors.As(err, &je) || je.Code != CodeNetwork || je.Kind != NetTLSUntrust {
		t.Fatalf("got %#v", err)
	}
}
