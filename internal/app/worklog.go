package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/state"
	"jiraffe/internal/worklog"
)

// WorklogJQL selects the issues I worked on today (SPEC, worklog rule 1).
const WorklogJQL = `(assignee = currentUser() OR status changed BY currentUser() DURING (startOfDay(), now())) AND (status changed DURING (startOfDay(), now()) OR statusCategory = "In Progress")`

var worklogFields = []string{"summary", "status", "created", "assignee"}

// errRunFailed marks a run that completed with item errors.
var errRunFailed = errors.New("worklog run had failures")

// ComputeWorklog computes the preview for now without writing anything.
// Used by the headless --worklog-preview mode.
func (a *App) ComputeWorklog(ctx context.Context) (worklog.Run, error) {
	return a.computeWorklog(ctx, worklog.TriggerPreview, time.Time{}, "")
}

// ComputeWorklogFor computes the preview for a day ("2006-01-02"; "" =
// today) without writing anything; the date is validated like the
// worklogPreview parameter. Used by the headless preview mode.
func (a *App) ComputeWorklogFor(ctx context.Context, date string) (worklog.Run, error) {
	d, err := a.runDate(date)
	if err != nil {
		return worklog.Run{}, err
	}
	return a.computeWorklog(ctx, worklog.TriggerPreview, time.Time{}, d)
}

// MaxPastDays is how far back a worklog run may compute a day.
const MaxPastDays = 30

// runDate validates the optional date of worklogPreview/worklogRun: a real
// "YYYY-MM-DD" date, not in the future and at most MaxPastDays back. Today
// is returned as "" (the normal run for today).
func (a *App) runDate(date string) (string, error) {
	if date == "" {
		return "", nil
	}
	now := a.clock.Now()
	d, err := parseDay(date, now.Location())
	if err != nil || d.After(now) || d.Before(dayStart(now).AddDate(0, 0, -MaxPastDays)) {
		return "", a.newErr(CodeBadParams, "err_bad_date", MaxPastDays)
	}
	if date == now.Format(worklog.DateLayout) {
		return "", nil
	}
	return date, nil
}

// parseDay parses a strict "YYYY-MM-DD" date as the start of that day in loc.
func parseDay(date string, loc *time.Location) (time.Time, error) {
	// The layout's fixed-width fields (4-digit year, 2-digit month and day)
	// plus the length make the parse strict; no regexp (≈250 KB in the exe).
	if len(date) != len(worklog.DateLayout) {
		return time.Time{}, fmt.Errorf("bad date %q", date)
	}
	return time.ParseInLocation(worklog.DateLayout, date, loc)
}

// dayStart is midnight of t's day.
func dayStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// effectiveNow returns the moment a run computes for. due is the moment the
// scheduler decided the run was due (zero for manual runs and previews). If
// the calendar day changed since (e.g. waiting for another run across
// midnight), the run still computes the decided day, as of that day's end.
func effectiveNow(now, due time.Time) time.Time {
	if due.IsZero() || now.Format(worklog.DateLayout) == due.Format(worklog.DateLayout) {
		return now
	}
	if now.Before(due) { // clock went backwards
		return due
	}
	y, m, d := due.Date()
	return time.Date(y, m, d, 23, 59, 59, 0, due.Location())
}

