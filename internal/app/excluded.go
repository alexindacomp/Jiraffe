package app

import (
	"sort"
	"strings"
	"unicode/utf8"

	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

// ExcludedDTO is an issue taken out of the worklog (getWorklogExcluded).
type ExcludedDTO struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
	At      string `json:"at"`
}

// maxExcludedSummary bounds the stored summary (runes).
const maxExcludedSummary = 300

// worklogExcluded lists the excluded issues, most recently excluded first.
func (a *App) worklogExcluded() []ExcludedDTO {
	out := []ExcludedDTO{}
	for k, v := range a.st.Snapshot().WorklogExcluded {
		if v.Excluded {
			out = append(out, ExcludedDTO{Key: k, Summary: v.Summary, At: v.At})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// setWorklogExcluded takes an issue out of the worklog or puts it back and
// returns the new list.
func (a *App) setWorklogExcluded(key, summary string, excluded *bool) ([]ExcludedDTO, error) {
	key = strings.TrimSpace(key)
	if !worklog.ValidKey(key) || excluded == nil {
		return nil, a.newErr(CodeBadParams, "err_bad_params")
	}
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > maxExcludedSummary {
		r := []rune(summary)
		summary = string(r[:maxExcludedSummary])
	}
	at := a.clock.Now()
	if err := a.st.Update(func(tx state.Tx) { tx.SetExcluded(key, summary, *excluded, at) }); err != nil {
		return nil, err
	}
	return a.worklogExcluded(), nil
}
