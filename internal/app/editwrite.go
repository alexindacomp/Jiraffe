package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/worklog"
)

// Error codes of the edit methods.
const (
	CodeBusy          = "busy"           // the same create is already running or just done
	CodeForbiddenEdit = "forbidden_edit" // no permission to create / edit
	CodeEditConflict  = "edit_conflict"  // the issue changed since getEditMeta
)

// CreatedDTO is the createIssue result. Warning (localized) reports a part
// that did not apply although the issue was created (the sprint).
type CreatedDTO struct {
	Key     string `json:"key"`
	URL     string `json:"url"`
	Warning string `json:"warning,omitempty"`
}

// UpdatedIssueDTO is the updateIssue result. Updated is "" when it could
// not be re-read (the UI then refetches); Warning (localized) reports a
// part that did not apply although the fields were saved (the sprint).
type UpdatedIssueDTO struct {
	Key     string `json:"key"`
	Updated string `json:"updated"`
	Warning string `json:"warning,omitempty"`
}

// Limits of Jira's error texts passed on to the UI.
const (
	maxJiraErrors     = 20   // entries (field errors + messages)
	maxJiraErrorRunes = 300  // per message
	maxJiraTotalRunes = 2000 // the whole form-level message
	maxEchoedID       = 64   // field ids in messages
	maxHTMLReason     = 80   // the reason of a rejected HTML value
)

// clipText drops control characters and cuts s to at most max runes (the
// last one "…" when cut).
func clipText(s string, max int) string {
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsControl(r) {
			rs[i] = ' '
		}
	}
	out := strings.TrimSpace(string(rs))
	if rs = []rune(out); len(rs) > max {
		out = strings.TrimSpace(string(rs[:max-1])) + "…"
	}
	return out
}

// editErr maps Jira errors of edit calls: 403 ⇒ forbidden_edit, 400 with
// field errors ⇒ validation (fields keep Jira's ids).
func (a *App) editErr(err error) error {
	var je *jira.Error
	if !errors.As(err, &je) {
		return err
	}
	switch {
	case je.Code == jira.CodeForbidden:
		return a.newErr(CodeForbiddenEdit, "err_forbidden_edit")
	case je.Code == jira.CodeBadRequest && je.Kind != jira.KindInvalidKey && (len(je.FieldErrors) > 0 || len(je.Messages) > 0):
		return a.jiraValidation(je, nil)
	}
	return err
}

// jiraValidation turns Jira's 400 body into a validation error: errors on
// form fields become field errors (Jira's estimate keys map to
// "timetracking"), the rest and errorMessages the form-level message.
func (a *App) jiraValidation(je *jira.Error, fields []FieldMetaDTO) *Error {
	lang := a.Language()
	known := map[string]string{} // Jira id → form id
	for _, f := range fields {
		known[f.ID] = f.ID
		if f.Kind == FKEstimate {
			for _, k := range []string{"timeoriginalestimate", "timeestimate", "originalEstimate", "remainingEstimate",
				"timetracking_originalestimate", "timetracking_remainingestimate"} {
				known[k] = f.ID
			}
		}
	}
	e := &Error{Code: CodeValidation, Message: T(lang, "err_validation_fields")}
	var general []string
	ids := make([]string, 0, len(je.FieldErrors))
	for id := range je.FieldErrors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := 0
	for _, id := range ids {
		if entries == maxJiraErrors {
			break
		}
		entries++
		msg := clipText(je.FieldErrors[id], maxJiraErrorRunes)
		fid, ok := known[id]
		switch {
		case ok:
			e.Fields = append(e.Fields, config.FieldError{Field: fid, Message: msg})
		case fields == nil && len(id) <= maxEchoedID:
			e.Fields = append(e.Fields, config.FieldError{Field: clipText(id, maxEchoedID), Message: msg})
		default:
			general = append(general, clipText(id, maxEchoedID)+": "+msg)
		}
	}
	for _, m := range je.Messages {
		if entries == maxJiraErrors {
			break
		}
		entries++
		general = append(general, clipText(m, maxJiraErrorRunes))
	}
	if len(general) > 0 {
		e.Message = clipText(e.Message+": "+strings.Join(general, "; "), maxJiraTotalRunes)
	}
	return e
}

// fieldErrs collects per-field validation errors of our own checks.
type fieldErrs struct {
	lang string
	errs []config.FieldError
}

func (f *fieldErrs) add(field, key string, args ...any) {
	f.errs = append(f.errs, config.FieldError{Field: field, Message: T(f.lang, key, args...)})
}

