package worklog

import "testing"

func TestDescribe(t *testing.T) {
	all := []string{RoleDev, RoleQA, RoleAuthor, RoleCommenter}
	tests := []struct {
		roles                  []string
		share, create, comment int64
		n                      int
		lang, want             string
	}{
		{[]string{RoleDev}, 3600, 0, 0, 0, "ru", "Разработка"},
		{all, 3600, 900, 600, 2, "ru", "Разработка; тестирование; создание задачи; комментарии (2)"},
		{all, 30, 900, 0, 0, "uk", "Створення задачі"}, // status share under a minute is not described
		{[]string{RoleReviewer, RoleCommenter}, 1200, 0, 300, 1, "en", "Code review; comments (1)"},
		{[]string{RoleQA}, 600, 0, 0, 0, "de", "Testing"}, // unknown language: en
		{[]string{RoleAuthor}, 0, 0, 0, 0, "ru", ""},      // no time: nothing to describe
		{nil, 3600, 0, 0, 0, "ru", ""},
	}
	for _, tc := range tests {
		if got := Describe(tc.roles, tc.share, tc.create, tc.comment, tc.n, tc.lang); got != tc.want {
			t.Errorf("%v %d/%d/%d %s: %q, want %q", tc.roles, tc.share, tc.create, tc.comment, tc.lang, got, tc.want)
		}
	}
}

// v0.8.4: status names match without spaces and case ("In Testing" = InTesting).
func TestStatusSetIgnoresSpaces(t *testing.T) {
	s := NewStatusSet([]string{"In Testing", " for  review "})
	for _, n := range []string{"InTesting", "intesting", "In Testing", "ForReview"} {
		if !s.Has(n) {
			t.Errorf("%q not matched", n)
		}
	}
	if s.Has("Testing") {
		t.Error("Testing matched In Testing")
	}
}
