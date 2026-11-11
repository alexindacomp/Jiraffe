package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// failingProtector simulates DPAPI failures (e.g. config copied from another
// Windows user).
type failingProtector struct{ protectErr, unprotectErr error }

func (f failingProtector) Protect(p []byte) ([]byte, error) {
	if f.protectErr != nil {
		return nil, f.protectErr
	}
	return xorProtector{}.Protect(p)
}

func (f failingProtector) Unprotect(b []byte) ([]byte, error) {
	if f.unprotectErr != nil {
		return nil, f.unprotectErr
	}
	return xorProtector{}.Unprotect(b)
}

func TestOpenMissingFileGivesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Jiraffe", "config.json")
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Get(); !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("got %+v\nwant %+v", got, Defaults())
	}
	if st.Configured() || st.HasToken() {
		t.Error("a fresh install must not be configured")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("opening must not create the file")
	}
}

func TestOpenCorruptFileUsesDefaultsAndKeepsTheOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	corrupt := []byte(`{"jiraUrl":"https://sd.local","worklogTime":"19:00","tokenEnc":"AAAA"`) // truncated write
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, xorProtector{})
	if err == nil {
		t.Fatal("expected an error describing the corruption")
	}
	if got := st.Get(); !reflect.DeepEqual(got, Defaults()) {
		t.Errorf("not defaults: %+v", got)
	}
	if st.Token() != "" || st.Configured() {
		t.Error("token from a corrupt file must not be used")
	}
	if b, _ := os.ReadFile(path + ".bad"); !bytes.Equal(b, corrupt) {
		t.Errorf(".bad = %q", b)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("corrupt file left in place")
	}
	// The store keeps working: saving writes a fresh valid file.
	s := Defaults()
	s.JiraURL = "https://sd.local"
	tok := "t"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	if st2, err := Open(path, xorProtector{}); err != nil || !st2.Configured() {
		t.Fatalf("reopen after save: %v", err)
	}
}

func TestTokenNeverStoredInPlainText(t *testing.T) {
	tokens := []string{
		"NjE2NDQ3ODk0MDM5OkqZ8s0B+9fM1lJ7x2Qv/3hW", // realistic Jira PAT (base64-looking)
		`p"a\ss<w>o&rd`,          // characters JSON escapes
		"токен-с-кириллицей",     // non-ASCII
		strings.Repeat("A", 300), // long
	}
	for _, tok := range tokens {
		path := filepath.Join(t.TempDir(), "config.json")
		st, _ := Open(path, xorProtector{})
		s := Defaults()
		s.JiraURL = " https://sd.local/ "
		tok := tok
		saved, errs, err := st.Save(s, &tok)
		if err != nil || errs != nil {
			t.Fatalf("save: %v %v", err, errs)
		}
		if saved.JiraURL != "https://sd.local" {
			t.Errorf("URL not normalized on save: %q", saved.JiraURL)
		}
		raw, _ := os.ReadFile(path)
		jsonTok, _ := json.Marshal(tok)
		for _, form := range []string{tok, string(jsonTok[1 : len(jsonTok)-1]), base64.StdEncoding.EncodeToString([]byte(tok))} {
			if bytes.Contains(raw, []byte(form)) {
				t.Errorf("token %.12q… found in config.json", tok)
			}
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		if _, ok := keys["token"]; ok {
			t.Error(`config.json has a "token" key`)
		}
		if _, ok := keys["tokenEnc"]; !ok {
			t.Error(`config.json has no "tokenEnc"`)
		}
		st2, err := Open(path, xorProtector{})
		if err != nil || st2.Token() != tok || !st2.Configured() || st2.Get().JiraURL != "https://sd.local" {
			t.Fatalf("reload: err=%v token=%q url=%q", err, st2.Token(), st2.Get().JiraURL)
		}
		// Saving without a token (nil) keeps the stored one.
		if _, _, err := st2.Save(st2.Get(), nil); err != nil || st2.Token() != tok {
			t.Errorf("token lost on save without token: %v", err)
		}
		if st3, _ := Open(path, xorProtector{}); st3.Token() != tok {
			t.Errorf("token lost on disk after save without token")
		}
		if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
			t.Error("temp file with the token left behind")
		}
	}
}

func TestClearingTheTokenRemovesItFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	tok := "secret"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	empty := "  "
	if _, _, err := st.Save(st.Get(), &empty); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("tokenEnc")) || st.HasToken() || st.Configured() {
		t.Errorf("token not cleared: %s", raw)
	}
}

func TestUndecryptableTokenKeepsSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL, s.WorklogTime = "https://sd.local", "19:15"
	tok := "secret"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path, failingProtector{unprotectErr: errors.New("DPAPI: key not valid for use in specified state")})
	if err == nil || !strings.Contains(err.Error(), "re-enter") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), tok) {
		t.Error("token in the error message")
	}
	if g := st2.Get(); g.JiraURL != "https://sd.local" || g.WorklogTime != "19:15" || st2.Token() != "" || st2.Configured() {
		t.Errorf("settings %+v token %q", g, st2.Token())
	}
	// Bad base64 is reported the same way.
	if err := os.WriteFile(path, []byte(`{"jiraUrl":"https://sd.local","tokenEnc":"%%%"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st3, err := Open(path, xorProtector{})
	if err == nil || st3.Token() != "" || st3.Get().JiraURL != "https://sd.local" {
		t.Errorf("bad base64: %v %q", err, st3.Token())
	}
}

func TestSaveFailureKeepsPreviousState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	tok := "old"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	st.prot = failingProtector{protectErr: errors.New("DPAPI unavailable")}
	s.WorklogTime = "20:00"
	newTok := "new"
	if _, _, err := st.Save(s, &newTok); err == nil {
		t.Fatal("expected an encryption error")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || st.Token() != "old" || st.Get().WorklogTime != "18:00" {
		t.Errorf("state changed after a failed save: token %q time %s", st.Token(), st.Get().WorklogTime)
	}
}

func TestNormalizeCalendar(t *testing.T) {
	s := Defaults()
	s.WorkDays = []int{5, 1, 3, 1, 7}
	s.Vacations = []Vacation{{From: " 2027-01-05 ", To: ""}, {From: "2026-12-28", To: "2027-01-02", Note: " NY "}}
	s.Holidays = []string{" 2026-12-25", "", "2026-01-01", "2026-12-25"}
	s.InProgressStatuses = []string{"In Progress", " in progress ", "В работе", ""}
	s.Normalize()
	if !reflect.DeepEqual(s.WorkDays, []int{1, 3, 5, 7}) {
		t.Errorf("workDays %v", s.WorkDays)
	}
	want := []Vacation{{From: "2026-12-28", To: "2027-01-02", Note: "NY"}, {From: "2027-01-05", To: "2027-01-05"}}
	if !reflect.DeepEqual(s.Vacations, want) {
		t.Errorf("vacations %+v", s.Vacations)
	}
	if !reflect.DeepEqual(s.Holidays, []string{"2026-01-01", "2026-12-25"}) {
		t.Errorf("holidays %v", s.Holidays)
	}
	if !reflect.DeepEqual(s.InProgressStatuses, []string{"In Progress", "В работе"}) {
		t.Errorf("statuses %v", s.InProgressStatuses)
	}
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("normalized settings invalid: %+v", errs)
	}
}