func (f *fieldErrs) err() error {
	if len(f.errs) == 0 {
		return nil
	}
	return &Error{Code: CodeValidation, Message: T(f.lang, "err_validation_fields"), Fields: f.errs}
}

// writeSet are the mapped values of one write.
type writeSet struct {
	jira   map[string]any // Jira field id → Jira JSON value
	sprint *sprintMove    // sprint set through the agile API
}

type sprintMove struct{ id int } // 0 = backlog

// writeCtx is what value checks need besides the field.
type writeCtx struct {
	hoursPerDay float64
	html        htmlRules
	// htmlDescription: the description is HTML for Jira (an HTML project or
	// issue), so it is always checked as HTML, not only when it looks so.
	htmlDescription bool
	// wikiKnown: the description is positively wiki (a wiki issue, or a
	// project format from a real sample), so its literal {code}/{noformat}/
	// {{…}} regions are exempt from the wiki-target screen. Never for
	// textarea fields: their renderer comes from the field configuration.
	wikiKnown bool
}

// mapValues type-checks values against the form fields and maps them to
// Jira JSON. Only fields of the form may be written (else bad_params);
// create requires every required field without a default.
func (a *App) mapValues(values map[string]json.RawMessage, fields []FieldMetaDTO, create bool, wc writeCtx) (*writeSet, error) {
	lang := a.Language()
	byID := make(map[string]FieldMetaDTO, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}
	ids := make([]string, 0, len(values))
	for id := range values {
		if _, ok := byID[id]; !ok {
			return nil, &Error{Code: CodeBadParams, Message: T(lang, "err_unknown_field", clipText(id, maxEchoedID))}
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	errs := &fieldErrs{lang: lang}
	ws := &writeSet{jira: map[string]any{}}
	for _, id := range ids {
		f := byID[id]
		val, empty, ok := mapValue(f, values[id], wc, errs)
		if !ok {
			continue
		}
		if empty && f.Required {
			errs.add(id, "fe_required")
			continue
		}
		if f.agile {
			mv := &sprintMove{}
			if n, isNum := val.(int); isNum {
				mv.id = n
			}
			ws.sprint = mv
			continue
		}
		if empty && (create || f.Kind == FKEstimate) {
			continue // nothing to set (a new issue starts empty)
		}
		ws.jira[id] = val
	}
	if create {
		for _, f := range fields {
			if !f.Required || f.Default != nil {
				continue
			}
			_, given := values[f.ID]
			if f.Kind == FKUnsupported {
				if !given { // a given value was already reported by mapValue
					errs.add(f.ID, "fe_unsupported")
				}
				continue
			}
			if !given {
				errs.add(f.ID, "fe_required")
			}
		}
	}
	if err := errs.err(); err != nil {
		return nil, err
	}
	return ws, nil
}

// mapValue maps one FieldValue (raw JSON from the UI) to Jira JSON; empty
// reports an empty value (null, "", []); ok is false when an error was
// recorded.
func mapValue(f FieldMetaDTO, raw json.RawMessage, wc writeCtx, errs *fieldErrs) (val any, empty, ok bool) {
	bad := func(key string, args ...any) (any, bool, bool) {
		errs.add(f.ID, key, args...)
		return nil, false, false
	}
	isNull := len(raw) == 0 || strings.TrimSpace(string(raw)) == "null"
	optionIDs := func() map[string]bool {
		m := map[string]bool{}
		for _, o := range f.Options {
			m[o.ID] = true
		}
		return m
	}
	switch f.Kind {
	case FKUnsupported:
		return bad("fe_unsupported")
	case FKSummary, FKDescription, FKText, FKTextarea, FKEpicName:
		if isNull {
			return nil, true, true
		}
		var s string
		if json.Unmarshal(raw, &s) != nil || !utf8.ValidString(s) {
			return bad("fe_invalid")
		}
		if f.Kind == FKSummary || f.Kind == FKText || f.Kind == FKEpicName {
			if strings.ContainsAny(s, "\r\n") {
				return bad("fe_single_line")
			}
			s = strings.TrimSpace(s)
		}
		max := f.Max
		if max == 0 {
			max = maxText
		}
		if n := utf8.RuneCountInString(s); n > max {
			return bad("fe_too_long", max)
		}
		if strings.TrimSpace(s) == "" {
			return nil, true, true
		}
		if f.Kind == FKDescription && wc.htmlDescription || (f.Kind == FKDescription || f.Kind == FKTextarea) && needsHTMLCheckWiki(s, wc.wikiKnown && f.Kind == FKDescription) {
			if err := checkHTML(s, wc.html); err != nil {
				return bad("fe_html", clipText(err.Error(), maxHTMLReason))
			}
		}
		return s, false, true
	case FKNumber:
		if isNull {
			return nil, true, true
		}
		var n float64
		if json.Unmarshal(raw, &n) != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return bad("fe_number")
		}
		return n, false, true
	case FKStoryPoints:
		if len(f.Options) == 0 {
			f.Kind = FKNumber
			return mapValue(f, raw, wc, errs)
		}
		f.Kind = FKSelect
		return mapValue(f, raw, wc, errs)
	case FKDate:
		if isNull {
			return nil, true, true
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return bad("fe_date")
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, true, true
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return bad("fe_date")
		}
		return s, false, true
	case FKDateTime:
		if isNull {
			return nil, true, true
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return bad("fe_datetime")
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil, true, true
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
				if t, err = time.ParseInLocation(l, s, time.Local); err == nil {
					break
				}
			}
		}
		if err != nil {
			return bad("fe_datetime")
		}
		return jira.FormatTime(t), false, true
	case FKSelect, FKPriority:
		if isNull {
			return nil, true, true
		}
		id, okID := idOf(raw)
		if !okID {
			return bad("fe_invalid")
		}
		if !optionIDs()[id] {
			return bad("fe_option")
		}
		return map[string]string{"id": id}, false, true
	case FKMultiselect, FKComponents, FKVersions:
		if isNull {
			return []map[string]string{}, true, true
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || len(list) > maxListValues {
			return bad("fe_invalid")
		}
		allowed := optionIDs()
		out := []map[string]string{}
		seen := map[string]bool{}
		for _, r := range list {
			id, okID := idOf(r)
			if !okID { // the read form: plain option ids
				var s string
				okID = json.Unmarshal(r, &s) == nil && s != "" && len(s) <= 64
				id = s
			}
			if !okID {
				return bad("fe_invalid")
			}
			if !allowed[id] {
				return bad("fe_option")
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, map[string]string{"id": id})
			}
		}
		return out, len(out) == 0, true
	case FKLabels:
		if isNull {
			return []string{}, true, true
		}
		var ls []string
		if json.Unmarshal(raw, &ls) != nil || len(ls) > maxListValues {
			return bad("fe_invalid")
		}
		out := []string{}
		seen := map[string]bool{}
		for _, l := range ls {
			l = strings.TrimSpace(l)
			switch {
			case l == "":
				continue
			case strings.IndexFunc(l, unicode.IsSpace) >= 0:
				return bad("fe_label_space")
			case utf8.RuneCountInString(l) > maxText:
				return bad("fe_too_long", maxText)
			}
			if !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
		return out, len(out) == 0, true
	case FKUser:
		if isNull {
			return nil, true, true
		}
		name, okName := userName(raw)
		if !okName {
			return bad("fe_invalid")
		}
		return map[string]string{"name": name}, false, true
	case FKUsers:
		if isNull {
			return []map[string]string{}, true, true
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || len(list) > maxListValues {
			return bad("fe_invalid")
		}
		out := []map[string]string{}
		for _, r := range list {
			name, okName := userName(r)
			if !okName {
				return bad("fe_invalid")
			}
			out = append(out, map[string]string{"name": name})
		}
		return out, len(out) == 0, true
	case FKEpic, FKParent:
		if isNull {
			if f.Kind == FKParent {
				return bad("fe_required")
			}
			return nil, true, true
		}
		key, okKey := issueKeyOf(raw)
		if !okKey {
			return bad("fe_key")
		}
		if f.Kind == FKParent {
			return map[string]string{"key": key}, false, true
		}
		return key, false, true
	case FKSprint:
		if isNull {
			return nil, true, true
		}
		var n float64
		if json.Unmarshal(raw, &n) != nil || n <= 0 || n != math.Trunc(n) || n > math.MaxInt32 {
			return bad("fe_invalid")
		}
		if !optionIDs()[strconv.Itoa(int(n))] {
			return bad("fe_option")
		}
		return int(n), false, true
	case FKEstimate:
		if isNull {
			return nil, true, true
		}
		var e struct {
			OriginalEstimate  *string `json:"originalEstimate"`
			RemainingEstimate *string `json:"remainingEstimate"`
		}
		if json.Unmarshal(raw, &e) != nil {
			return bad("fe_invalid")
		}
		out := map[string]string{}
		for _, p := range []struct {
			name string
			v    *string
		}{{"originalEstimate", e.OriginalEstimate}, {"remainingEstimate", e.RemainingEstimate}} {
			if p.v == nil || strings.TrimSpace(*p.v) == "" {
				continue
			}
			_, norm, err := worklog.ParseDuration(*p.v, wc.hoursPerDay)
			if err != nil {
				return bad("fe_duration")
			}
			out[p.name] = norm
		}
		return out, len(out) == 0, true
	}
	return bad("fe_unsupported")
}

// idOf reads {"id": "…"} (an option, priority, component or version).
func idOf(raw json.RawMessage) (string, bool) {
	var o struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &o) != nil || len(o.ID) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(o.ID, &s) == nil && s != "" && len(s) <= 64 {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(o.ID, &n) == nil {
		if _, err := n.Int64(); err == nil {
			return n.String(), true
		}
	}
	return "", false
}

// userName reads {"name": "…"} or a plain user name string.
func userName(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var u struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &u) != nil {
			return "", false
		}
		s = u.Name
	}
	s = strings.TrimSpace(s)
	return s, s != "" && len(s) <= maxText && !strings.ContainsAny(s, "\r\n")
}

