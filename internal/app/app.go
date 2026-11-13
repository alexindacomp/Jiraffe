// Package app is the binding layer between the UI (JS contract in SPEC.md)
// and the domain packages. It is OS independent: everything platform
// specific comes in through the Platform interface.
package app

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jiraffe/internal/config"
	"jiraffe/internal/jira"
	"jiraffe/internal/scheduler"
	"jiraffe/internal/state"
	"jiraffe/internal/update"
	"jiraffe/internal/worklog"
)

// Version is set at build time from the git tag by build.sh
// (-ldflags "-X jiraffe/internal/app.Version=…").
var Version = "dev"

// Platform provides the OS services the app needs.
type Platform interface {
	// Transport performs HTTP requests (WinHTTP on Windows).
	Transport() jira.Transport
	// OpenURL opens a URL in the default browser.
	OpenURL(u string) error
	// SetAutostart writes or removes the autostart entry.
	SetAutostart(enabled bool) error
	// Notify shows a tray balloon; clicking it opens url ("" = open the app).
	Notify(title, text, url string)
	// SettingsChanged lets the platform refresh e.g. the tray menu language.
	SettingsChanged(s config.Settings, paused bool)
	// SetWindowTheme recolours the native frame (title bar) of the app
	// window. It never fails: no window or no support ⇒ nothing happens.
	SetWindowTheme(t WindowTheme)
}

// Emitter pushes an event to the UI (dropped if no window is open).
type Emitter func(typ string, payload any)

