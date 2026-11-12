package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

var zone = time.FixedZone("EET", 2*3600)

// testToken is the token newTestApp stores; distinctive so leak checks are meaningful.
const testToken = "tok-7f3a9c0e"

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time { return c.t }

type plainProtector struct{}

func (plainProtector) Protect(b []byte) ([]byte, error)   { return append([]byte("x"), b...), nil }
func (plainProtector) Unprotect(b []byte) ([]byte, error) { return b[1:], nil }

type notice struct{ title, text, url string }

type fakePlatform struct {
	t            jira.Transport
	mu           sync.Mutex
	notices      []notice
	opened       []string
	autostartErr error
	themes       []WindowTheme
}

func (p *fakePlatform) Transport() jira.Transport { return p.t }
func (p *fakePlatform) OpenURL(u string) error    { p.opened = append(p.opened, u); return nil }
func (p *fakePlatform) SetAutostart(bool) error   { return p.autostartErr }
func (p *fakePlatform) Notify(title, text, u string) {
	p.mu.Lock()
	p.notices = append(p.notices, notice{title, text, u})
	p.mu.Unlock()
}
func (p *fakePlatform) SettingsChanged(config.Settings, bool) {}
func (p *fakePlatform) SetWindowTheme(t WindowTheme) {
	p.mu.Lock()
	p.themes = append(p.themes, t)
	p.mu.Unlock()
}

const worklogIssues = `[
 {"key":"A-1","fields":{"summary":"All day","status":{"id":"3","name":"In Progress","statusCategory":{"key":"indeterminate"}},
   "created":"2026-11-10T10:00:00.000+0200"},
  "changelog":{"startAt":0,"maxResults":2,"total":2,"histories":[
   {"created":"2026-11-10T11:00:00.000+0200","items":[{"field":"status","fromString":"ToDo","toString":"In Progress"}]},
   {"created":"2026-11-11T12:00:00.000+0200","items":[{"field":"resolution","fromString":null,"toString":"Done"}]}]}},
 {"key":"B-2","fields":{"summary":"Afternoon","status":{"id":"5","name":"ForReview","statusCategory":{"key":"indeterminate"}},
   "created":"2026-11-01T10:00:00.000+0200"},
  "changelog":{"startAt":0,"maxResults":2,"total":2,"histories":[
   {"created":"2026-11-11T15:00:00.000+0200","items":[{"field":"status","fromString":"ToDo","toString":"В работе"}]},
   {"created":"2026-11-11T17:00:00.000+0200","items":[{"field":"assignee","fromString":"x","toString":"y"},{"field":"status","fromString":"В работе","toString":"ForReview"}]}]}}
]`

func newTestApp(t *testing.T, fj *fakeJira, now time.Time) (*App, *fakePlatform, *fakeClock) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Open(filepath.Join(dir, "config.json"), plainProtector{})
	if err != nil {
		t.Fatal(err)
	}
	s := config.Defaults()
	s.JiraURL = "https://sd.local"
	tok := testToken
	if _, _, err := cfg.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	plat := &fakePlatform{t: fj}
	clock := &fakeClock{t: now}
	attachClock(fj, clock)
	return New(cfg, st, plat, clock, nil), plat, clock
}

func TestWorklogRunIsIdempotent(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	ctx := context.Background()

	prev, err := a.Call(ctx, "worklogPreview", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := prev.(worklog.Run); r.TotalSeconds != 8*3600 || len(fj.posted) != 0 {
		t.Fatalf("preview: total=%d posted=%v", r.TotalSeconds, fj.posted)
	}

	res, err := a.Call(ctx, "worklogRun", json.RawMessage(`"{}"`))
	if err != nil {
		t.Fatal(err)
	}
	run := res.(worklog.Run)
	want := []string{"A-1 25200 2026-11-11T09:00:00.000+0200", "B-2 3600 2026-11-11T15:00:00.000+0200"}
	if strings.Join(fj.posted, "|") != strings.Join(want, "|") {
		t.Fatalf("posted %v, want %v", fj.posted, want)
	}
	if run.Total != "8h" || run.Items[0].Result != worklog.ResultLogged || run.Items[0].Duration != "7h" {
		t.Errorf("run %+v", run)
	}
	if fj.lastToken != "Bearer "+testToken {
		t.Errorf("auth header %q", fj.lastToken)
	}

	res, err = a.Call(ctx, "worklogRun", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fj.posted) != 2 || res.(worklog.Run).TotalSeconds != 0 {
		t.Fatalf("second run logged again: %v", fj.posted)
	}
	hist, _ := a.Call(ctx, "getWorklogHistory", nil)
	if h := hist.([]worklog.Run); len(h) != 2 || h[0].TotalSeconds != 0 || h[1].TotalSeconds != 8*3600 {
		t.Errorf("history %+v", h)
	}
}

func TestWorklogOnWeekendReportsReason(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 14, 18, 0, 0, 0, zone)) // Saturday
	res, err := a.Call(context.Background(), "worklogRun", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(worklog.Run); r.SkippedReason != worklog.ReasonWeekend || len(fj.posted) != 0 {
		t.Fatalf("got %+v posted=%v", r, fj.posted)
	}
}

