package worklog

import (
	"strings"
	"unicode/utf8"
)

// Limits of manual entries (SPEC v0.8.5).
const (
	MaxManualEntries = 50
	MaxManualKeys    = 20
	MaxManualNote    = 200
	maxManualID      = 40
)

// Manual entry activities (ManualEntry.activity).
const (
	ActivityGrooming   = "grooming"
	ActivityEstimation = "estimation"
	ActivityQuestions  = "questions"
)

// ManualEntry is time the user adds by hand for one day: seconds in total,
// split evenly between keys (v0.8.5).
type ManualEntry struct {
	ID       string   `json:"id"`
	Keys     []string `json:"keys"`
	Seconds  int64    `json:"seconds"`
	Activity string   `json:"activity,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func (e ManualEntry) clone() ManualEntry {
	e.Keys = append([]string{}, e.Keys...)
	return e
}

// valid checks one entry (see SPEC v0.8.5).
func (e ManualEntry) valid() bool {
	if len(e.ID) == 0 || len(e.ID) > maxManualID {
		return false
	}
	for i := 0; i < len(e.ID); i++ {
		c := e.ID[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	if len(e.Keys) == 0 || len(e.Keys) > MaxManualKeys {
		return false
	}
	seen := map[string]bool{}
	for _, k := range e.Keys {
		if !ValidKey(k) || seen[k] {
			return false
		}
		seen[k] = true
	}
	if e.Seconds < 60 || e.Seconds > MaxOverrideSeconds || e.Seconds%60 != 0 {
		return false
	}
	switch e.Activity {
	case "", ActivityGrooming, ActivityEstimation, ActivityQuestions:
	default:
		return false
	}
	return utf8.RuneCountInString(e.Note) <= MaxManualNote
}

// validManual checks the entries of a day: each valid, ids unique.
func validManual(list []ManualEntry) bool {
	if len(list) > MaxManualEntries {
		return false
	}
	ids := map[string]bool{}
	for _, e := range list {
		if !e.valid() || ids[e.ID] {
			return false
		}
		ids[e.ID] = true
	}
	return true
}

// ManualShare is an issue's manual time of a day and what it was for.
type ManualShare struct {
	Seconds    int64
	Activities []string // activity codes, unique, in entry order
	Notes      []string // notes, unique, in entry order
}

// ManualTime splits each entry's seconds between its keys in whole
// minutes (the remaining minutes one each to the first keys) and adds up
// the entries per issue.
func ManualTime(list []ManualEntry) map[string]*ManualShare {
	out := map[string]*ManualShare{}
	for _, e := range list {
		n := int64(len(e.Keys))
		if n == 0 {
			continue
		}
		mins := e.Seconds / 60
		each, extra := mins/n, mins%n
		for i, k := range e.Keys {
			m := each
			if int64(i) < extra {
				m++
			}
			s := out[k]
			if s == nil {
				s = &ManualShare{}
				out[k] = s
			}
			s.Seconds += m * 60
			if e.Activity != "" && !contains(s.Activities, e.Activity) {
				s.Activities = append(s.Activities, e.Activity)
			}
			if note := strings.TrimSpace(e.Note); note != "" && !contains(s.Notes, note) {
				s.Notes = append(s.Notes, note)
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
