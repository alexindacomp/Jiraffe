package app

import (
	"context"
	"encoding/json"
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

var worklogFields = []string{"summary", "status", "created", "assignee"}

// errRunFailed marks a run that completed with item errors.
var errRunFailed = errors.New("worklog run had failures")

// ComputeWorklog computes the preview for now without writing anything.
// Used by the headless --worklog-preview mode.
func (a *App) ComputeWorklog(ctx context.Context) (worklog.Run, error) {
	return a.previewWorklog(ctx, "")
}

// ComputeWorklogFor computes the preview for a day ("2006-01-02"; "" =
// today) without writing anything; the date is validated like the
// worklogPreview parameter. Used by the headless preview mode.
func (a *App) ComputeWorklogFor(ctx context.Context, date string) (worklog.Run, error) {
	d, err := a.runDate(date)
	if err != nil {
		return worklog.Run{}, err
	}
	return a.previewWorklog(ctx, d)
}

// previewWorklog computes the preview of a day (validated, "" = today)
// together with the related issues (preview only).
func (a *App) previewWorklog(ctx context.Context, date string) (worklog.Run, error) {
	run, err := a.computeWorklog(ctx, worklog.TriggerPreview, time.Time{}, date)
	if err != nil || run.SkippedReason != "" {
		return run, err
	}
	if now := a.clock.Now(); date == "" && now.Before(config.At(now, a.cfg.Get().WorkdayStart)) {
		return run, nil // the day has not started: no Jira calls at all
	}
	run.Related = a.relatedIssues(ctx, run)
	return run, nil
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

// overridesDate validates the date of get/setWorklogOverrides like
// runDate and returns the actual day ("" = today).
func (a *App) overridesDate(date string) (string, error) {
	d, err := a.runDate(date)
	if err != nil || d != "" {
		return d, err
	}
	return a.clock.Now().Format(worklog.DateLayout), nil
}

// worklogOverrides returns the fixed-time overrides of a day.
func (a *App) worklogOverrides(date string) (worklog.DayOverrides, error) {
	d, err := a.overridesDate(date)
	if err != nil {
		return worklog.DayOverrides{}, err
	}
	return a.st.Snapshot().Overrides(d), nil
}

// setWorklogOverrides validates and stores the fixed-time overrides of a
// day as a whole and returns them normalized.
func (a *App) setWorklogOverrides(date string, ov *worklog.DayOverrides) (worklog.DayOverrides, error) {
	d, err := a.overridesDate(date)
	if err != nil {
		return worklog.DayOverrides{}, err
	}
	if ov == nil || !ov.Valid() {
		return worklog.DayOverrides{}, a.newErr(CodeBadParams, "err_bad_overrides", worklog.MaxOverrideIssues)
	}
	out := ov.Clone()
	out.Normalize()
	if err := a.st.Update(func(tx state.Tx) { tx.SetOverrides(d, out, a.clock.Now()) }); err != nil {
		log.Printf("save worklog overrides: %v", err)
		return worklog.DayOverrides{}, &Error{Code: CodeInternal, Message: T(a.Language(), "err_save_failed") + ": " + err.Error()}
	}
	return out, nil
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
	jql := func(q RoleQuery) string { return WorklogJQLFor(now, realNow, q) }
	if past {
		jql = func(q RoleQuery) string { return PastWorklogJQL(run.Date, q) }
	}
	ov := a.st.Snapshot().Overrides(run.Date)
	planned, other, err := a.plan(ctx, c, identity, s, now, jql, ov)
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
			Key:            p.Key,
			Summary:        p.Summary,
			Status:         p.Status,
			Roles:          append([]string{}, p.Roles...),
			Comments:       p.Comments,
			CreateSeconds:  p.Fixed.CreateSeconds,
			CommentSeconds: p.Fixed.CommentSeconds,
			CreateSource:   p.Fixed.CreateSource,
			CommentSource:  p.Fixed.CommentSource,
			FixedSeconds:   p.Fixed.Seconds(),
			ShareSeconds:   int64(math.Round(p.Share)),
			LoggedSeconds:  p.Logged,
			TodoSeconds:    p.Todo,
			Duration:       worklog.FormatDuration(float64(p.Todo), s.HoursPerDay),
			Started:        started.Format(time.RFC3339),
		}
		item.Comment = worklog.Describe(p.Roles, int64(math.Round(p.Share)), p.Fixed.CreateSeconds, p.Fixed.CommentSeconds, p.Comments, commentLang(s))
		switch {
		case p.Todo < worklog.MinSeconds:
			item.TodoSeconds = 0
			item.Duration = worklog.FormatDuration(0, s.HoursPerDay)
			item.Result = worklog.ResultSkipped
		case trigger == worklog.TriggerPreview:
			total += p.Todo
		default:
			attempted++
			if err := c.AddWorklog(ctx, p.Key, p.Todo, started, item.Comment); err != nil {
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

// RoleQuery is what the worklog queries need to know about my roles: the
// ids of the people fields of the active roles ("" = field absent or role
// off; the Developer field is used whenever it exists) and the statuses of
// all active status roles.
type RoleQuery struct {
	DevField      string
	QAField       string
	ReviewerField string
	Statuses      []string // Dev ∪ QA ∪ Reviewer statuses, de-duplicated
}

// roleQueryOf builds the RoleQuery of the settings on a Jira with fields
// f: QA / Reviewer are active when their field exists and their status list
// is not empty.
func roleQueryOf(s config.Settings, f jira.FieldIDs) RoleQuery {
	q := RoleQuery{DevField: f.Developer}
	lists := [][]string{s.InProgressStatuses}
	if f.QA != "" && len(s.QAStatuses) > 0 {
		q.QAField = f.QA
		lists = append(lists, s.QAStatuses)
	}
	if f.Reviewer != "" && len(s.ReviewStatuses) > 0 {
		q.ReviewerField = f.Reviewer
		lists = append(lists, s.ReviewStatuses)
	}
	seen := map[string]bool{}
	for _, l := range lists {
		for _, n := range l {
			n = strings.TrimSpace(n)
			if k := strings.ToLower(n); n != "" && !seen[k] {
				seen[k] = true
				q.Statuses = append(q.Statuses, n)
			}
		}
	}
	return q
}

// KnownStatuses keeps the statuses of q that exist in Jira (names, any
// case; Jira's spelling is used). JQL rejects the whole query when a status
// name is unknown, and the role lists hold generic defaults. When the list
// cannot be read for good (400/403/404), q is kept as is.
func KnownStatuses(q RoleQuery, all []jira.Status) RoleQuery {
	if all == nil {
		return q
	}
	byName := make(map[string]string, len(all))
	for _, st := range all {
		byName[strings.ToLower(strings.TrimSpace(st.Name))] = st.Name
	}
	var keep []string
	for _, n := range q.Statuses {
		if name, ok := byName[strings.ToLower(strings.TrimSpace(n))]; ok {
			keep = append(keep, name)
		}
	}
	q.Statuses = keep
	return q
}

// knownRoleQuery is roleQueryOf limited to the statuses Jira has.
func (a *App) knownRoleQuery(ctx context.Context, c *jira.Client, s config.Settings, f jira.FieldIDs) (RoleQuery, error) {
	q := roleQueryOf(s, f)
	all, err := c.Statuses(ctx)
	if err != nil {
		if !permanent(err) {
			return q, err
		}
		log.Printf("worklog: status list unavailable, using the configured names: %v", err)
		return q, nil
	}
	return KnownStatuses(q, all), nil
}

// cfClause turns a custom field id ("customfield_10200") into its JQL
// clause name ("cf[10200]"); "" for anything else.
func cfClause(id string) string {
	n := strings.TrimPrefix(id, "customfield_")
	if n == id || n == "" {
		return ""
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return ""
		}
	}
	return "cf[" + n + "]"
}

// peopleJQL is " OR cf[N] = currentUser()" for each people field of q.
func peopleJQL(q RoleQuery) string {
	var b strings.Builder
	for _, id := range []string{q.DevField, q.QAField, q.ReviewerField} {
		if cf := cfClause(id); cf != "" {
			b.WriteString(" OR " + cf + " = currentUser()")
		}
	}
	return b.String()
}

// statusList is the quoted, comma-separated list of status names ("" if none).
func statusList(names []string) string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if k := strings.ToLower(n); n != "" && !seen[k] {
			seen[k] = true
			out = append(out, jqlString(n))
		}
	}
	return strings.Join(out, ", ")
}

// commentLang is the language of the worklog descriptions.
func commentLang(s config.Settings) string {
	if s.WorklogCommentLanguage != "" {
		return s.WorklogCommentLanguage
	}
	return s.Language
}

// WorklogJQLFor returns the status-roles query for a run computing the day
// of effective while the real clock says realNow. Normally the window starts
// at Jira's startOfDay(). When a run decided before midnight executes after
// it, startOfDay() would mean the new day, so the window starts N+1 days back
// (N = days between the two dates, +1 for a Jira user time zone that differs
// from the PC's); local clipping then keeps exactly the effective day.
func WorklogJQLFor(effective, realNow time.Time, q RoleQuery) string {
	ey, em, ed := effective.Date()
	ry, rm, rd := realNow.In(effective.Location()).Date()
	eDay := time.Date(ey, em, ed, 12, 0, 0, 0, time.UTC)
	rDay := time.Date(ry, rm, rd, 12, 0, 0, 0, time.UTC)
	from := "startOfDay()"
	if n := int(rDay.Sub(eDay).Hours()/24 + 0.5); n > 0 {
		from = fmt.Sprintf("startOfDay(-%dd)", n+1)
	}
	statusIn := ""
	if l := statusList(q.Statuses); l != "" {
		statusIn = " OR status IN (" + l + ")"
	}
	return `(assignee = currentUser()` + peopleJQL(q) + ` OR status changed BY currentUser() DURING (` + from + `, now())) AND ` +
		`(status changed DURING (` + from + `, now()) OR statusCategory = "In Progress"` + statusIn + `)`
}

// capCheckError marks a failure to determine everything I logged today; the
// run must then fail rather than risk exceeding the daily cap.
type capCheckError struct{ err error }

func (e *capCheckError) Error() string { return "daily cap check: " + e.err.Error() }
func (e *capCheckError) Unwrap() error { return e.err }

// PastWorklogJQL selects the issues that were in a status of my status
// roles on a past day (date "2006-01-02") and were mine or moved by me then;
// absolute bounds, so it does not depend on today. "" when no status is set.
func PastWorklogJQL(date string, q RoleQuery) string {
	l := statusList(q.Statuses)
	if l == "" {
		return ""
	}
	during := fmt.Sprintf(`DURING ("%s 00:00", "%s 23:59")`, date, date)
	return `status WAS IN (` + l + `) ` + during +
		` AND (assignee WAS currentUser() ` + during + peopleJQL(q) + ` OR status CHANGED BY currentUser() ` + during + `)`
}

// AuthorJQL finds the issues I created around a day (Jira's time zone); the
// exact local day is filtered from their creation time.
func AuthorJQL(date string) string {
	return fmt.Sprintf(`creator = currentUser() AND created >= "%s 00:00" AND created <= "%s 23:59"`, date, date)
}

// CommenterJQL finds the issues I may have commented on since a day.
func CommenterJQL(date string, q RoleQuery) string {
	return fmt.Sprintf(`updated >= "%s 00:00" AND (watcher = currentUser() OR assignee = currentUser() OR creator = currentUser() OR reporter = currentUser()%s)`,
		date, peopleJQL(q))
}

// RelatedJQL finds the issues where I hold a role that are open or were
// resolved since the day.
func RelatedJQL(date string, q RoleQuery) string {
	return fmt.Sprintf(`(assignee = currentUser() OR creator = currentUser()%s) AND (resolution = Unresolved OR resolved >= "%s 00:00")`,
		peopleJQL(q), date)
}

// jqlString quotes a JQL string literal.
func jqlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Limits of the role searches.
const (
	worklogSearchLimit = 200
	// worklogMaxComments bounds the comments read for an issue whose
	// embedded comment list is incomplete.
	worklogMaxComments = 1000
)

// hasUser reports whether a people field (one user or a list) holds me.
func hasUser(raw json.RawMessage, me *jira.User) bool {
	if len(raw) == 0 {
		return false
	}
	var list []jira.User
	if json.Unmarshal(raw, &list) != nil {
		var one *jira.User
		if json.Unmarshal(raw, &one) != nil || one == nil {
			return false
		}
		list = []jira.User{*one}
	}
	for i := range list {
		if me.Same(&list[i]) {
			return true
		}
	}
	return false
}

// fieldHas reports whether the people field id of the issue holds me.
func fieldHas(f *jira.Fields, id string, me *jira.User) bool {
	return id != "" && hasUser(f.Custom[id], me)
}

// StatusRoles returns my status roles on an issue found by the status-roles
// query (current field values): QA / Reviewer by their fields; Dev when the
// Developer field holds me, or otherwise when I am neither QA nor Reviewer
// on it (the query found it as assignee or status changer).
func StatusRoles(f *jira.Fields, me *jira.User, q RoleQuery) []string {
	qa := fieldHas(f, q.QAField, me)
	rev := fieldHas(f, q.ReviewerField, me)
	var out []string
	if fieldHas(f, q.DevField, me) || !qa && !rev {
		out = append(out, worklog.RoleDev)
	}
	if qa {
		out = append(out, worklog.RoleQA)
	}
	if rev {
		out = append(out, worklog.RoleReviewer)
	}
	return out
}

// roleStatuses is the union of the statuses of the given status roles.
func roleStatuses(s config.Settings, roles []string) worklog.StatusSet {
	var names []string
	for _, r := range roles {
		switch r {
		case worklog.RoleDev:
			names = append(names, s.InProgressStatuses...)
		case worklog.RoleQA:
			names = append(names, s.QAStatuses...)
		case worklog.RoleReviewer:
			names = append(names, s.ReviewStatuses...)
		}
	}
	return worklog.NewStatusSet(names)
}

// dayIssue collects what one issue did on the computed day.
type dayIssue struct {
	key, summary, status string
	statusRoles          []string
	spans                []worklog.Interval
	created              bool
	comments             int
	fixedAt              time.Time // creation / first comment that day (zero = none)
}

func (d *dayIssue) noteFixedAt(t time.Time) {
	if d.fixedAt.IsZero() || t.Before(d.fixedAt) {
		d.fixedAt = t
	}
}

// roles lists the roles of the issue in contract order.
func (d *dayIssue) roles() []string {
	out := append([]string{}, d.statusRoles...)
	if d.created {
		out = append(out, worklog.RoleAuthor)
	}
	if d.comments > 0 {
		out = append(out, worklog.RoleCommenter)
	}
	return out
}

// dayIssues keeps the issues of a day in the order they were found.
type dayIssues struct {
	order []*dayIssue
	byKey map[string]*dayIssue
}

func (d *dayIssues) get(is jira.Issue) *dayIssue {
	if x, ok := d.byKey[is.Key]; ok {
		return x
	}
	x := &dayIssue{key: is.Key, summary: is.Fields.Summary}
	if is.Fields.Status != nil {
		x.status = is.Fields.Status.Name
	}
	if d.byKey == nil {
		d.byKey = map[string]*dayIssue{}
	}
	d.byKey[is.Key] = x
	d.order = append(d.order, x)
	return x
}

// plan fetches the day's issues of all my roles and allocates the time
// between them (jql builds the status-roles query; ov are the day's fixed
// time overrides). It also returns my time logged that day on other
// (non-candidate) issues, which reduces the daily cap: the cap covers all my
// worklogs of the day. now is the moment computed for (the end of a past
// day).
func (a *App) plan(ctx context.Context, c *jira.Client, identity string, s config.Settings, now time.Time, jql func(RoleQuery) string, ov worklog.DayOverrides) ([]worklog.Planned, int64, error) {
	day := worklog.Day{Start: config.At(now, s.WorkdayStart), End: config.At(now, s.WorkdayEnd)}
	if now.Before(day.End) {
		day.End = now
	}
	if s.LunchStart != "" && s.LunchEnd != "" {
		day.HasLunch, day.LunchStart, day.LunchEnd = true, config.At(now, s.LunchStart), config.At(now, s.LunchEnd)
	}
	if !day.End.After(day.Start) {
		return nil, 0, nil
	}
	me, err := a.me(ctx, c, identity)
	if err != nil {
		return nil, 0, err
	}
	meta, err := a.metaFor(ctx, c, identity)
	if err != nil {
		return nil, 0, err
	}
	q, err := a.knownRoleQuery(ctx, c, s, meta.fields)
	if err != nil {
		return nil, 0, err
	}
	date := now.Format(worklog.DateLayout)
	var found dayIssues
	if err := a.statusIssues(ctx, c, me, s, q, jql(q), day, now, &found); err != nil {
		return nil, 0, err
	}
	if s.AuthorMinutes > 0 {
		if err := a.authorIssues(ctx, c, me, date, now.Location(), &found); err != nil {
			return nil, 0, err
		}
	}
	if s.CommentMinutes > 0 {
		if err := a.commentedIssues(ctx, c, me, q, date, now.Location(), &found); err != nil {
			return nil, 0, err
		}
	}

	// Issues taken out of the worklog get nothing (their time logged that
	// day still counts against the cap, as "other" time).
	excluded := a.st.Snapshot().Excluded()
	var fixedIn []worklog.FixedIssue
	for _, d := range found.order {
		if excluded[d.key] {
			continue
		}
		if d.created || d.comments > 0 {
			fixedIn = append(fixedIn, worklog.FixedIssue{Key: d.key, Created: d.created, Comments: d.comments})
		}
	}
	fixed := worklog.FixedTime(fixedIn, ov, int64(s.AuthorMinutes)*60, int64(s.CommentMinutes)*60)
	// A fixed-only worklog starts at the creation / first comment, kept
	// inside the workday.
	ws, we := config.At(now, s.WorkdayStart), config.At(now, s.WorkdayEnd).Add(-time.Minute)
	var cands []worklog.Candidate
	for _, d := range found.order {
		if excluded[d.key] || len(d.spans) == 0 && !d.created && d.comments == 0 {
			continue
		}
		start := d.fixedAt.In(now.Location())
		if start.Before(ws) {
			start = ws
		}
		if start.After(we) {
			start = we
		}
		cands = append(cands, worklog.Candidate{Key: d.key, Summary: d.summary, Status: d.status, Spans: d.spans,
			Roles: d.roles(), Comments: d.comments, Fixed: fixed[d.key], FixedStart: start})
	}
	isCand := map[string]bool{}
	for i := range cands {
		isCand[cands[i].Key] = true
		wls, err := c.Worklogs(ctx, cands[i].Key)
		if err != nil {
			return nil, 0, err
		}
		cands[i].Logged = LoggedToday(wls, me, date, now.Location())
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

// statusIssues adds the issues of my status roles (Dev / QA / Reviewer)
// with their time in those roles' statuses, clipped to the day; issues
// with less than a minute are left out (they may still be added as author /
// commenter).
func (a *App) statusIssues(ctx context.Context, c *jira.Client, me *jira.User, s config.Settings, q RoleQuery, jql string, day worklog.Day, now time.Time, found *dayIssues) error {
	if jql == "" {
		return nil
	}
	fields := append([]string{}, worklogFields...)
	for _, id := range []string{q.DevField, q.QAField, q.ReviewerField} {
		if id != "" {
			fields = append(fields, id)
		}
	}
	issues, err := c.Search(ctx, jql, jira.SearchOptions{Fields: fields, Expand: "changelog", Limit: worklogSearchLimit})
	if err != nil {
		return err
	}
	for _, is := range issues {
		if is.Changelog != nil && is.Changelog.Total > len(is.Changelog.Histories) {
			full, err := c.IssueWithChangelog(ctx, is.Key)
			if err != nil {
				return err
			}
			is.Changelog = full.Changelog
			if full.Fields.Created != "" {
				is.Fields.Created = full.Fields.Created
			}
			if full.Changelog != nil && full.Changelog.Total > len(full.Changelog.Histories) {
				log.Printf("worklog %s: changelog still truncated (%d of %d entries)", is.Key, len(full.Changelog.Histories), full.Changelog.Total)
			}
		}
		roles := StatusRoles(&is.Fields, me, q)
		spans := day.Clip(issueIntervals(is, roleStatuses(s, roles), now))
		if worklog.TotalSeconds(spans) < worklog.MinSeconds {
			continue
		}
		d := found.get(is)
		if d.statusRoles == nil {
			d.statusRoles, d.spans = roles, spans
		}
	}
	return nil
}

// authorIssues marks the issues I created on the day (local time).
func (a *App) authorIssues(ctx context.Context, c *jira.Client, me *jira.User, date string, loc *time.Location, found *dayIssues) error {
	issues, err := c.Search(ctx, AuthorJQL(date), jira.SearchOptions{Fields: []string{"summary", "status", "created", "creator"}, Limit: worklogSearchLimit})
	if err != nil {
		return err
	}
	for _, is := range issues {
		created, err := jira.ParseTime(is.Fields.Created)
		if err != nil || created.In(loc).Format(worklog.DateLayout) != date {
			continue
		}
		if is.Fields.Creator != nil && !me.Same(is.Fields.Creator) {
			continue
		}
		d := found.get(is)
		d.created = true
		d.noteFixedAt(created)
	}
	return nil
}

// commentedIssues counts my comments of the day (local time) per issue.
func (a *App) commentedIssues(ctx context.Context, c *jira.Client, me *jira.User, q RoleQuery, date string, loc *time.Location, found *dayIssues) error {
	issues, err := c.Search(ctx, CommenterJQL(date, q), jira.SearchOptions{Fields: []string{"summary", "status", "comment"}, Limit: worklogSearchLimit})
	if err != nil {
		return err
	}
	for _, is := range issues {
		page := is.Fields.Comment
		if page == nil {
			continue
		}
		comments := page.Comments
		if page.Total > len(page.Comments) {
			if comments, err = c.Comments(ctx, is.Key, worklogMaxComments); err != nil {
				return err
			}
		}
		n, first := 0, time.Time{}
		for _, cm := range comments {
			t, err := jira.ParseTime(cm.Created)
			if err != nil || !me.Same(cm.Author) || t.In(loc).Format(worklog.DateLayout) != date {
				continue
			}
			if n++; first.IsZero() || t.Before(first) {
				first = t
			}
		}
		if n > 0 {
			d := found.get(is)
			d.comments = n
			d.noteFixedAt(first)
		}
	}
	return nil
}

// relatedIssues lists my other issues (any role) that are open or were
// resolved since the run's day and are not items of the run. Preview only;
// failures are logged and give an empty list.
func (a *App) relatedIssues(ctx context.Context, run worklog.Run) []worklog.RelatedItem {
	c, identity, err := a.client()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	me, err := a.me(ctx, c, identity)
	if err != nil {
		log.Printf("worklog related: %v", err)
		return nil
	}
	meta, err := a.metaFor(ctx, c, identity)
	if err != nil {
		log.Printf("worklog related: %v", err)
		return nil
	}
	q, err := a.knownRoleQuery(ctx, c, a.cfg.Get(), meta.fields)
	if err != nil {
		log.Printf("worklog related: %v", err)
		return nil
	}
	fields := []string{"summary", "status", "assignee", "creator"}
	for _, id := range []string{q.DevField, q.QAField, q.ReviewerField} {
		if id != "" {
			fields = append(fields, id)
		}
	}
	issues, err := c.Search(ctx, RelatedJQL(run.Date, q), jira.SearchOptions{Fields: fields, Limit: worklogSearchLimit})
	if err != nil {
		log.Printf("worklog related: %v", err)
		return nil
	}
	skip := a.st.Snapshot().Excluded() // shown in their own list
	for _, it := range run.Items {
		skip[it.Key] = true
	}
	var out []worklog.RelatedItem
	for _, is := range issues {
		if skip[is.Key] {
			continue
		}
		skip[is.Key] = true
		f := &is.Fields
		qa, rev := fieldHas(f, q.QAField, me), fieldHas(f, q.ReviewerField, me)
		var roles []string
		if fieldHas(f, q.DevField, me) || me.Same(f.Assignee) && !qa && !rev {
			roles = append(roles, worklog.RoleDev)
		}
		if qa {
			roles = append(roles, worklog.RoleQA)
		}
		if rev {
			roles = append(roles, worklog.RoleReviewer)
		}
		if me.Same(f.Creator) {
			roles = append(roles, worklog.RoleAuthor)
		}
		if len(roles) == 0 {
			continue
		}
		item := worklog.RelatedItem{Key: is.Key, Summary: f.Summary, Roles: roles}
		if f.Status != nil {
			item.Status = f.Status.Name
		}
		out = append(out, item)
	}
	return out
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
	run.Related = nil // preview only, never stored
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