func TestScheduledWorklogMarksDay(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	a, plat, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 18, 0, 0, 0, zone))
	if err := a.ScheduledWorklog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := a.st.Snapshot().LastWorklogDate; d != "2026-11-11" {
		t.Errorf("LastWorklogDate=%q", d)
	}
	if len(plat.notices) != 1 || !strings.Contains(plat.notices[0].text, "8h") {
		t.Errorf("notices %+v", plat.notices)
	}
	st := a.State()
	if st.NextWorklogAt != "2026-11-12T18:00:00+02:00" || st.LastWorklog == nil {
		t.Errorf("state %+v", st)
	}
}

func TestCommentPollFirstRunThenNotifies(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}, comments: `[
	 {"key":"A-1","fields":{"summary":"S","comment":{"total":1,"comments":[
	   {"id":"10","author":{"name":"bob","key":"bob","displayName":"Bob"},"body":"old","created":"2026-11-11T09:00:00.000+0200"}]}}}]`}
	a, plat, clock := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	if n := a.st.Snapshot().Notifications; len(n) != 0 || len(plat.notices) != 0 {
		t.Fatalf("first run must not notify: %+v", n)
	}
	clock.t = clock.t.Add(5 * time.Minute)
	fj.comments = `[
	 {"key":"A-1","fields":{"summary":"S","comment":{"total":3,"comments":[
	   {"id":"10","author":{"name":"bob","key":"bob","displayName":"Bob"},"body":"old","created":"2026-11-11T09:00:00.000+0200"},
	   {"id":"11","author":{"name":"alex","key":"alex","displayName":"Alex"},"body":"mine","created":"2026-11-11T10:02:00.000+0200"},
	   {"id":"12","author":{"name":"bob","key":"bob","displayName":"Bob"},"body":"hi  [~alex]\nplease check","created":"2026-11-11T10:03:00.000+0200"}]}}},
	 {"key":"C-3","fields":{"summary":"Other","comment":{"total":1,"comments":[
	   {"id":"5","author":{"name":"eve","key":"eve","displayName":"Eve"},"body":"ancient","created":"2026-01-01T09:00:00.000+0200"}]}}}]`
	if err := a.PollComments(ctx); err != nil {
		t.Fatal(err)
	}
	n := a.st.Snapshot().Notifications
	if len(n) != 1 || n[0].ID != "12" || n[0].Body != "hi @alex please check" || n[0].Read {
		t.Fatalf("notifications %+v", n)
	}
	if n[0].URL != "https://sd.local/browse/A-1?focusedCommentId=12#comment-12" {
		t.Errorf("url %s", n[0].URL)
	}
	if len(plat.notices) != 1 || plat.notices[0].url != n[0].URL {
		t.Errorf("balloon %+v", plat.notices)
	}
	res, err := a.Call(ctx, "markNotificationsRead", json.RawMessage(`{}`))
	if err != nil || res.(map[string]int)["unread"] != 0 {
		t.Fatalf("mark read: %v %v", res, err)
	}
}

func TestSettingsHostChangeRequiresToken(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	_, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"https://evil.example","language":"en"}`))
	var ae *Error
	if !errors.As(err, &ae) || ae.Code != CodeValidation || len(ae.Fields) != 1 || ae.Fields[0].Field != "token" {
		t.Fatalf("got %#v", err)
	}
	// testConnection to another host never uses the stored token
	res, err := a.Call(ctx, "testConnection", json.RawMessage(`{"jiraUrl":"https://evil.example"}`))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), `"ok":true`) || fj.lastToken != "" {
		t.Fatalf("stored token used for another host: %s (auth %q)", b, fj.lastToken)
	}
	// same host, partial update keeps everything else
	res, err = a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"https://SD.local/","worklogTime":"18:30"}`))
	if err != nil {
		t.Fatal(err)
	}
	sv := res.(SaveResult)
	if !sv.HasToken || sv.WorklogTime != "18:30" || sv.WorkdayStart != "09:00" {
		t.Errorf("settings %+v", sv)
	}
	b, _ := json.Marshal(sv)
	if strings.Contains(string(b), testToken) {
		t.Errorf("token leaked: %s", b)
	}
	if strings.Contains(string(b), `"token"`) || strings.Contains(string(b), "tokenEnc") {
		t.Errorf("token field present in Settings: %s", b)
	}
}

