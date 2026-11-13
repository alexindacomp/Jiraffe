package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

// Missed-day detection (SPEC v0.7.1 §3).
const (
	// CatchupLookbackDays: working days among the last N days before today
	// are checked.
	CatchupLookbackDays = 7
	// CatchupMaxDays: at most this many days are previewed per check.
	CatchupMaxDays = 5
	// catchupRetry is the pause after a check that failed (Jira unreachable).
	catchupRetry = 15 * time.Minute
)

// MissedDaysDTO is the result of getMissedWorklogDays / skipWorklogDay and
// the payload of the "worklogMissed" event: the previews of past working
// days that have something to log, oldest first.
type MissedDaysDTO struct {
	Days []worklog.Run `json:"days"`
}

// errCatchupNotConfigured stops a check when Jira is not configured.
var errCatchupNotConfigured = errors.New("not configured")

// noteJiraOK records that a Jira call succeeded; the first one of the
// session starts the missed-day check.
func (a *App) noteJiraOK() {
	if a.jiraOK.CompareAndSwap(false, true) && a.started.Load() {
		a.goBackground("missed worklog days", func(ctx context.Context) {
			if err := a.CatchUp(ctx); err != nil {
				log.Printf("missed worklog days: %v", err)
			}
		})
	}
}

// CatchUp runs the missed-day check when it is due: the app started, Jira
// configured and reached this session, automatic worklog on, not checked yet today and not
// waiting after a failure. The scheduler calls it on every tick; it returns
// the error of a check that failed.
func (a *App) CatchUp(ctx context.Context) error {
	s := a.cfg.Get()
	if !a.started.Load() || !a.cfg.Configured() || !s.WorklogEnabled || !a.jiraOK.Load() {
		return nil
	}
	if !a.catchMu.TryLock() {
		return nil // a check is running
	}
	defer a.catchMu.Unlock()
	now := a.clock.Now()
	today := now.Format(worklog.DateLayout)
	a.mu.Lock()
	due := a.catchDay != today && !now.Before(a.catchRetryAt)
	a.mu.Unlock()
	if !due {
		return nil
	}
	err := a.checkMissedDays(ctx, now)
	a.mu.Lock()
	if err != nil {
		a.catchRetryAt = now.Add(catchupRetry)
	} else {
		a.catchDay = today
	}
	a.mu.Unlock()
	return err
}

// ensureFirstRunDate returns firstRunDate, setting it once when missing:
// the earliest date in the history (an existing install), else today.
func (a *App) ensureFirstRunDate() string {
	snap := a.st.Snapshot()
	if snap.FirstRunDate != "" {
		return snap.FirstRunDate
	}
	first := a.clock.Now().Format(worklog.DateLayout)
	for _, r := range snap.History {
		if _, err := parseDay(r.Date, time.UTC); err == nil && r.Date < first {
			first = r.Date
		}
	}
	if err := a.st.Update(func(tx state.Tx) {
		if tx.FirstRunDate == "" || first < tx.FirstRunDate {
			tx.FirstRunDate = first
		}
		first = tx.FirstRunDate
	}); err != nil {
		log.Printf("save first run date: %v", err)
	}
	return first
}

// handledDays are the days with a history entry that completed (any
// trigger but preview, no run error) plus the skipped days.
func handledDays(d state.Data) map[string]bool {
	out := map[string]bool{}
	for _, r := range d.History {
		if r.Trigger != worklog.TriggerPreview && r.Error == "" {
			out[r.Date] = true
		}
	}
	for _, day := range d.SkippedWorklogDays {
		out[day] = true
	}
	return out
}

// CandidateDays lists the days the missed-day check looks at for today
// (now's day), oldest first: working days within the last
// CatchupLookbackDays before today, not before first, not handled; at most
// CatchupMaxDays.
func CandidateDays(now time.Time, cal worklog.Calendar, first string, handled map[string]bool) []string {
	var out []string
	y, m, d := now.Date()
	for i := CatchupLookbackDays; i >= 1 && len(out) < CatchupMaxDays; i-- {
		day := time.Date(y, m, d-i, 12, 0, 0, 0, now.Location())
		date := day.Format(worklog.DateLayout)
		if date < first || handled[date] {
			continue
		}
		if ok, _ := cal.Check(day); !ok {
			continue
		}
		out = append(out, date)
	}
	return out
}

