package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

func uiPrefsJSON(t *testing.T, a *App) string {
	t.Helper()
	res, err := a.Call(context.Background(), "getUiPrefs", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUIPrefsRoundtripAndPersistence(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)

	fj := &fakeJira{}
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, zone)}
	a := openApp(t, dir, fj, clock, nil)
	ctx := context.Background()
	if got := uiPrefsJSON(t, a); got != "{}" {
		t.Errorf("empty prefs %s", got)
	}
	res, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":{ "onlyMine": true, "board": {"id": 880, "secret": "s3cr3t-pref"} }}`))
	if err != nil || res != nil {
		t.Fatalf("set: %v %v", res, err)
	}
	want := `{"onlyMine":true,"board":{"id":880,"secret":"s3cr3t-pref"}}`
	if got := uiPrefsJSON(t, a); got != want {
		t.Errorf("got %s", got)
	}
	// Replaces the whole object.
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":{"lang":"uk"}}`)); err != nil {
		t.Fatal(err)
	}
	if got := uiPrefsJSON(t, a); got != `{"lang":"uk"}` {
		t.Errorf("after replace %s", got)
	}
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":{"x":"s3cr3t-pref"}}`)); err != nil {
		t.Fatal(err)
	}
	// Independent of the Jira connection: a new origin keeps them.
	if _, err := a.Call(ctx, "saveSettings", json.RawMessage(`{"jiraUrl":"https://jira.other.local","token":"new"}`)); err != nil {
		t.Fatal(err)
	}
	a.Stop(time.Second)
	// Persisted in state.json: a restarted app reads them back.
	b := openApp(t, dir, fj, clock, nil)
	if got := uiPrefsJSON(t, b); got != `{"x":"s3cr3t-pref"}` {
		t.Errorf("after reload %s", got)
	}
	if strings.Contains(logs.String(), "s3cr3t-pref") {
		t.Errorf("prefs logged: %s", logs.String())
	}
}

func TestUIPrefsValidation(t *testing.T) {
	a, _, _ := newTestApp(t, &fakeJira{}, time.Date(2026, 11, 11, 10, 0, 0, 0, zone))
	ctx := context.Background()
	if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":{"keep":1}}`)); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{`{}`, `{"prefs":null}`, `{"prefs":[1,2]}`, `{"prefs":"{}"}`, `{"prefs":42}`, `{"prefs":true}`, `{"prefs":{"a":}}`} {
		_, err := a.Call(ctx, "setUiPrefs", json.RawMessage(p))
		if e := ToError(err); e.Code != CodeBadParams || e.Message == "" {
			t.Errorf("%s: %+v", p, e)
		}
	}
	_, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":[]}`))
	if e := ToError(err); e.Message != "Настройки интерфейса должны быть объектом JSON" {
		t.Errorf("localized message %q", e.Message)
	}

	// Size limit on the compact form: 32 KB fits, one byte more does not;
	// whitespace does not count.
	pad := MaxUIPrefsBytes - len(`{"v":""}`)
	exact := fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("a", pad))
	spaced := fmt.Sprintf("{ \"v\" :\n\t\"%s\" }", strings.Repeat("a", pad))
	for _, ok := range []string{exact, spaced} {
		if _, err := a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":`+ok+`}`)); err != nil {
			t.Errorf("%d bytes rejected: %v", len(ok), err)
		}
	}
	over := fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("a", pad+1))
	_, err = a.Call(ctx, "setUiPrefs", json.RawMessage(`{"prefs":`+over+`}`))
	if e := ToError(err); e.Code != CodeBadParams || !strings.Contains(e.Message, "32") {
		t.Errorf("over limit: %+v", e)
	}
	// Rejected calls leave the stored object unchanged.
	if got := uiPrefsJSON(t, a); got != exact {
		t.Errorf("stored prefs changed by a rejected call (%d bytes)", len(got))
	}
}