func TestOpenURLRestricted(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a, plat, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	for _, u := range []string{"https://evil.example/browse/A-1", "file:///c:/windows", "javascript:alert(1)", "https://sd.local.evil.example/x"} {
		if _, err := a.Call(ctx, "openUrl", json.RawMessage(fmt.Sprintf(`{"url":%q}`, u))); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	if _, err := a.Call(ctx, "openUrl", json.RawMessage(`{"url":"https://sd.local/browse/A-1"}`)); err != nil {
		t.Errorf("jira url refused: %v", err)
	}
	if len(plat.opened) != 1 {
		t.Errorf("opened %v", plat.opened)
	}
}

func TestBuildColumnsOrder(t *testing.T) {
	is := func(key, id, name, cat string) IssueDTO {
		return IssueDTO{Key: key, Status: StatusDTO{ID: id, Name: name, Category: cat}}
	}
	cols := BuildColumns([]IssueDTO{
		is("A-1", "3", "In Progress", "indeterminate"),
		is("A-2", "1", "ToDo", "new"),
		is("A-3", "6", "ForReview", "indeterminate"),
		is("A-4", "3", "In Progress", "indeterminate"),
		is("A-5", "0", "Backlog", "new"),
	})
	var got []string
	for _, c := range cols {
		got = append(got, fmt.Sprintf("%s:%d", c.Status.Name, len(c.Issues)))
	}
	if strings.Join(got, ",") != "ToDo:1,Backlog:1,In Progress:2,ForReview:1" {
		t.Errorf("columns %v", got)
	}
}

func TestUnknownMethodAndBadParams(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{worklogs: map[string][]jira.Worklog{}}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	if _, err := a.Call(context.Background(), "nope", nil); ToError(err).Code != CodeUnknownMethod {
		t.Errorf("got %v", err)
	}
	if _, err := a.Call(context.Background(), "getTransitions", json.RawMessage(`{"key":"../../x"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("got %v", err)
	}
}

func TestTransitionSucceedsEvenIfReloadFails(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}, transErr: errors.New("connection reset")}
	var events []string
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	a.emit = func(typ string, _ any) { events = append(events, typ) }
	res, err := a.Call(context.Background(), "transition", json.RawMessage(`{"key":"A-1","transitionId":"31"}`))
	if err != nil {
		t.Fatalf("transition reported failure although Jira accepted it: %v", err)
	}
	is := res.(*IssueDTO)
	if is.Key != "A-1" || is.Status.Name != "ForReview" || is.Status.Category != "indeterminate" {
		t.Errorf("fallback issue %+v", is)
	}
	if len(fj.transitions) != 1 || !strings.Contains(strings.Join(events, ","), "board") {
		t.Errorf("transitions=%v events=%v", fj.transitions, events)
	}

	// normal path returns the fresh issue; unknown transition ids are refused
	fj.transErr = nil
	res, err = a.Call(context.Background(), "transition", json.RawMessage(`{"key":"A-1","transitionId":"31"}`))
	if err != nil || res.(*IssueDTO).Summary != "Fresh" {
		t.Errorf("got %+v %v", res, err)
	}
	if _, err := a.Call(context.Background(), "transition", json.RawMessage(`{"key":"A-1","transitionId":"99"}`)); ToError(err).Code != CodeBadParams {
		t.Errorf("unknown transition: %v", err)
	}
	if len(fj.transitions) != 2 {
		t.Errorf("unknown transition was sent: %v", fj.transitions)
	}
}

func TestBoardFetchedDuringTransitionIsNotCached(t *testing.T) {
	fj := &fakeJira{issues: worklogIssues, worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fj.searchHook = func() { once.Do(func() { close(started); <-release }) }

	done := make(chan error)
	go func() { _, err := a.Call(ctx, "getBoard", nil); done <- err }()
	<-started // the board search is in flight with the old state
	if _, err := a.Call(ctx, "transition", json.RawMessage(`{"key":"A-1","transitionId":"31"}`)); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := fj.searches
	if _, err := a.Call(ctx, "getBoard", nil); err != nil {
		t.Fatal(err)
	}
	if fj.searches != before+1 {
		t.Fatal("stale board fetched before the transition was cached")
	}
	if _, err := a.Call(ctx, "getBoard", nil); err != nil {
		t.Fatal(err)
	}
	if fj.searches != before+1 {
		t.Error("fresh board was not cached")
	}
}

func TestSchemeChangeRequiresTokenAndWarnsOnHTTP(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a, plat, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	_, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"http://sd.local"}`))
	if e := ToError(err); e.Code != CodeValidation || len(e.Fields) != 1 || e.Fields[0].Field != "token" {
		t.Fatalf("https→http without token: %#v", err)
	}
	if res, _ := a.Call(ctx, "testConnection", json.RawMessage(`{"jiraUrl":"http://sd.local"}`)); fj.lastToken != "" {
		t.Fatalf("stored token sent over http: %+v", res)
	}
	plat.autostartErr = errors.New("registry denied")
	res, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"http://sd.local","token":"new","language":"en","autostart":false}`))
	if err != nil {
		t.Fatal(err)
	}
	w := res.(SaveResult).Warnings
	if len(w) != 2 || !strings.Contains(w[0], "http") || !strings.Contains(w[1], "autostart") {
		t.Errorf("warnings %q", w)
	}
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"warnings":[`) || !strings.Contains(string(b), `"jiraUrl":"http://sd.local"`) {
		t.Errorf("json %s", b)
	}
}