// checkMissedDays previews every candidate day. A day with nothing to add
// is recorded silently as a "catchup" history entry; the others become the
// missed days (event, one balloon). A failed preview aborts the check (the
// days found so far are kept for the next one).
func (a *App) checkMissedDays(ctx context.Context, now time.Time) error {
	first := a.ensureFirstRunDate()
	s := a.cfg.Get()
	days := CandidateDays(now, calendarOf(s), first, handledDays(a.st.Snapshot()))
	var missed []worklog.Run
	recorded := 0
	for _, date := range days {
		run, err := a.computeWorklog(ctx, worklog.TriggerPreview, time.Time{}, date)
		if err != nil {
			return fmt.Errorf("preview %s: %w", date, err)
		}
		if run.SkippedReason != "" {
			return errCatchupNotConfigured
		}
		if run.TotalSeconds > 0 {
			missed = append(missed, run)
			continue
		}
		run.Trigger = worklog.TriggerCatchup
		if err := a.st.Update(func(tx state.Tx) { tx.AddRun(run) }); err != nil {
			log.Printf("save catch-up entry %s: %v", date, err)
		}
		recorded++
		log.Printf("worklog %s: nothing to add (catch-up)", date)
	}
	if recorded > 0 {
		a.emitState()
	}
	a.mu.Lock()
	changed := !sameDays(a.missed, missed) || a.missed == nil
	a.missed = missed
	if a.missed == nil {
		a.missed = []worklog.Run{}
	}
	a.mu.Unlock()
	if changed {
		a.emit("worklogMissed", MissedDaysDTO{Days: nonNilRuns(missed)})
	}
	if len(missed) > 0 {
		var dates []string
		for _, r := range missed {
			dates = append(dates, r.Date)
		}
		log.Printf("worklog: missed days %s", strings.Join(dates, ", "))
		if !a.Paused() {
			lang := a.Language()
			a.plat.Notify(T(lang, "worklogTitle"), T(lang, "worklogMissed", a.dayLabels(dates)), "")
		}
	}
	return nil
}

// missedDays returns the missed days still unhandled (another run or
// process may have handled some since the check).
func (a *App) missedDays() MissedDaysDTO {
	handled := handledDays(a.st.Snapshot())
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []worklog.Run{}
	for _, r := range a.missed {
		if !handled[r.Date] {
			out = append(out, r)
		}
	}
	return MissedDaysDTO{Days: out}
}

// refreshMissed drops handled days from the missed list and announces the
// change.
func (a *App) refreshMissed() {
	cur := a.missedDays()
	a.mu.Lock()
	changed := !sameDays(a.missed, cur.Days)
	if changed {
		a.missed = cur.Days
	}
	a.mu.Unlock()
	if changed {
		a.emit("worklogMissed", cur)
	}
}

// skipWorklogDay marks a day as not to be logged (never asked again).
func (a *App) skipWorklogDay(date string) (MissedDaysDTO, error) {
	now := a.clock.Now()
	d, err := parseDay(date, now.Location())
	if err != nil || d.After(now) || d.Before(dayStart(now).AddDate(0, 0, -MaxPastDays)) {
		return MissedDaysDTO{}, a.newErr(CodeBadParams, "err_bad_date", MaxPastDays)
	}
	if err := a.st.Update(func(tx state.Tx) { tx.SkipDay(date) }); err != nil {
		return MissedDaysDTO{}, err
	}
	log.Printf("worklog %s: skipped by the user", date)
	a.refreshMissed()
	return a.missedDays(), nil
}

func sameDays(a, b []worklog.Run) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Date != b[i].Date || a[i].TotalSeconds != b[i].TotalSeconds {
			return false
		}
	}
	return true
}

func nonNilRuns(r []worklog.Run) []worklog.Run {
	if r == nil {
		return []worklog.Run{}
	}
	return r
}

// dayLabels formats dates for a balloon: "12 Nov", "10 Nov, 12 Nov".
func (a *App) dayLabels(dates []string) string {
	lang := a.Language()
	months := shortMonths[lang]
	if months == nil {
		months = shortMonths["en"]
	}
	var out []string
	for _, ds := range dates {
		d, err := parseDay(ds, time.UTC)
		if err != nil {
			continue
		}
		out = append(out, fmt.Sprintf("%d %s", d.Day(), months[d.Month()-1]))
	}
	return strings.Join(out, ", ")
}

var shortMonths = map[string][]string{
	"en": {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
	"ru": {"янв.", "февр.", "мар.", "апр.", "мая", "июн.", "июл.", "авг.", "сент.", "окт.", "нояб.", "дек."},
	"uk": {"січ.", "лют.", "бер.", "квіт.", "трав.", "черв.", "лип.", "серп.", "вер.", "жовт.", "лист.", "груд."},
}