// computeWorklog computes (preview) or performs (manual/schedule) a worklog
// run. date ("2006-01-02", validated, before today) selects a past day; ""
// is today (or the day the scheduler decided, see effectiveNow). A fatal
// error (e.g. search failed) is returned as err with run.Error set;
// per-issue failures only set item errors and run.Error.
func (a *App) computeWorklog(ctx context.Context, trigger string, due time.Time, date string) (worklog.Run, error) {
	if trigger != worklog.TriggerPreview {
		a.runMu.Lock()
		defer a.runMu.Unlock()
		if a.updExiting.Load() {
			return worklog.Run{}, errUpdateRestart // the new version runs it
		}
	}
	realNow := a.clock.Now()
	now := effectiveNow(realNow, due)
	past := false
	if date != "" {
		d, err := parseDay(date, realNow.Location())
		if err != nil {
			return worklog.Run{}, a.newErr(CodeBadParams, "err_bad_date", MaxPastDays)
		}
		// The day as of its end: intervals still open then end at the
		// workday end after clipping.
		now, past = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, d.Location()), true
	}
	s := a.cfg.Get()
	run := worklog.Run{
		At:         realNow.Format(time.RFC3339),
		Date:       now.Format(worklog.DateLayout),
		Trigger:    trigger,
		Items:      []worklog.Item{},
		CapSeconds: s.CapSeconds(),
		Total:      worklog.FormatDuration(0, s.HoursPerDay),
	}
	lang := s.Language
	c, identity, err := a.client()
	if err != nil {
		run.SkippedReason = worklog.ReasonNotConfigured
		run.Error = Localize(err, lang).Message
		return run, nil
	}
	if ok, reason := calendarOf(s).Check(now); !ok {
		if !past {
			run.SkippedReason = reason
			return run, nil
		}
		// A past day is computed on request anyway; the result says so.
		run.NonWorkingDay = reason
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	jql := WorklogJQLFor(now, realNow)
	if past {
		jql = PastWorklogJQL(run.Date, s.InProgressStatuses)
	}
	planned, other, err := a.plan(ctx, c, identity, s, now, jql)
	if err != nil {
		var ce *capCheckError
		if errors.As(err, &ce) {
			e := Localize(ce.err, lang)
			err = &Error{Code: e.Code, Message: T(lang, "err_cap_check") + ": " + e.Message}
		}
		run.Error = Localize(err, lang).Message
		return run, err
	}
	run.OtherLoggedSeconds = other

	var total int64
	attempted, failed := 0, 0
	for _, p := range planned {
		started := p.Started.In(now.Location())
		item := worklog.Item{
			Key:           p.Key,
			Summary:       p.Summary,
			Status:        p.Status,
			ShareSeconds:  int64(math.Round(p.Share)),
			LoggedSeconds: p.Logged,
			TodoSeconds:   p.Todo,
			Duration:      worklog.FormatDuration(float64(p.Todo), s.HoursPerDay),
			Started:       started.Format(time.RFC3339),
		}
		switch {
		case p.Todo < worklog.MinSeconds:
			item.TodoSeconds = 0
			item.Duration = worklog.FormatDuration(0, s.HoursPerDay)
			item.Result = worklog.ResultSkipped
		case trigger == worklog.TriggerPreview:
			total += p.Todo
		default:
			attempted++
			if err := c.AddWorklog(ctx, p.Key, p.Todo, started); err != nil {
				failed++
				item.Result = worklog.ResultError
				item.Error = Localize(err, lang).Message
				log.Printf("worklog %s: add %ds failed: %v", p.Key, p.Todo, err)
			} else {
				item.Result = worklog.ResultLogged
				total += p.Todo
				log.Printf("worklog %s: logged %s from %s", p.Key, item.Duration, item.Started)
			}
		}
		run.Items = append(run.Items, item)
	}
	run.TotalSeconds = total
	run.Total = worklog.FormatDuration(float64(total), s.HoursPerDay)
	if failed > 0 {
		run.Error = T(lang, "err_run_failed", failed, attempted)
	}
	return run, nil
}

// WorklogJQLFor returns the issue query for a run computing the day of
// effective while the real clock says realNow. Normally that is WorklogJQL
// (Jira's startOfDay()). When a run decided before midnight executes after
// it, startOfDay() would mean the new day, so the window starts N+1 days back
// (N = days between the two dates, +1 for a Jira user time zone that differs
// from the PC's); local clipping then keeps exactly the effective day.
func WorklogJQLFor(effective, realNow time.Time) string {
	ey, em, ed := effective.Date()
	ry, rm, rd := realNow.In(effective.Location()).Date()
	eDay := time.Date(ey, em, ed, 12, 0, 0, 0, time.UTC)
	rDay := time.Date(ry, rm, rd, 12, 0, 0, 0, time.UTC)
	n := int(rDay.Sub(eDay).Hours()/24 + 0.5)
	if n <= 0 {
		return WorklogJQL
	}
	from := fmt.Sprintf("startOfDay(-%dd)", n+1)
	return `(assignee = currentUser() OR status changed BY currentUser() DURING (` + from + `, now())) AND ` +
		`(status changed DURING (` + from + `, now()) OR statusCategory = "In Progress")`
}