// issueKeyOf reads "KEY" or {"key": "KEY"}.
func issueKeyOf(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var o struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return "", false
		}
		s = o.Key
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	return s, jira.ValidIssueKey(s)
}

// afterWrite drops the caches that show issues and tells the UI to refresh.
func (a *App) afterWrite() {
	a.invalidateBoard()
	a.mu.Lock()
	a.epics = epicCache{} // epic names may have changed
	a.mu.Unlock()
	a.emit("board", struct{}{})
}

// createDedup is how long a successful create blocks the same create
// (project, type and values) again: a double click must not create twice.
const createDedup = 10 * time.Second

// createKey identifies a create request.
func createKey(project, typeID string, values map[string]json.RawMessage) string {
	b, _ := json.Marshal(values) // sorted keys, compacted values
	h := sha256.Sum256([]byte(strings.ToUpper(project) + "\x00" + typeID + "\x00" + string(b)))
	return hex.EncodeToString(h[:])
}

// beginCreate marks a create in flight; false if the same create is in
// flight or succeeded within createDedup. done(ok) ends it.
func (a *App) beginCreate(key string) (done func(ok bool), started bool) {
	now := a.clock.Now()
	a.createMu.Lock()
	defer a.createMu.Unlock()
	for k, t := range a.creating {
		if !t.IsZero() && (now.Sub(t) >= createDedup || now.Before(t)) {
			delete(a.creating, k)
		}
	}
	if _, busy := a.creating[key]; busy {
		return nil, false
	}
	if a.creating == nil {
		a.creating = map[string]time.Time{}
	}
	a.creating[key] = time.Time{} // in flight
	return func(ok bool) {
		a.createMu.Lock()
		defer a.createMu.Unlock()
		if ok {
			a.creating[key] = a.clock.Now()
		} else {
			delete(a.creating, key)
		}
	}, true
}

