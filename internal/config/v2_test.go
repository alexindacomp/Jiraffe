package config

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFactorValidation(t *testing.T) {
	want := map[string]string{"en": "Must be between 0 and 1", "ru": "Допустимо от 0 до 1", "uk": "Допустимо від 0 до 1"}
	for _, lf := range []float64{-0.05, 1.01, math.NaN(), math.Inf(1)} {
		for lang, msg := range want {
			s := Defaults()
			s.Language = lang
			s.LoadFactor = lf
			errs := s.Validate()
			if len(errs) != 1 || errs[0].Field != "loadFactor" || errs[0].Message != msg {
				t.Errorf("%s loadFactor=%v: %+v", lang, lf, errs)
			}
		}
	}
	for _, lf := range []float64{0, 0.05, 0.6, 1} {
		s := Defaults()
		s.LoadFactor = lf
		if errs := s.Validate(); len(errs) != 0 {
			t.Errorf("loadFactor=%v rejected: %+v", lf, errs)
		}
	}
	if d := Defaults(); d.LoadFactor != 1 || d.CapSeconds() != 8*3600 {
		t.Errorf("defaults: loadFactor %v cap %d", d.LoadFactor, d.CapSeconds())
	}
	s := Defaults()
	s.LoadFactor = 0.6
	if s.CapSeconds() != 4*3600+48*60 {
		t.Errorf("cap %d", s.CapSeconds())
	}
}

func TestOldConfigGetsV02Defaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"jiraUrl":"https://sd.local","dailyCapHours":6}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Get(); s.LoadFactor != 1 || s.TrackedProjects != nil || s.DailyCapHours != 6 {
		t.Errorf("settings %+v", s)
	}
	// An invalid stored load factor or project key falls back to the default.
	if err := os.WriteFile(path, []byte(`{"loadFactor":7,"trackedProjects":["RBI","bad key"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ = Open(path, xorProtector{})
	if s := st.Get(); s.LoadFactor != 1 || s.TrackedProjects != nil {
		t.Errorf("repaired settings %+v", s)
	}
}

func TestTrackedProjectsAreKeptBySaveAndResetOnNewOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	st, _ := Open(path, xorProtector{})
	s := Defaults()
	s.JiraURL = "https://sd.local"
	tok := "t"
	if _, _, err := st.Save(s, &tok); err != nil {
		t.Fatal(err)
	}
	if st.Get().TrackedProjects != nil {
		t.Fatal("tracked projects must start uninitialised")
	}
	if _, err := st.UpdateTrackedProjects("https://other.local", func([]string) []string { return []string{"X"} }); !errors.Is(err, ErrConnectionChanged) {
		t.Fatalf("update for another connection: %v", err)
	}
	got, err := st.UpdateTrackedProjects("https://sd.local", func(cur []string) []string {
		if cur != nil {
			t.Errorf("cur %v", cur)
		}
		return []string{" rbi ", "OPS", "RBI"}
	})
	if err != nil || strings.Join(got.TrackedProjects, ",") != "OPS,RBI" {
		t.Fatalf("update: %v %v", got.TrackedProjects, err)
	}
	if _, err := st.UpdateTrackedProjects("https://sd.local", func([]string) []string { return []string{"bad key"} }); !errors.Is(err, ErrValidation) {
		t.Errorf("invalid key accepted: %v", err)
	}
	if _, err := st.UpdateTrackedProjects("https://sd.local", func([]string) []string { return nil }); err != nil {
		t.Errorf("no-op update: %v", err)
	}

	// A settings form with a stale copy cannot change the list.
	s = st.Get()
	s.TrackedProjects = []string{"ZZZ"}
	s.WorklogTime = "18:30"
	saved, _, err := st.Save(s, nil)
	if err != nil || strings.Join(saved.TrackedProjects, ",") != "OPS,RBI" || saved.WorklogTime != "18:30" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	reopened, _ := Open(path, xorProtector{})
	if strings.Join(reopened.Get().TrackedProjects, ",") != "OPS,RBI" {
		t.Errorf("persisted %v", reopened.Get().TrackedProjects)
	}
	// Same origin spelled differently keeps them; another Jira resets them.
	s.JiraURL = "https://SD.local:443/"
	if saved, _, _ := st.Save(s, nil); saved.TrackedProjects == nil {
		t.Error("same origin reset the tracked projects")
	}
	s.JiraURL = "https://jira.other.local"
	if saved, _, _ := st.Save(s, &tok); saved.TrackedProjects != nil {
		t.Errorf("new origin kept %v", saved.TrackedProjects)
	}
	// Clone keeps nil and empty apart.
	empty := Defaults()
	empty.TrackedProjects = []string{}
	if c := empty.Clone(); c.TrackedProjects == nil {
		t.Error("clone turned [] into nil")
	}
	if c := Defaults().Clone(); c.TrackedProjects != nil {
		t.Error("clone turned nil into []")
	}
}