// capCheckError marks a failure to determine everything I logged today; the
// run must then fail rather than risk exceeding the daily cap.
type capCheckError struct{ err error }

func (e *capCheckError) Error() string { return "daily cap check: " + e.err.Error() }
func (e *capCheckError) Unwrap() error { return e.err }

// PastWorklogJQL selects the issues that were in progress on a past day
// (date "2006-01-02") and were mine or moved by me then; absolute bounds,
// so it does not depend on today. "" when no in-progress status is set.
func PastWorklogJQL(date string, inProgress []string) string {
	var names []string
	seen := map[string]bool{}
	for _, n := range inProgress {
		n = strings.TrimSpace(n)
		if k := strings.ToLower(n); n != "" && !seen[k] {
			seen[k] = true
			names = append(names, jqlString(n))
		}
	}
	if len(names) == 0 {
		return ""
	}
	during := fmt.Sprintf(`DURING ("%s 00:00", "%s 23:59")`, date, date)
	return `status WAS IN (` + strings.Join(names, ", ") + `) ` + during +
		` AND (assignee WAS currentUser() ` + during + ` OR status CHANGED BY currentUser() ` + during + `)`
}

// jqlString quotes a JQL string literal.
func jqlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// plan fetches the day's issues (jql) and allocates the time between them.
// It also returns my time logged that day on other (non-candidate) issues,
// which reduces the daily cap: the cap covers all my worklogs of the day.
// now is the moment computed for (the end of a past day).
func (a *App) plan(ctx context.Context, c *jira.Client, identity string, s config.Settings, now time.Time, jql string) ([]worklog.Planned, int64, error) {
	day := worklog.Day{Start: config.At(now, s.WorkdayStart), End: config.At(now, s.WorkdayEnd)}
	if now.Before(day.End) {
		day.End = now
	}
	if s.LunchStart != "" && s.LunchEnd != "" {
		day.HasLunch, day.LunchStart, day.LunchEnd = true, config.At(now, s.LunchStart), config.At(now, s.LunchEnd)
	}
	if !day.End.After(day.Start) || jql == "" {
		return nil, 0, nil
	}
	me, err := a.me(ctx, c, identity)
	if err != nil {
		return nil, 0, err
	}
	issues, err := c.Search(ctx, jql, jira.SearchOptions{Fields: worklogFields, Expand: "changelog", Limit: 200})
	if err != nil {
		return nil, 0, err
	}
	inProgress := worklog.NewStatusSet(s.InProgressStatuses)
	var cands []worklog.Candidate
	for _, is := range issues {
		if is.Changelog != nil && is.Changelog.Total > len(is.Changelog.Histories) {
			full, err := c.IssueWithChangelog(ctx, is.Key)
			if err != nil {
				return nil, 0, err
			}
			is.Changelog = full.Changelog
			if full.Fields.Created != "" {
				is.Fields.Created = full.Fields.Created
			}
			if full.Changelog != nil && full.Changelog.Total > len(full.Changelog.Histories) {
				log.Printf("worklog %s: changelog still truncated (%d of %d entries)", is.Key, len(full.Changelog.Histories), full.Changelog.Total)
			}
		}
		spans := day.Clip(issueIntervals(is, inProgress, now))
		if worklog.TotalSeconds(spans) < worklog.MinSeconds {
			continue
		}
		status := ""
		if is.Fields.Status != nil {
			status = is.Fields.Status.Name
		}
		cands = append(cands, worklog.Candidate{Key: is.Key, Summary: is.Fields.Summary, Status: status, Spans: spans})
	}
	today := now.Format(worklog.DateLayout)
	isCand := map[string]bool{}
	for i := range cands {
		isCand[cands[i].Key] = true
		wls, err := c.Worklogs(ctx, cands[i].Key)
		if err != nil {
			return nil, 0, err
		}
		cands[i].Logged = LoggedToday(wls, me, today, now.Location())
	}
	other, err := a.loggedElsewhere(ctx, c, me, now, isCand)
	if err != nil {
		return nil, 0, &capCheckError{err}
	}
	// Plan subtracts the candidates' logged time from the cap; subtracting the
	// other issues' time here makes the budget cap − everything logged today.
	// The cap is the effective one: dailyCapHours × loadFactor.
	capSeconds := s.CapSeconds() - other
	if capSeconds < 0 {
		capSeconds = 0
	}
	return worklog.Plan(cands, capSeconds), other, nil
}

