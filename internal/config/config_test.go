package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// xorProtector is a reversible fake of DPAPI.
type xorProtector struct{}

func (xorProtector) Protect(p []byte) ([]byte, error) {
	out := make([]byte, len(p))
	for i, b := range p {
		out[i] = b ^ 0x5a
	}
	return out, nil
}
func (x xorProtector) Unprotect(b []byte) ([]byte, error) { return x.Protect(b) }

func TestDefaultsAreValid(t *testing.T) {
	if errs := Defaults().Validate(); len(errs) != 0 {
		t.Fatalf("defaults invalid: %+v", errs)
	}
}

func TestValidateReportsFields(t *testing.T) {
	s := Defaults()
	s.JiraURL = "sd.local"
	s.WorklogTime = "25:00"
	s.WorkdayStart, s.WorkdayEnd = "18:00", "09:00"
	s.LunchStart, s.LunchEnd = "13:00", ""
	s.DailyCapHours = 0
	s.WorkDays = []int{}
	s.Vacations = []Vacation{{From: "2026-12-31", To: "2026-12-01"}}
	s.Holidays = []string{"25.12.2026"}
	s.InProgressStatuses = nil
	s.CommentPollMinutes = 61
	s.Language = "en"
	got := map[string]string{}
	for _, e := range s.Validate() {
		got[e.Field] = e.Message
	}
	for _, f := range []string{"jiraUrl", "worklogTime", "workdayEnd", "lunchEnd", "dailyCapHours", "workDays", "vacations", "holidays", "inProgressStatuses", "commentPollMinutes"} {
		if got[f] == "" {
			t.Errorf("missing error for %s (got %v)", f, got)
		}
	}
	s.Language = "ru"
	if m := s.Validate()[0].Message; !strings.ContainsAny(m, "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") {
		t.Errorf("expected a Russian message, got %q", m)
	}
}

func TestEmptyLunchIsAllowed(t *testing.T) {
	s := Defaults()
	s.LunchStart, s.LunchEnd = "", ""
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("got %+v", errs)
	}
}

func TestOpenMissingKeysGetDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"jiraUrl":"https://sd.local","worklogTime":"18:30","commentPollMinutes":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if s.WorklogTime != "18:30" || s.WorkdayStart != "09:00" || s.CommentPollMinutes != 5 || len(s.InProgressStatuses) != 3 {
		t.Errorf("got %+v", s)
	}
}

func TestWorklogTimeNotBeforeWorkdayEnd(t *testing.T) {
	s := Defaults()
	s.Language = "uk"
	s.WorklogTime = "17:00"
	errs := s.Validate()
	if len(errs) != 1 || errs[0].Field != "worklogTime" || !strings.Contains(errs[0].Message, "робочого дня") {
		t.Fatalf("got %+v", errs)
	}
	s.WorklogTime, s.WorkdayEnd = "17:00", "17:00"
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("equal times must be valid: %+v", errs)
	}

	// A stored config with an early run time is repaired on load.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"worklogTime":"12:00","workdayEnd":"19:00"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := Open(path, xorProtector{})
	if got := st.Get().WorklogTime; got != "19:00" {
		t.Errorf("repaired worklogTime = %s, want 19:00", got)
	}
}

func TestSameOrigin(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://sd.local", "https://SD.local/", true},
		{"https://sd.local", "https://sd.local:443", true},
		{"https://sd.local/jira", "https://sd.local", true},
		{"https://sd.local", "http://sd.local", false},
		{"https://sd.local", "https://sd.local:8443", false},
		{"https://sd.local", "https://evil.example", false},
		{"https://sd.local", "", false},
		{"http://sd.local", "http://sd.local:80", true},
	}
	for _, tc := range tests {
		if got := SameOrigin(tc.a, tc.b); got != tc.want {
			t.Errorf("SameOrigin(%q, %q) = %v", tc.a, tc.b, got)
		}
	}
}

func TestOverrideDropsTokenForOtherOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	tok := "stored"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}

	st.Override("https://sd.local/", "")
	if st.Token() != "stored" {
		t.Error("same origin must keep the stored token")
	}
	st.Override("http://sd.local", "")
	if st.Token() != "" || st.Configured() {
		t.Error("scheme change without a token must drop the stored token")
	}
	st.Override("https://evil.example", "env-token")
	if st.Token() != "env-token" || st.Get().JiraURL != "https://evil.example" {
		t.Errorf("env token not applied: %q %q", st.Token(), st.Get().JiraURL)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("evil")) {
		t.Error("Override must not persist")
	}
}

func TestWriteFileAtomicReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "f.json")
	for _, v := range []string{"one", "two"} {
		if err := WriteFileAtomic(path, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "two" {
		t.Errorf("got %q", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}