// Error is the error payload returned to JS: {"code","message","fields"?}.
type Error struct {
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Fields  []config.FieldError `json:"fields,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Error codes produced by the app itself (Jira errors keep jira.Code*).
const (
	CodeNotConfigured = "not_configured"
	CodeValidation    = "validation"
	CodeBadParams     = "bad_params"
	CodeUnknownMethod = "unknown_method"
	CodeForbiddenURL  = "forbidden_url"
	CodeInternal      = "internal"
	CodeConflict      = "conflict" // the connection changed during the call
	// CodeProjectUnavailable: the project parameter names no project I can
	// browse (distinct from Jira's own HTTP 400s, which are "bad_request").
	CodeProjectUnavailable = "project_unavailable"
)

// CodeBadNonce rejects calls from pages that are not the app (missing or
// wrong nonce).
const CodeBadNonce = "bad_nonce"

func errNotConfigured(lang string) *Error {
	return &Error{Code: CodeNotConfigured, Message: T(lang, "err_not_configured")}
}

// newErr builds an app error with a message localized to the UI language.
func (a *App) newErr(code, key string, args ...any) *Error {
	return &Error{Code: code, Message: T(a.Language(), key, args...)}
}

// ToError converts any error into the JS error payload with English messages.
func ToError(err error) *Error { return Localize(err, "en") }

// ErrorFor converts any error into the JS error payload, localized to the
// configured UI language.
func (a *App) ErrorFor(err error) *Error { return Localize(err, a.Language()) }

// Localize converts err into the JS error payload with a message in lang.
// Text that comes from Jira itself is appended untranslated.
func Localize(err error, lang string) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	var je *jira.Error
	if errors.As(err, &je) {
		var msg string
		detail := je.Detail
		switch je.Code {
		case jira.CodeNetwork:
			key := "net_" + je.Kind
			if !hasText(key) || je.Kind == jira.NetOther {
				key, detail = "net_other", je.Message
			}
			msg = T(lang, key)
		case jira.CodeBadRequest:
			if je.Kind == jira.KindInvalidKey {
				msg, detail = T(lang, "err_invalid_key"), ""
			} else {
				msg = T(lang, "err_bad_request", je.Status)
			}
		case jira.CodeServer:
			msg = T(lang, "err_jira_error", je.Status)
		case jira.CodeBadResponse:
			switch je.Kind {
			case jira.KindHTML:
				msg = T(lang, "err_bad_response_html")
			case jira.KindRedirect:
				msg, detail = T(lang, "err_redirect", je.Detail), ""
			default:
				msg = T(lang, "err_bad_response")
			}
		default: // unauthorized, forbidden, not_found
			msg = T(lang, "err_"+je.Code)
		}
		if detail != "" {
			msg += ": " + detail
		}
		return &Error{Code: je.Code, Message: msg}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Error{Code: jira.CodeNetwork, Message: T(lang, "net_timeout")}
	}
	return &Error{Code: CodeInternal, Message: T(lang, "err_internal") + ": " + err.Error()}
}

// CheckNonce reports, in constant time, whether got (the JSON-encoded 4th
// binding argument) is the string want. Pages other than the app (e.g. a
// dropped file) do not know the nonce.
func CheckNonce(want string, got json.RawMessage) bool {
	var s string
	if want == "" || len(got) == 0 || json.Unmarshal(got, &s) != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s), []byte(want)) == 1
}

// BadNonceError is returned to callers without a valid nonce.
func BadNonceError(lang string) *Error {
	return &Error{Code: CodeBadNonce, Message: T(lang, "err_bad_nonce")}
}

// AutostartReader is optionally implemented by a Platform that can report
// whether the autostart entry is really in place.
type AutostartReader interface {
	// AutostartEnabled reports whether autostart is registered for this exe.
	AutostartEnabled() (bool, error)
}

// App implements the JS ↔ Go contract.
type App struct {
	cfg   *config.Store
	st    *state.Store
	plat  Platform
	clock scheduler.Clock
	emit  Emitter
	sched *scheduler.Scheduler

	mu       sync.Mutex
	user     *jira.User
	userFor  string                // identity (url + token) the cached user belongs to
	cache    map[string]cacheEntry // board, epics, projects, … (see cache.go)
	cacheGen uint64                // bumped on every invalidation; stale fetches are not cached
	meta     *metaEntry            // custom field ids and issue type ids
	metaMu   sync.Mutex            // one metadata discovery at a time
	// layoutBuilds marks the projects whose board layout is being built
	// (closed when done); guarded by mu.
	layoutBuilds map[string]chan struct{}
	epics        epicCache  // epic names/colours for epic chips
	trackMu      sync.Mutex // serializes the lazy trackedProjects initialisation
	runMu        sync.Mutex // serializes worklog runs that write to Jira
	pollMu       sync.Mutex // serializes comment polls
	bgWG         sync.WaitGroup
	bgCtx        context.Context
	bgCancel     context.CancelFunc
	startOnce    sync.Once

	// createIssue duplicate guard (editwrite.go): request key → zero while
	// in flight, else the time it succeeded.
	createMu sync.Mutex
	creating map[string]time.Time

	// updates (see update.go); upd is nil when updates are not enabled
	upd        *update.Updater
	updHost    UpdateHost
	updCheckMu sync.Mutex  // one check at a time
	updFilesMu sync.Mutex  // held by the install job; cleanup skips its pass meanwhile
	swapMu     sync.Mutex  // held while the exe is swapped; Stop waits for it (no time limit)
	updJob     atomic.Bool // an install job is running
	updExiting atomic.Bool // the new exe was started: no more worklog runs
	// guarded by mu
	updLastAuto  time.Time   // last automatic check of this session
	updAnnounced string      // version announced by balloon this session
	justUpdated  *UpdatedDTO // set after an update (this session)
}

// New creates the app. emit may be nil.
func New(cfg *config.Store, st *state.Store, plat Platform, clock scheduler.Clock, emit Emitter) *App {
	if emit == nil {
		emit = func(string, any) {}
	}
	a := &App{cfg: cfg, st: st, plat: plat, clock: clock, emit: emit}
	a.bgCtx, a.bgCancel = context.WithCancel(context.Background())
	a.sched = scheduler.New(clock, scheduler.Hooks{
		Config:       a.schedulerConfig,
		LastRunDate:  func() string { return a.st.Snapshot().LastWorklogDate },
		RunWorklog:   a.ScheduledWorklogAt,
		PollComments: a.PollComments,
	})
	return a
}

// Start applies the autostart setting, fetches the current user and starts
// the scheduler. It returns immediately.
func (a *App) Start() {
	a.startOnce.Do(func() {
		s := a.cfg.Get()
		if err := a.plat.SetAutostart(s.Autostart); err != nil {
			log.Printf("autostart: %v", err)
		}
		a.plat.SettingsChanged(s, a.Paused())
		a.goBackground("refresh user", func(ctx context.Context) { a.refreshUser(ctx) })
		a.goBackground("scheduler", func(ctx context.Context) { a.sched.Run(ctx, 30*time.Second) })
		if a.upd != nil {
			a.goBackground("update cleanup", a.cleanupUpdates)
			a.goBackground("update check", a.updateLoop)
		}
	})
}

// Stop cancels background work and waits up to timeout for it to finish.
func (a *App) Stop(timeout time.Duration) {
	a.bgCancel()
	// An exe swap in progress always completes (or rolls back) first, so the
	// process never exits between renaming jiraffe.exe away and putting the
	// new one in place. It is bounded by the rename retries; a swap that
	// starts after this sees the cancelled context and does nothing.
	a.swapMu.Lock()
	a.swapMu.Unlock() // nothing to protect: only waiting for the swap
	done := make(chan struct{})
	go func() { a.bgWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("shutdown: background work did not finish in %s", timeout)
	}
}

// goBackground runs fn in a goroutine with panic recovery, tracked by Stop.
func (a *App) goBackground(what string, fn func(ctx context.Context)) {
	a.bgWG.Add(1)
	go func() {
		defer a.bgWG.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("%s panicked: %v\n%s", what, r, debug.Stack())
			}
		}()
		fn(a.bgCtx)
	}()
}

// Context returns the app lifetime context (cancelled by Stop).
func (a *App) Context() context.Context { return a.bgCtx }

// Call executes one contract method. params is the JSON parameter object (a
// JSON string containing JSON is accepted too). It never panics.
func (a *App) Call(ctx context.Context, method string, params json.RawMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("call %s panicked: %v\n%s", method, r, debug.Stack())
			result, err = nil, &Error{Code: CodeInternal, Message: fmt.Sprintf("internal error in %s", method)}
		}
	}()
	params = normalizeParams(params)
	switch method {
	case "getState":
		return a.State(), nil
	case "getSettings":
		return a.settingsView(a.cfg.Get()), nil
	case "testConnection":
		return a.testConnection(ctx, params)
	case "saveSettings":
		return a.saveSettings(params)
	case "getBoard":
		var p struct {
			Force   bool   `json:"force"`
			Scope   string `json:"scope"`
			Project string `json:"project"`
			BoardID int    `json:"boardId"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getBoard(ctx, p.Force, p.Scope, p.Project, p.BoardID)
	case "getIssue":
		var p struct {
			Key string `json:"key"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getIssue(ctx, p.Key)
	case "getProjects":
		var p struct {
			Force bool `json:"force"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getProjects(ctx, p.Force)
	case "setProjectTracked":
		var p struct {
			Key     string `json:"key"`
			Tracked bool   `json:"tracked"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.setProjectTracked(ctx, p.Key, p.Tracked)
	case "getAgileBoards":
		var p struct {
			Force bool `json:"force"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getAgileBoards(ctx, p.Force)
	case "getSprints":
		var p struct {
			BoardID int  `json:"boardId"`
			Force   bool `json:"force"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getSprints(ctx, p.BoardID, p.Force)
	case "getSprintGroups":
		var p struct {
			Project       string `json:"project"`
			Scope         string `json:"scope"`
			IncludeClosed bool   `json:"includeClosed"`
			Force         bool   `json:"force"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getSprintGroups(ctx, p.Project, p.Scope, p.IncludeClosed, p.Force)
	case "getEpics", "getStories":
		var p struct {
			IncludeDone bool   `json:"includeDone"`
			Scope       string `json:"scope"`
			Project     string `json:"project"`
			Force       bool   `json:"force"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		if method == "getEpics" {
			return a.getEpics(ctx, p.IncludeDone, p.Scope, p.Project, p.Force)
		}
		return a.getStories(ctx, p.IncludeDone, p.Scope, p.Project, p.Force)
	case "getTransitions":
		var p struct {
			Key string `json:"key"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getTransitions(ctx, p.Key)
	case "transition":
		var p struct {
			Key          string `json:"key"`
			TransitionID string `json:"transitionId"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.transition(ctx, p.Key, p.TransitionID)
	case "openUrl":
		var p struct {
			URL string `json:"url"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.openURL(p.URL)
	case "setWindowTheme":
		var p struct {
			Dark    *bool   `json:"dark"`
			Caption *string `json:"caption"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.setWindowTheme(p.Dark, p.Caption)
	case "getCreateMeta":
		var p struct {
			Project     string `json:"project"`
			IssueTypeID string `json:"issueTypeId"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getCreateMeta(ctx, p.Project, p.IssueTypeID)
	case "getEditMeta":
		var p struct {
			Key string `json:"key"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.getEditMeta(ctx, p.Key)
	case "searchAssignable":
		var p struct {
			Project string `json:"project"`
			Key     string `json:"key"`
			Query   string `json:"query"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.searchAssignable(ctx, p.Project, p.Key, p.Query)
	case "searchIssuesFor":
		var p struct {
			Purpose string `json:"purpose"`
			Project string `json:"project"`
			Query   string `json:"query"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.searchIssuesFor(ctx, p.Purpose, p.Project, p.Query)
	case "createIssue":
		var p struct {
			Project     string                     `json:"project"`
			IssueTypeID string                     `json:"issueTypeId"`
			Values      map[string]json.RawMessage `json:"values"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.createIssue(ctx, p.Project, p.IssueTypeID, p.Values)
	case "updateIssue":
		var p struct {
			Key             string                     `json:"key"`
			Values          map[string]json.RawMessage `json:"values"`
			ExpectedUpdated string                     `json:"expectedUpdated"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.updateIssue(ctx, p.Key, p.Values, p.ExpectedUpdated)
	case "searchUsers":
		var p struct {
			Query string `json:"query"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.searchUsers(ctx, p.Query)
	case "openFieldLink":
		var p struct {
			Key   string `json:"key"`
			Field string `json:"field"`
			URL   string `json:"url"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.openFieldLink(ctx, p.Key, p.Field, p.URL)
	case "getLinkTypes":
		return a.getLinkTypes(ctx)
	case "addIssueLink":
		var p struct {
			Key       string `json:"key"`
			TypeID    string `json:"typeId"`
			Direction string `json:"direction"`
			Target    string `json:"target"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.addIssueLink(ctx, p.Key, p.TypeID, p.Direction, p.Target)
	case "deleteIssueLink":
		var p struct {
			Key    string `json:"key"`
			LinkID string `json:"linkId"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.deleteIssueLink(ctx, p.Key, p.LinkID)
	case "addComment":
		var p struct {
			Key    string `json:"key"`
			Body   string `json:"body"`
			Format string `json:"format"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.addComment(ctx, p.Key, p.Body, p.Format)
	case "getWindowInfo":
		return a.getWindowInfo(), nil
	case "windowControl":
		var p struct {
			Action string `json:"action"`
			Edge   string `json:"edge"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.windowControl(p.Action, p.Edge)
	case "getUpdateStatus":
		return a.updateStatus(), nil
	case "checkForUpdates":
		return a.checkUpdates(ctx)
	case "installUpdate", "skipUpdate":
		var p struct {
			Version string `json:"version"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		if method == "skipUpdate" {
			return a.skipUpdate(p.Version)
		}
		return nil, a.installUpdate(p.Version)
	case "worklogPreview":
		run, err := a.computeWorklog(ctx, worklog.TriggerPreview, time.Time{})
		if err != nil {
			return nil, err
		}
		return run, nil
	case "worklogRun":
		return a.ManualWorklog(ctx)
	case "getWorklogHistory":
		h := a.st.Snapshot().History
		if h == nil {
			h = []worklog.Run{}
		}
		return h, nil
	case "getNotifications":
		n := a.st.Snapshot().Notifications
		if n == nil {
			n = []state.Notification{}
		}
		return n, nil
	case "getUiPrefs":
		return a.uiPrefs(), nil
	case "setUiPrefs":
		var p struct {
			Prefs json.RawMessage `json:"prefs"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return nil, a.setUIPrefs(p.Prefs)
	case "markNotificationsRead":
		var p struct {
			IDs *[]string `json:"ids"`
		}
		if err := a.decode(params, &p); err != nil {
			return nil, err
		}
		return a.markRead(p.IDs)
	default:
		return nil, a.newErr(CodeUnknownMethod, "err_unknown_method", method)
	}
}

// normalizeParams unwraps a JSON string holding JSON and maps empty to {}.
func normalizeParams(p json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(p))
	if strings.HasPrefix(t, `"`) {
		var inner string
		if json.Unmarshal([]byte(t), &inner) == nil {
			t = strings.TrimSpace(inner)
		}
	}
	if t == "" || t == "null" {
		t = "{}"
	}
	return json.RawMessage(t)
}

func (a *App) decode(params json.RawMessage, v any) error {
	if err := json.Unmarshal(params, v); err != nil {
		return &Error{Code: CodeBadParams, Message: T(a.Language(), "err_bad_params") + ": " + err.Error()}
	}
	return nil
}

// --- state -----------------------------------------------------------------

// UserDTO is the user in getState.
type UserDTO struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// StateDTO is the getState result and the "state" event payload.
type StateDTO struct {
	Configured    bool         `json:"configured"`
	User          *UserDTO     `json:"user,omitempty"`
	Version       string       `json:"version"`
	Language      string       `json:"language"`
	Unread        int          `json:"unread"`
	NextWorklogAt string       `json:"nextWorklogAt,omitempty"`
	LastWorklog   *worklog.Run `json:"lastWorklog,omitempty"`
}

// State returns the current app state (no network access).
func (a *App) State() StateDTO {
	s := a.cfg.Get()
	snap := a.st.Snapshot()
	out := StateDTO{
		Configured: a.cfg.Configured(),
		Version:    Version,
		Language:   s.Language,
		Unread:     snap.Unread(),
	}
	if u := a.cachedUser(); u != nil {
		out.User = &UserDTO{Name: u.Name, DisplayName: u.DisplayName}
	}
	if next, ok := a.sched.NextWorklogAt(a.clock.Now(), a.schedulerConfig(), snap.LastWorklogDate); ok {
		out.NextWorklogAt = next.Format(time.RFC3339)
	}
	if len(snap.History) > 0 {
		last := snap.History[0]
		out.LastWorklog = &last
	}
	return out
}

func (a *App) emitState() { a.emit("state", a.State()) }

func (a *App) schedulerConfig() scheduler.Config {
	s := a.cfg.Get()
	m, err := config.ParseClock(s.WorklogTime)
	if err != nil {
		m = 18 * 60
	}
	return scheduler.Config{
		Configured:     a.cfg.Configured(),
		WorklogEnabled: s.WorklogEnabled,
		WorklogMinute:  m,
		Calendar:       calendarOf(s),
		NotifyComments: s.NotifyComments,
		PollEvery:      time.Duration(s.CommentPollMinutes) * time.Minute,
	}
}

func calendarOf(s config.Settings) worklog.Calendar {
	c := worklog.Calendar{WorkDays: s.WorkDays, Holidays: s.Holidays}
	for _, v := range s.Vacations {
		c.Vacations = append(c.Vacations, worklog.DateRange{From: v.From, To: v.To})
	}
	return c
}

// --- jira client / user ----------------------------------------------------

// client returns a Jira client for the saved settings.
func (a *App) client() (*jira.Client, string, error) {
	if !a.cfg.Configured() {
		return nil, "", errNotConfigured(a.Language())
	}
	s := a.cfg.Get()
	tok := a.cfg.Token()
	return jira.New(s.JiraURL, tok, a.plat.Transport()), s.JiraURL + "\x00" + tok, nil
}

func (a *App) cachedUser() *jira.User {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.user == nil {
		return nil
	}
	if a.cfg.Configured() && a.userFor == a.cfg.Get().JiraURL+"\x00"+a.cfg.Token() {
		u := *a.user
		return &u
	}
	return nil
}

// me returns the current Jira user, cached per URL+token.
func (a *App) me(ctx context.Context, c *jira.Client, identity string) (*jira.User, error) {
	a.mu.Lock()
	if a.user != nil && a.userFor == identity {
		u := *a.user
		a.mu.Unlock()
		return &u, nil
	}
	a.mu.Unlock()
	u, err := c.Myself(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.user, a.userFor = u, identity
	a.mu.Unlock()
	return u, nil
}

func (a *App) refreshUser(ctx context.Context) {
	c, id, err := a.client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := a.me(ctx, c, id); err != nil {
		log.Printf("fetch current user: %v", err)
		return
	}
	a.emitState()
}

// invalidateCaches drops everything learned from Jira (connection changed).
func (a *App) invalidateCaches() {
	a.mu.Lock()
	a.user, a.userFor = nil, ""
	a.cache = nil
	a.meta = nil
	a.epics = epicCache{}
	a.cacheGen++
	a.mu.Unlock()
}

// invalidateBoard drops the cached issue lists (boards, sprint groups,
// epics, stories, project issue counts) after issues changed; a fetch
// already in flight will not store its (possibly stale) result.
func (a *App) invalidateBoard() {
	a.dropCache(cacheBoard, cacheSprintGroups, cacheEpics, cacheStories, cacheProjects)
}

// --- settings --------------------------------------------------------------

// SettingsView is the Settings contract type (token never included).
// Autostart is the stored preference; AutostartEffective is whether the
// autostart entry really exists for this exe (they differ when writing the
// registry failed).
type SettingsView struct {
	config.Settings
	HasToken           bool `json:"hasToken"`
	AutostartEffective bool `json:"autostartEffective"`
}

func (a *App) settingsView(s config.Settings) SettingsView {
	return SettingsView{Settings: s, HasToken: a.cfg.HasToken(), AutostartEffective: a.autostartEffective(s.Autostart)}
}

// autostartEffective reads the real autostart state; platforms that cannot
// report it are assumed to match the stored preference.
func (a *App) autostartEffective(stored bool) bool {
	r, ok := a.plat.(AutostartReader)
	if !ok {
		return stored
	}
	on, err := r.AutostartEnabled()
	if err != nil {
		log.Printf("read autostart: %v", err)
		return false
	}
	return on
}

// SaveResult is the saveSettings result: Settings plus optional localized
// warnings (the settings were saved; the UI shows warnings as toasts).
type SaveResult struct {
	SettingsView
	Warnings []string `json:"warnings,omitempty"`
}

func (a *App) testConnection(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		JiraURL string  `json:"jiraUrl"`
		Token   *string `json:"token"`
	}
	if err := a.decode(params, &p); err != nil {
		return nil, err
	}
	lang := a.Language()
	type result struct {
		OK          bool   `json:"ok"`
		DisplayName string `json:"displayName,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	base := config.NormalizeURL(p.JiraURL)
	if !config.ValidateURL(base) {
		return result{Error: T(lang, "err_enter_url")}, nil
	}
	tok := ""
	if p.Token != nil {
		tok = strings.TrimSpace(*p.Token)
	}
	if tok == "" {
		// The stored token is only ever sent to the saved Jira origin
		// (scheme+host+port), so it cannot be exfiltrated to another server
		// or downgraded to plain http.
		if !a.cfg.HasToken() || !config.SameOrigin(base, a.cfg.Get().JiraURL) {
			return result{Error: T(lang, "err_enter_token")}, nil
		}
		tok = a.cfg.Token()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := jira.New(base, tok, a.plat.Transport()).Myself(ctx)
	if err != nil {
		return result{Error: Localize(err, lang).Message}, nil
	}
	return result{OK: true, DisplayName: u.DisplayName}, nil
}

func (a *App) saveSettings(params json.RawMessage) (any, error) {
	old := a.cfg.Get()
	oldToken := a.cfg.Token()
	in := struct {
		config.Settings
		Token *string `json:"token"`
	}{Settings: old.Clone()}
	if err := json.Unmarshal(params, &in); err != nil {
		// A non-number loadFactor is a field error, not a malformed call.
		if fe := loadFactorTypeError(params, in.Language); fe != nil {
			return nil, &Error{Code: CodeValidation, Message: T(in.Language, "errInvalidSettings"), Fields: []config.FieldError{*fe}}
		}
		return nil, &Error{Code: CodeBadParams, Message: T(a.Language(), "err_bad_params") + ": " + err.Error()}
	}
	if in.Token != nil && strings.TrimSpace(*in.Token) == "" {
		in.Token = nil // empty token field means "keep the stored one"
	}
	newURL := config.NormalizeURL(in.JiraURL)
	// The stored token is only ever sent to the origin it was saved with: a
	// new scheme/host/port (also after the URL was cleared) needs the token
	// entered again.
	if in.Token == nil && oldToken != "" && newURL != "" && !config.SameOrigin(newURL, old.JiraURL) {
		lang := in.Language
		return nil, &Error{Code: CodeValidation, Message: T(lang, "errInvalidSettings"),
			Fields: []config.FieldError{{Field: "token", Message: T(lang, "errTokenForNewHost")}}}
	}
	if fe := loadFactorTypeError(params, in.Language); fe != nil {
		return nil, &Error{Code: CodeValidation, Message: T(in.Language, "errInvalidSettings"), Fields: []config.FieldError{*fe}}
	}
	saved, fieldErrs, err := a.cfg.Save(in.Settings, in.Token)
	if errors.Is(err, config.ErrValidation) {
		return nil, &Error{Code: CodeValidation, Message: T(in.Language, "errInvalidSettings"), Fields: fieldErrs}
	}
	if err != nil {
		log.Printf("save settings: %v", err)
		return nil, &Error{Code: CodeInternal, Message: T(in.Language, "err_save_failed") + ": " + err.Error()}
	}
	var warnings []string
	if strings.HasPrefix(strings.ToLower(saved.JiraURL), "http://") {
		warnings = append(warnings, T(saved.Language, "warn_http"))
	}
	connChanged := saved.JiraURL != old.JiraURL || a.cfg.Token() != oldToken
	boardChanged := saved.BoardJQL != old.BoardJQL // the "mine" board query
	if connChanged {
		a.invalidateCaches()
	} else if boardChanged {
		a.dropCache(cacheBoard, cacheSprintGroups)
	}
	// Re-apply also when the registry disagrees with the stored preference
	// (e.g. an earlier write failed).
	if saved.Autostart != old.Autostart || a.autostartEffective(saved.Autostart) != saved.Autostart {
		if err := a.plat.SetAutostart(saved.Autostart); err != nil {
			log.Printf("autostart: %v", err)
			warnings = append(warnings, T(saved.Language, "warn_autostart"))
		}
	}
	a.plat.SettingsChanged(saved, a.Paused())
	a.sched.Poke()
	a.emitState()
	if connChanged {
		a.goBackground("refresh user", func(ctx context.Context) { a.refreshUser(ctx) })
	}
	if connChanged || boardChanged {
		a.emit("board", struct{}{}) // the cached board was dropped above
	}
	return SaveResult{SettingsView: a.settingsView(saved), Warnings: warnings}, nil
}

// loadFactorTypeError returns a field error when params carry a loadFactor
// that is not a JSON number (e.g. a string or null).
func loadFactorTypeError(params json.RawMessage, lang string) *config.FieldError {
	var raw map[string]json.RawMessage
	if json.Unmarshal(params, &raw) != nil {
		return nil
	}
	v, ok := raw["loadFactor"]
	if !ok {
		return nil
	}
	var n float64
	if t := strings.TrimSpace(string(v)); t != "null" && json.Unmarshal(v, &n) == nil {
		return nil
	}
	return &config.FieldError{Field: "loadFactor", Message: config.RangeMessage(lang, 0, 1)}
}

// --- UI preferences ----------------------------------------------------------

// MaxUIPrefsBytes bounds the stored UI preferences (compact JSON).
const MaxUIPrefsBytes = 32 * 1024

// uiPrefs returns the stored UI preferences object ({} if none).
func (a *App) uiPrefs() json.RawMessage {
	p := a.st.Snapshot().UIPrefs
	if len(p) == 0 {
		return json.RawMessage(`{}`)
	}
	return p
}

// setUIPrefs replaces the stored UI preferences with raw, which must be a
// JSON object of at most MaxUIPrefsBytes (compact). Contents are never logged.
func (a *App) setUIPrefs(raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if t := strings.TrimSpace(string(raw)); !strings.HasPrefix(t, "{") || json.Unmarshal(raw, &obj) != nil {
		return a.newErr(CodeBadParams, "err_prefs_object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return a.newErr(CodeBadParams, "err_prefs_object")
	}
	if buf.Len() > MaxUIPrefsBytes {
		return a.newErr(CodeBadParams, "err_prefs_too_large", MaxUIPrefsBytes/1024)
	}
	prefs := json.RawMessage(buf.Bytes())
	if err := a.st.Update(func(tx state.Tx) { tx.UIPrefs = prefs }); err != nil {
		log.Printf("save ui prefs: %v", err)
		return &Error{Code: CodeInternal, Message: T(a.Language(), "err_save_failed") + ": " + err.Error()}
	}
	return nil
}

// --- notifications ---------------------------------------------------------

func (a *App) markRead(ids *[]string) (any, error) {
	var want map[string]bool
	if ids != nil {
		want = make(map[string]bool, len(*ids))
		for _, id := range *ids {
			want[id] = true
		}
	}
	unread := 0
	if err := a.st.Update(func(tx state.Tx) {
		for i := range tx.Notifications {
			if want == nil || want[tx.Notifications[i].ID] {
				tx.Notifications[i].Read = true
			}
		}
		unread = tx.Data.Unread()
	}); err != nil {
		log.Printf("mark notifications read: %v", err)
	}
	a.emitState()
	return map[string]int{"unread": unread}, nil
}

// Paused reports whether tray balloons are paused.
func (a *App) Paused() bool { return a.st.Snapshot().NotificationsPaused }

// SetPaused pauses or resumes tray balloons.
func (a *App) SetPaused(p bool) {
	if err := a.st.Update(func(tx state.Tx) { tx.NotificationsPaused = p }); err != nil {
		log.Printf("save pause state: %v", err)
	}
	a.plat.SettingsChanged(a.cfg.Get(), p)
}

// Language returns the UI language from the settings.
func (a *App) Language() string { return a.cfg.Get().Language }

// --- urls --------------------------------------------------------------------

// URLAllowed reports whether raw is an http(s) URL under the Jira base URL.
func URLAllowed(raw, base string) bool {
	if base == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Host == "" {
		return false
	}
	b, err := url.Parse(base)
	if err != nil || !strings.EqualFold(u.Host, b.Host) {
		return false
	}
	bp := strings.TrimRight(b.Path, "/")
	return bp == "" || u.Path == bp || strings.HasPrefix(u.Path, bp+"/")
}

func (a *App) openURL(raw string) error {
	// The only exception to "Jira only": the Jiraffe release pages.
	if !URLAllowed(raw, a.cfg.Get().JiraURL) && !update.ReleasePageAllowed(raw) {
		return a.newErr(CodeForbiddenURL, "err_forbidden_url")
	}
	if err := a.plat.OpenURL(raw); err != nil {
		log.Printf("open url: %v", err)
		return a.newErr(CodeInternal, "err_open_failed")
	}
	return nil
}

// OpenNotificationURL opens a URL from a balloon (validated like openUrl).
func (a *App) OpenNotificationURL(raw string) {
	if err := a.openURL(raw); err != nil {
		log.Printf("open balloon url: %v", err)
	}
}
