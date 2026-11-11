package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Every slider step 0.00, 0.05, …, 1.00 as the UI sends it (a decimal JSON
// number) is accepted, and the cap is exact: round(cap × factor × 3600).
func TestLoadFactorSliderStepsAndCapRounding(t *testing.T) {
	for k := 0; k <= 20; k++ {
		var lf float64
		lit := fmt.Sprintf("%.2f", float64(k)*0.05)
		if err := json.Unmarshal([]byte(lit), &lf); err != nil {
			t.Fatal(err)
		}
		s := Defaults() // dailyCapHours 8
		s.LoadFactor = lf
		if errs := s.Validate(); len(errs) != 0 {
			t.Errorf("loadFactor %s rejected: %+v", lit, errs)
		}
		if got, want := s.CapSeconds(), int64(k)*1440; got != want { // 8h × 0.05 = 24m
			t.Errorf("8h × %s: cap %d, want %d", lit, got, want)
		}
		s.DailyCapHours = 7.3 // 7h18m: 0.05 step = 21m54s = 1314s
		if got, want := s.CapSeconds(), int64(k)*1314; got != want {
			t.Errorf("7.3h × %s: cap %d, want %d", lit, got, want)
		}
	}
	// Rounding to the nearest second, not truncation: 1h × 0.0001 = 0.36s ⇒ 0,
	// 1h × 0.0002 = 0.72s ⇒ 1.
	for _, tc := range []struct {
		cap, lf float64
		want    int64
	}{{1, 0.0001, 0}, {1, 0.0002, 1}, {8, 1, 28800}, {24, 1, 86400}, {8, 0.6, 17280}, {6, 0.5, 10800}} {
		s := Defaults()
		s.DailyCapHours, s.LoadFactor = tc.cap, tc.lf
		if got := s.CapSeconds(); got != tc.want {
			t.Errorf("%vh × %v: cap %d, want %d", tc.cap, tc.lf, got, tc.want)
		}
	}
	// Just outside the range is rejected (no tolerance beyond 1).
	for _, lf := range []float64{1.0000001, -0.0000001} {
		s := Defaults()
		s.LoadFactor = lf
		if errs := s.Validate(); len(errs) != 1 || errs[0].Field != "loadFactor" {
			t.Errorf("loadFactor %v: %+v", lf, errs)
		}
	}
}

// A stored "loadFactor": null keeps the default; an out-of-range stored value
// is repaired without touching the valid fields around it.
func TestStoredLoadFactorNullAndRepairKeepOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"jiraUrl":"https://sd.local","loadFactor":null,"dailyCapHours":6}`)
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Get(); s.LoadFactor != 1 || s.CapSeconds() != 6*3600 {
		t.Errorf("null loadFactor: %v, cap %d", s.LoadFactor, s.CapSeconds())
	}
	write(`{"jiraUrl":"https://sd.local","loadFactor":-0.5,"dailyCapHours":6,"trackedProjects":["OPS","RBI"]}`)
	st, _ = Open(path, xorProtector{})
	s := st.Get()
	if s.LoadFactor != 1 || s.DailyCapHours != 6 || s.JiraURL != "https://sd.local" || strings.Join(s.TrackedProjects, ",") != "OPS,RBI" {
		t.Errorf("repair touched valid fields: %+v", s)
	}
	write(`{"jiraUrl":"https://sd.local","loadFactor":0.35}`)
	st, _ = Open(path, xorProtector{})
	if s := st.Get(); s.LoadFactor != 0.35 || s.CapSeconds() != 10080 {
		t.Errorf("stored 0.35: %v cap %d", s.LoadFactor, s.CapSeconds())
	}
}

// trackedProjects: [] (initialised, nothing tracked) survives a restart and is
// not confused with null; stored keys are normalised; more than 200 keys are
// rejected by validation and a too-long list on disk is dropped (re-init).
func TestTrackedProjectsEmptyListAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	if _, _, err := st.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTrackedProjects("https://sd.local", func([]string) []string { return []string{} }); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if tp := reopened.Get().TrackedProjects; tp == nil || len(tp) != 0 {
		t.Errorf("[] after restart: %#v", tp)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"trackedProjects": []`) && !strings.Contains(string(raw), `"trackedProjects":[]`) {
		t.Errorf("file %s", raw)
	}

	// Exactly 200 is fine, 201 is not.
	keys := make([]string, 201)
	for i := range keys {
		keys[i] = fmt.Sprintf("P%03d", i)
	}
	s = Defaults()
	s.Language = "en"
	s.TrackedProjects = keys[:200]
	if errs := s.Validate(); len(errs) != 0 {
		t.Errorf("200 keys: %+v", errs)
	}
	s.TrackedProjects = keys
	if errs := s.Validate(); len(errs) != 1 || errs[0].Field != "trackedProjects" || errs[0].Message != "Too many projects (at most 200)" {
		t.Errorf("201 keys: %+v", errs)
	}
	if _, err := st.UpdateTrackedProjects("https://sd.local", func([]string) []string { return keys }); err == nil {
		t.Error("201 keys stored")
	}
	if tp := st.Get().TrackedProjects; tp == nil || len(tp) != 0 {
		t.Errorf("rejected update changed the list: %v", tp)
	}
	// Normalisation on update: duplicates by case collapse.
	got, err := st.UpdateTrackedProjects("https://sd.local", func([]string) []string { return []string{"rbi", "RBI", " Rbi", "a_1"} })
	if err != nil || strings.Join(got.TrackedProjects, ",") != "A_1,RBI" {
		t.Errorf("normalised %v %v", got.TrackedProjects, err)
	}
	// Invalid keys (JQL metacharacters, leading digit) are rejected.
	for _, bad := range []string{`A") OR ("1`, "1ABC", "A-B", strings.Repeat("A", 65)} {
		s := Defaults()
		s.TrackedProjects = []string{bad}
		if errs := s.Validate(); len(errs) != 1 || errs[0].Field != "trackedProjects" {
			t.Errorf("%q: %+v", bad, errs)
		}
	}
}

// UpdateTrackedProjects is atomic: concurrent toggles of different keys are
// all kept (no lost update).
func TestUpdateTrackedProjectsConcurrentTogglesAreNotLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	if _, _, err := st.Save(s, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("P%02d", i)
			_, err := st.UpdateTrackedProjects("https://sd.local", func(cur []string) []string { return append(cur, key) })
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if n := len(st.Get().TrackedProjects); n != 20 {
		t.Errorf("%d of 20 keys kept: %v", n, st.Get().TrackedProjects)
	}
	reopened, _ := Open(path, xorProtector{})
	if n := len(reopened.Get().TrackedProjects); n != 20 {
		t.Errorf("%d persisted", n)
	}
}