func (a *App) createIssue(ctx context.Context, project, typeID string, values map[string]json.RawMessage) (*CreatedDTO, error) {
	if typeID == "" {
		return nil, a.badParams("issueTypeId is required")
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	m, err := a.createMetaFor(ctx, c, id, project, typeID)
	if err != nil {
		return nil, err
	}
	if m.dto.IssueTypeID != typeID {
		return nil, a.badParams("issueTypeId is not offered for this project")
	}
	ws, err := a.mapValues(values, m.dto.Fields, true, writeCtx{hoursPerDay: a.cfg.Get().HoursPerDay, html: htmlRules{jiraURL: c.BaseURL()},
		htmlDescription: m.dto.DescriptionFormat == DescHTML, wikiKnown: m.dto.DescriptionFormat == DescWiki && m.formatKnown})
	if err != nil {
		return nil, err
	}
	done, started := a.beginCreate(createKey(m.dto.Project.Key, typeID, values))
	if !started {
		return nil, a.newErr(CodeBusy, "err_busy_create")
	}
	ok := false
	defer func() { done(ok) }()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ws.jira["project"] = map[string]string{"key": m.dto.Project.Key}
	ws.jira["issuetype"] = map[string]string{"id": typeID}
	created, err := c.CreateIssue(ctx, ws.jira)
	if err != nil {
		var je *jira.Error
		if errors.As(err, &je) && je.Code == jira.CodeBadRequest && (len(je.FieldErrors) > 0 || len(je.Messages) > 0) {
			return nil, a.jiraValidation(je, m.dto.Fields)
		}
		return nil, a.editErr(err)
	}
	ok = true
	defer a.afterWrite()
	out := &CreatedDTO{Key: created.Key, URL: c.BrowseURL(created.Key)}
	if ws.sprint != nil && ws.sprint.id > 0 {
		if err := c.MoveToSprint(ctx, ws.sprint.id, []string{created.Key}); err != nil {
			// The issue exists: report the sprint as a warning.
			log.Printf("create %s: move to sprint %d: %v", created.Key, ws.sprint.id, err)
			out.Warning = T(a.Language(), "warn_sprint_not_changed", Localize(err, a.Language()).Message)
		}
	}
	return out, nil
}

// sameUpdated reports whether two "updated" values are the same moment
// (exact text, or the same instant to the millisecond).
func sameUpdated(a, b string) bool {
	if a == b {
		return true
	}
	ta, err1 := jira.ParseTime(a)
	tb, err2 := jira.ParseTime(b)
	return err1 == nil && err2 == nil && ta.Equal(tb)
}

func (a *App) updateIssue(ctx context.Context, key string, values map[string]json.RawMessage, expected string) (*UpdatedIssueDTO, error) {
	if !jira.ValidIssueKey(key) {
		return nil, a.newErr(CodeBadParams, "err_invalid_key")
	}
	if strings.TrimSpace(expected) == "" {
		return nil, a.badParams("expectedUpdated is required")
	}
	c, id, err := a.client()
	if err != nil {
		return nil, err
	}
	st, err := a.editStateOf(ctx, c, id, key, false)
	if err != nil {
		return nil, err
	}
	if len(st.dto.Fields) == 0 {
		return nil, a.newErr(CodeForbiddenEdit, "err_forbidden_edit")
	}
	if !sameUpdated(st.dto.Updated, expected) {
		return nil, a.newErr(CodeEditConflict, "err_edit_conflict")
	}
	// The issue's own format, as getEditMeta reports it: a wiki issue stays
	// wiki even in an HTML project; an empty description uses the project's.
	format, known := descriptionFormat(st.desc), true
	if format == "" && len(values) > 0 {
		format, known = a.projectDescriptionFormatKnown(ctx, c, id, st.dto.Project.Key)
	}
	ws, err := a.mapValues(values, st.dto.Fields, false, writeCtx{hoursPerDay: a.cfg.Get().HoursPerDay,
		html: htmlRules{jiraURL: c.BaseURL(), known: imgSources(st.desc)}, htmlDescription: format == DescHTML,
		wikiKnown: format == DescWiki && known})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out := &UpdatedIssueDTO{Key: key, Updated: st.dto.Updated}
	wrote := false
	if len(ws.jira) > 0 {
		if err := c.UpdateIssue(ctx, key, ws.jira); err != nil {
			var je *jira.Error
			if errors.As(err, &je) && je.Code == jira.CodeBadRequest && (len(je.FieldErrors) > 0 || len(je.Messages) > 0) {
				return nil, a.jiraValidation(je, st.dto.Fields)
			}
			return nil, a.editErr(err)
		}
		wrote = true
	}
	if ws.sprint != nil {
		var err error
		if ws.sprint.id > 0 {
			err = c.MoveToSprint(ctx, ws.sprint.id, []string{key})
		} else {
			err = c.MoveToBacklog(ctx, []string{key})
		}
		switch {
		case err != nil && !wrote:
			return nil, a.editErr(err) // nothing was saved
		case err != nil:
			// The fields are saved: a failure here must not look like a
			// failed save (a retry would only meet edit_conflict).
			log.Printf("update %s: sprint move: %v", key, err)
			out.Warning = T(a.Language(), "warn_sprint_not_changed", Localize(err, a.Language()).Message)
		default:
			wrote = true
		}
	}
	if wrote {
		a.afterWrite()
		out.Updated = "" // unknown unless re-read: the UI refetches
		if raw, err := c.IssueFields(ctx, key, []string{"updated"}); err == nil {
			var u string
			if json.Unmarshal(raw["updated"], &u) == nil {
				out.Updated = u
			}
		} else {
			log.Printf("update %s: re-read updated: %v", key, err)
		}
	}
	return out, nil
}
