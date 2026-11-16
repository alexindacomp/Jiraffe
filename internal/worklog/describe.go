package worklog

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// describePhrases are the worklog description parts per language (v0.8.2).
// activityPhrases name the manual entry activities per language (v0.8.5).
var activityPhrases = map[string]map[string]string{
	"en": {ActivityGrooming: "grooming", ActivityEstimation: "estimation", ActivityQuestions: "clarifying questions"},
	"ru": {ActivityGrooming: "груминг", ActivityEstimation: "оценка", ActivityQuestions: "уточняющие вопросы"},
	"uk": {ActivityGrooming: "грумінг", ActivityEstimation: "оцінка", ActivityQuestions: "уточнюючі питання"},
}

// ActivityPhrase is the description text of a manual activity in lang
// (anything else: en); "" for an unknown activity.
func ActivityPhrase(activity, lang string) string {
	ph, ok := activityPhrases[lang]
	if !ok {
		ph = activityPhrases["en"]
	}
	return ph[activity]
}

var describePhrases = map[string]map[string]string{
	"en": {RoleDev: "development", RoleQA: "testing", RoleReviewer: "code review", RoleAuthor: "issue creation", RoleCommenter: "comments"},
	"ru": {RoleDev: "разработка", RoleQA: "тестирование", RoleReviewer: "код-ревью", RoleAuthor: "создание задачи", RoleCommenter: "комментарии"},
	"uk": {RoleDev: "розробка", RoleQA: "тестування", RoleReviewer: "код-рев'ю", RoleAuthor: "створення задачі", RoleCommenter: "коментарі"},
}

// Describe builds the short worklog description of an issue from what the
// time is for: the status roles when the issue has status time (share ≥ 1
// min), "issue creation" with create time, "comments (n)" with comment time.
// Parts are joined with "; ", the first one capitalised; lang is ru, uk or
// en (anything else: en). "" when there is nothing to describe.
func Describe(roles []string, shareSeconds, createSeconds, commentSeconds int64, comments int, lang string, extra ...string) string {
	ph, ok := describePhrases[lang]
	if !ok {
		ph = describePhrases["en"]
	}
	has := map[string]bool{}
	for _, r := range roles {
		has[r] = true
	}
	var parts []string
	if shareSeconds >= MinSeconds {
		for _, r := range []string{RoleDev, RoleQA, RoleReviewer} {
			if has[r] {
				parts = append(parts, ph[r])
			}
		}
	}
	if createSeconds > 0 && has[RoleAuthor] {
		parts = append(parts, ph[RoleAuthor])
	}
	if commentSeconds > 0 && has[RoleCommenter] {
		p := ph[RoleCommenter]
		if comments > 0 {
			p += " (" + strconv.Itoa(comments) + ")"
		}
		parts = append(parts, p)
	}
	for _, x := range extra { // v0.8.5: manual activities / notes
		if x = strings.TrimSpace(x); x != "" {
			dup := false
			for _, p := range parts {
				dup = dup || strings.EqualFold(p, x)
			}
			if !dup {
				parts = append(parts, x)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, "; ")
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}
