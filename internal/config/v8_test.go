package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestV8RoleDefaults(t *testing.T) {
	d := Defaults()
	if !reflect.DeepEqual(d.QAStatuses, []string{"Ready for QA", "QA", "In QA", "Testing", "In Testing", "Ready for Test", "In Test"}) ||
		!reflect.DeepEqual(d.ReviewStatuses, []string{"Review", "In Review", "Code Review"}) ||
		d.AuthorMinutes != 15 || d.CommentMinutes != 5 {
		t.Errorf("defaults %+v %+v %d %d", d.QAStatuses, d.ReviewStatuses, d.AuthorMinutes, d.CommentMinutes)
	}
	c := d.Clone()
	c.QAStatuses[0], c.ReviewStatuses[0] = "x", "y"
	if d.QAStatuses[0] == "x" || d.ReviewStatuses[0] == "y" {
		t.Error("Clone shares the role status lists")
	}
}

func TestV8OldFileGetsRoleDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := `{"jiraUrl":"https://sd.local","language":"en","inProgressStatuses":["Doing"]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	s, d := st.Get(), Defaults()
	if !reflect.DeepEqual(s.QAStatuses, d.QAStatuses) || !reflect.DeepEqual(s.ReviewStatuses, d.ReviewStatuses) ||
		s.AuthorMinutes != 15 || s.CommentMinutes != 5 || !reflect.DeepEqual(s.InProgressStatuses, []string{"Doing"}) {
		t.Errorf("old file: %+v", s)
	}
}

func TestV8RoleSettingsNormalizeAndSave(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "config.json"), xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	s := Defaults()
	s.QAStatuses = []string{" Testing ", "testing", "", "QA"}
	s.ReviewStatuses = nil // empty is allowed: the role is off
	s.AuthorMinutes, s.CommentMinutes = 0, 240
	saved, errs, err := st.Save(s, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, errs)
	}
	if !reflect.DeepEqual(saved.QAStatuses, []string{"Testing", "QA"}) || saved.ReviewStatuses == nil || len(saved.ReviewStatuses) != 0 ||
		saved.AuthorMinutes != 0 || saved.CommentMinutes != 240 {
		t.Errorf("saved %+v", saved)
	}
	// Reopened: the empty list stays empty (not the default).
	re, err := Open(st.path, xorProtector{})
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Get(); len(got.ReviewStatuses) != 0 || !reflect.DeepEqual(got.QAStatuses, []string{"Testing", "QA"}) {
		t.Errorf("reopened %+v %+v", got.QAStatuses, got.ReviewStatuses)
	}
}

func TestV8RoleSettingsValidation(t *testing.T) {
	fields := func(s Settings) map[string]bool {
		s.Normalize()
		m := map[string]bool{}
		for _, e := range s.Validate() {
			m[e.Field] = true
		}
		return m
	}
	s := Defaults()
	s.AuthorMinutes, s.CommentMinutes = 481, -1
	if f := fields(s); !f["authorMinutes"] || !f["commentMinutes"] {
		t.Errorf("minutes out of range accepted: %v", f)
	}
	s = Defaults()
	s.AuthorMinutes, s.CommentMinutes = 480, 0
	if f := fields(s); len(f) != 0 {
		t.Errorf("limits rejected: %v", f)
	}
	s = Defaults()
	s.CommentMinutes = 241
	if f := fields(s); !f["commentMinutes"] {
		t.Error("241 comment minutes accepted")
	}

	long := strings.Repeat("я", MaxStatusLen)
	s = Defaults()
	s.QAStatuses = []string{long}
	if f := fields(s); len(f) != 0 {
		t.Errorf("a %d-character status rejected: %v", MaxStatusLen, f)
	}
	s.QAStatuses = []string{long + "x"}
	s.ReviewStatuses = nil
	for i := 0; i <= MaxStatuses; i++ {
		s.ReviewStatuses = append(s.ReviewStatuses, "S"+strings.Repeat("x", i))
	}
	s.InProgressStatuses = []string{long + "y"}
	if f := fields(s); !f["qaStatuses"] || !f["reviewStatuses"] || !f["inProgressStatuses"] {
		t.Errorf("long / too many statuses accepted: %v", f)
	}

	// Broken values in a saved file fall back to their defaults.
	s = Defaults()
	s.QAStatuses, s.AuthorMinutes = []string{long + "x"}, 9999
	s.Normalize()
	r := repair(s, s.Validate())
	if !reflect.DeepEqual(r.QAStatuses, Defaults().QAStatuses) || r.AuthorMinutes != 15 {
		t.Errorf("repair %+v %d", r.QAStatuses, r.AuthorMinutes)
	}
}

// v0.8.2: the worklog comment language is "", ru, uk or en.
func TestWorklogCommentLanguage(t *testing.T) {
	s := Defaults()
	if s.WorklogCommentLanguage != "" {
		t.Errorf("default %q", s.WorklogCommentLanguage)
	}
	for _, l := range []string{"", "ru", " UK ", "en"} {
		s.WorklogCommentLanguage = l
		s.Normalize()
		for _, e := range s.Validate() {
			if e.Field == "worklogCommentLanguage" {
				t.Errorf("%q rejected", l)
			}
		}
	}
	s.WorklogCommentLanguage = "de"
	bad := false
	for _, e := range s.Validate() {
		bad = bad || e.Field == "worklogCommentLanguage"
	}
	if !bad {
		t.Error("de accepted")
	}
}