func TestErrorsAreLocalized(t *testing.T) {
	fj := &fakeJira{worklogs: map[string][]jira.Worklog{}}
	a, _, _ := newTestApp(t, fj, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"language":"ru"}`)); err != nil {
		t.Fatal(err)
	}
	fj.fail = func(*jira.Request) error {
		return &jira.TransportError{Kind: jira.NetDNS, Message: "name not resolved (WinHTTP 12007)"}
	}
	_, err := a.Call(ctx, "getBoard", json.RawMessage(`{"force":true}`))
	if e := a.ErrorFor(err); e.Code != jira.CodeNetwork || !strings.Contains(e.Message, "Не удалось найти сервер Jira") {
		t.Errorf("dns: %+v", e)
	}
	fj.fail = nil
	fj.status = func(*jira.Request) int { return 401 }
	_, err = a.Call(ctx, "getBoard", json.RawMessage(`{"force":true}`))
	if e := a.ErrorFor(err); e.Code != jira.CodeUnauthorized || !strings.Contains(e.Message, "Jira отклонила токен") || !strings.Contains(e.Message, "from jira") {
		t.Errorf("401: %+v", e)
	}
	res, _ := a.Call(ctx, "testConnection", json.RawMessage(`{"jiraUrl":"https://sd.local"}`))
	if b, _ := json.Marshal(res); !strings.Contains(string(b), "Jira отклонила токен") {
		t.Errorf("testConnection: %s", b)
	}
	fj.status = nil
	if _, err := a.Call(ctx, "openUrl", json.RawMessage(`{"url":"https://evil.example"}`)); !strings.Contains(a.ErrorFor(err).Message, "Можно открывать") {
		t.Errorf("openUrl: %v", err)
	}
	// English fallback and localized redirect
	e := Localize(&jira.Error{Code: jira.CodeBadResponse, Kind: jira.KindRedirect, Detail: "sso.example"}, "uk")
	if !strings.Contains(e.Message, "перенаправила запит на sso.example") {
		t.Errorf("redirect: %+v", e)
	}
	if e := Localize(&jira.Error{Code: jira.CodeNetwork, Kind: "weird", Message: "WinHTTP 12345"}, "xx"); e.Message != "Network error: WinHTTP 12345" {
		t.Errorf("fallback: %q", e.Message)
	}
}

func TestTextsCompleteInAllLanguages(t *testing.T) {
	for key := range texts["en"] {
		for _, lang := range []string{"ru", "uk"} {
			if _, ok := texts[lang][key]; !ok {
				t.Errorf("%s: missing %q", lang, key)
			}
		}
	}
}

func TestCheckNonce(t *testing.T) {
	const want = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		got  string
		want bool
	}{
		{`"` + want + `"`, true},
		{`"0123456789abcdef0123456789abcdee"`, false},
		{`""`, false},
		{``, false},
		{`null`, false},
		{`123`, false},
		{`"` + want + `x"`, false},
	}
	for _, tc := range tests {
		if got := CheckNonce(want, json.RawMessage(tc.got)); got != tc.want {
			t.Errorf("CheckNonce(%s) = %v", tc.got, got)
		}
	}
	if CheckNonce("", json.RawMessage(`""`)) {
		t.Error("an empty expected nonce must never match")
	}
	if e := BadNonceError("ru"); e.Code != CodeBadNonce || strings.Contains(e.Message, want) {
		t.Errorf("%+v", e)
	}
}