// OtherWorklogsJQL finds issues with my worklogs around the given day (±1
// day, since worklogDate uses the Jira user's time zone); the exact local
// day is filtered from the worklogs themselves.
func OtherWorklogsJQL(day time.Time) string {
	y, m, d := day.Date()
	from := time.Date(y, m, d-1, 12, 0, 0, 0, day.Location()).Format(worklog.DateLayout)
	to := time.Date(y, m, d+1, 12, 0, 0, 0, day.Location()).Format(worklog.DateLayout)
	return fmt.Sprintf(`worklogAuthor = currentUser() AND worklogDate >= "%s" AND worklogDate <= "%s"`, from, to)
}

// loggedElsewhere sums my worklogs started today on issues that are not
// candidates of this run.
func (a *App) loggedElsewhere(ctx context.Context, c *jira.Client, me *jira.User, now time.Time, skip map[string]bool) (int64, error) {
	// The search embeds each issue's worklogs (up to Jira's per-issue limit);
	// the full list is only fetched when the embedded one is incomplete.
	issues, err := c.Search(ctx, OtherWorklogsJQL(now), jira.SearchOptions{Fields: []string{"worklog"}, Limit: 500})
	if err != nil {
		return 0, err
	}
	today := now.Format(worklog.DateLayout)
	var sum int64
	for _, is := range issues {
		if skip[is.Key] {
			continue
		}
		wl := is.Fields.Worklog
		wls := []jira.Worklog(nil)
		if wl != nil && wl.Complete() {
			wls = wl.Worklogs
		} else if wls, err = c.Worklogs(ctx, is.Key); err != nil {
			return 0, err
		}
		sum += LoggedToday(wls, me, today, now.Location())
	}
	return sum, nil
}

// issueIntervals rebuilds the in-progress intervals of a Jira issue.
func issueIntervals(is jira.Issue, inProgress worklog.StatusSet, now time.Time) []worklog.Interval {
	created, err := jira.ParseTime(is.Fields.Created)
	if err != nil {
		created = time.Time{}
	}
	var changes []worklog.StatusChange
	if is.Changelog != nil {
		for _, h := range is.Changelog.Histories {
			at, err := jira.ParseTime(h.Created)
			if err != nil {
				continue
			}
			for _, it := range h.Items {
				if strings.EqualFold(it.Field, "status") {
					changes = append(changes, worklog.StatusChange{At: at, From: it.FromString, To: it.ToString})
				}
			}
		}
	}
	status := ""
	if is.Fields.Status != nil {
		status = is.Fields.Status.Name
	}
	return worklog.InProgressIntervals(created, status, changes, inProgress, now)
}

// LoggedToday sums the seconds me logged with a start date of today (in loc).
func LoggedToday(wls []jira.Worklog, me *jira.User, today string, loc *time.Location) int64 {
	var sum int64
	for _, w := range wls {
		if !me.Same(w.Author) {
			continue
		}
		t, err := jira.ParseTime(w.Started)
		if err != nil || t.In(loc).Format(worklog.DateLayout) != today {
			continue
		}
		sum += w.TimeSpentSeconds
	}
	return sum
}

// record stores a non-preview run in the history and notifies the UI.
func (a *App) record(run worklog.Run, scheduledOK bool) {
	if err := a.st.Update(func(tx state.Tx) {
		tx.AddRun(run)
		if scheduledOK {
			tx.LastWorklogDate = run.Date
		}
	}); err != nil {
		log.Printf("save worklog history: %v", err)
	}
	a.emit("worklog", run)
	a.emitState()
}

// ManualWorklog performs a worklog run for today requested by the user.
func (a *App) ManualWorklog(ctx context.Context) (worklog.Run, error) {
	return a.manualWorklog(ctx, "")
}

// manualWorklog performs a worklog run requested by the user for date
// (validated by runDate; "" = today).
func (a *App) manualWorklog(ctx context.Context, date string) (worklog.Run, error) {
	run, err := a.computeWorklog(ctx, worklog.TriggerManual, time.Time{}, date)
	if errors.Is(err, errUpdateRestart) {
		return run, a.newErr(CodeConflict, "err_update_restarting")
	}
	if run.SkippedReason == worklog.ReasonNotConfigured {
		return run, errNotConfigured(a.Language())
	}
	if run.SkippedReason != "" {
		return run, nil // not a working day: reported, nothing written, not recorded
	}
	a.record(run, false)
	if date != "" {
		a.refreshMissed() // the day may no longer be missed
	}
	if err != nil {
		return run, err
	}
	return run, nil
}

// ManualWorklogFromTray runs a manual worklog in the background and reports
// the result with a balloon (the window may be closed).
func (a *App) ManualWorklogFromTray() {
	a.goBackground("tray worklog", func(ctx context.Context) {
		run, err := a.ManualWorklog(ctx)
		lang := a.Language()
		if err != nil {
			a.plat.Notify(T(lang, "worklogFailedTitle"), Localize(err, lang).Message, "")
			return
		}
		a.notifyRun(run, true)
	})
}

// ScheduledWorklog runs the scheduled worklog for now (see ScheduledWorklogAt).
func (a *App) ScheduledWorklog(ctx context.Context) error {
	return a.ScheduledWorklogAt(ctx, a.clock.Now())
}

// ScheduledWorklogAt is the scheduler hook; due is the moment the scheduler
// found the run due. The run computes that day even if midnight passes
// meanwhile, and the day is only marked done if its worklog time had been
// reached. It returns an error when the run should be retried.
func (a *App) ScheduledWorklogAt(ctx context.Context, due time.Time) error {
	run, err := a.computeWorklog(ctx, worklog.TriggerSchedule, due, "")
	if errors.Is(err, errUpdateRestart) {
		return nil // not recorded: the new version catches up
	}
	if run.SkippedReason != "" {
		return nil // not configured / not a working day: nothing to retry
	}
	reached := !due.Before(config.At(due, a.cfg.Get().WorklogTime))
	ok := err == nil && run.Error == "" && reached
	a.record(run, ok)
	a.notifyRun(run, false)
	if err != nil {
		return err
	}
	if !ok {
		return errRunFailed
	}
	return nil
}

func (a *App) notifyRun(run worklog.Run, always bool) {
	if a.Paused() && !always {
		return
	}
	lang := a.Language()
	switch {
	case run.SkippedReason != "":
		a.plat.Notify(T(lang, "worklogTitle"), T(lang, "skip_"+run.SkippedReason), "")
	case run.Error != "":
		a.plat.Notify(T(lang, "worklogFailedTitle"), run.Error, "")
	case run.TotalSeconds > 0:
		n := 0
		for _, it := range run.Items {
			if it.Result == worklog.ResultLogged {
				n++
			}
		}
		a.plat.Notify(T(lang, "worklogTitle"), T(lang, "worklogLogged", run.Total, n), "")
	case always:
		a.plat.Notify(T(lang, "worklogTitle"), T(lang, "worklogNothing"), "")
	}
}
