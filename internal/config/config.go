// Package config holds the user settings: defaults, validation and JSON
// persistence. The Jira token is encrypted through a Protector (DPAPI on
// Windows) and never written in plain text.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultBoardJQL is the board query used when the setting is empty.
const DefaultBoardJQL = "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"

// Vacation is an inclusive date range.
type Vacation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Note string `json:"note"`
}

// Settings are the user-editable settings (camelCase JSON as in the contract).
type Settings struct {
	JiraURL            string     `json:"jiraUrl"`
	Language           string     `json:"language"`
	Autostart          bool       `json:"autostart"`
	WorklogEnabled     bool       `json:"worklogEnabled"`
	WorklogTime        string     `json:"worklogTime"`
	WorkdayStart       string     `json:"workdayStart"`
	WorkdayEnd         string     `json:"workdayEnd"`
	LunchStart         string     `json:"lunchStart"`
	LunchEnd           string     `json:"lunchEnd"`
	DailyCapHours      float64    `json:"dailyCapHours"`
	HoursPerDay        float64    `json:"hoursPerDay"`
	WorkDays           []int      `json:"workDays"`
	Vacations          []Vacation `json:"vacations"`
	Holidays           []string   `json:"holidays"`
	InProgressStatuses []string   `json:"inProgressStatuses"`
	NotifyComments     bool       `json:"notifyComments"`
	CommentPollMinutes int        `json:"commentPollMinutes"`
	BoardJQL           string     `json:"boardJql"`
}

// Defaults returns the default settings.
func Defaults() Settings {
	return Settings{
		Language:           "ru",
		Autostart:          true,
		WorklogEnabled:     true,
		WorklogTime:        "18:00",
		WorkdayStart:       "09:00",
		WorkdayEnd:         "18:00",
		LunchStart:         "13:00",
		LunchEnd:           "14:00",
		DailyCapHours:      8,
		HoursPerDay:        9,
		WorkDays:           []int{1, 2, 3, 4, 5},
		Vacations:          []Vacation{},
		Holidays:           []string{},
		InProgressStatuses: []string{"In Progress", "В работе", "В роботі"},
		NotifyComments:     true,
		CommentPollMinutes: 5,
		BoardJQL:           DefaultBoardJQL,
	}
}

// Clone returns a deep copy.
func (s Settings) Clone() Settings {
	c := s
	c.WorkDays = append([]int{}, s.WorkDays...)
	c.Vacations = append([]Vacation{}, s.Vacations...)
	c.Holidays = append([]string{}, s.Holidays...)
	c.InProgressStatuses = append([]string{}, s.InProgressStatuses...)
	return c
}

// FieldError is a validation error of one settings field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ParseClock parses "HH:MM" into minutes since midnight.
func ParseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil || len(strings.TrimSpace(s)) != 5 {
		return 0, fmt.Errorf("invalid time %q, expected HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// At returns the moment of clock "HH:MM" on the calendar day of day (in its
// location). The clock must be valid.
func At(day time.Time, clock string) time.Time {
	m, _ := ParseClock(clock)
	y, mo, d := day.Date()
	return time.Date(y, mo, d, m/60, m%60, 0, 0, day.Location())
}

// NormalizeURL trims spaces and trailing slashes of a Jira base URL.
func NormalizeURL(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// ValidateURL checks that s is an absolute http(s) URL without query/fragment.
// The empty string is not valid.
func ValidateURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// SameOrigin reports whether two URLs have the same scheme, host and
// effective port. The stored token is only ever sent to the origin it was
// saved for.
func SameOrigin(a, b string) bool {
	ua, err1 := url.Parse(NormalizeURL(a))
	ub, err2 := url.Parse(NormalizeURL(b))
	if err1 != nil || err2 != nil || ua.Host == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Hostname(), ub.Hostname()) &&
		effectivePort(ua) == effectivePort(ub)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return "443"
}

// Normalize trims and canonicalizes values in place (without validating).
func (s *Settings) Normalize() {
	s.JiraURL = NormalizeURL(s.JiraURL)
	s.Language = strings.ToLower(strings.TrimSpace(s.Language))
	for _, p := range []*string{&s.WorklogTime, &s.WorkdayStart, &s.WorkdayEnd, &s.LunchStart, &s.LunchEnd} {
		*p = strings.TrimSpace(*p)
	}
	s.BoardJQL = strings.TrimSpace(s.BoardJQL)
	if s.BoardJQL == "" {
		s.BoardJQL = DefaultBoardJQL
	}
	if s.WorkDays == nil {
		s.WorkDays = []int{}
	}
	sort.Ints(s.WorkDays)
	s.WorkDays = dedupInts(s.WorkDays)

	hol := make([]string, 0, len(s.Holidays))
	for _, h := range s.Holidays {
		if h = strings.TrimSpace(h); h != "" {
			hol = append(hol, h)
		}
	}
	sort.Strings(hol)
	s.Holidays = dedupStrings(hol)

	vac := make([]Vacation, 0, len(s.Vacations))
	for _, v := range s.Vacations {
		v.From, v.To, v.Note = strings.TrimSpace(v.From), strings.TrimSpace(v.To), strings.TrimSpace(v.Note)
		if v.To == "" {
			v.To = v.From
		}
		vac = append(vac, v)
	}
	sort.SliceStable(vac, func(i, j int) bool { return vac[i].From < vac[j].From })
	s.Vacations = vac

	st := make([]string, 0, len(s.InProgressStatuses))
	seen := map[string]bool{}
	for _, n := range s.InProgressStatuses {
		n = strings.TrimSpace(n)
		if n != "" && !seen[strings.ToLower(n)] {
			seen[strings.ToLower(n)] = true
			st = append(st, n)
		}
	}
	s.InProgressStatuses = st
}

// Validate returns per-field errors; messages are in s.Language (fallback en).
func (s Settings) Validate() []FieldError {
	lang := s.Language
	var errs []FieldError
	add := func(field, msgKey string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: msg(lang, msgKey, args...)})
	}

	if s.JiraURL != "" && !ValidateURL(s.JiraURL) {
		add("jiraUrl", "url")
	}
	if s.Language != "ru" && s.Language != "uk" && s.Language != "en" {
		add("language", "language")
	}
	clocks := map[string]int{}
	for _, f := range []struct {
		name, val string
		optional  bool
	}{
		{"worklogTime", s.WorklogTime, false},
		{"workdayStart", s.WorkdayStart, false},
		{"workdayEnd", s.WorkdayEnd, false},
		{"lunchStart", s.LunchStart, true},
		{"lunchEnd", s.LunchEnd, true},
	} {
		if f.optional && f.val == "" {
			continue
		}
		m, err := ParseClock(f.val)
		if err != nil {
			add(f.name, "clock")
			continue
		}
		clocks[f.name] = m
	}
	ws, okS := clocks["workdayStart"]
	we, okE := clocks["workdayEnd"]
	if okS && okE && ws >= we {
		add("workdayEnd", "endAfterStart")
	}
	// A run before the end of the workday would never log the rest of it.
	if wt, ok := clocks["worklogTime"]; ok && okE && wt < we {
		add("worklogTime", "worklogBeforeEnd")
	}
	if (s.LunchStart == "") != (s.LunchEnd == "") {
		if s.LunchStart == "" {
			add("lunchStart", "lunchBoth")
		} else {
			add("lunchEnd", "lunchBoth")
		}
	} else if ls, ok1 := clocks["lunchStart"]; ok1 {
		if le, ok2 := clocks["lunchEnd"]; ok2 && ls >= le {
			add("lunchEnd", "endAfterStart")
		}
	}
	if !(s.DailyCapHours >= 0.5 && s.DailyCapHours <= 24) {
		add("dailyCapHours", "range", 0.5, 24)
	}
	if !(s.HoursPerDay >= 1 && s.HoursPerDay <= 24) {
		add("hoursPerDay", "range", 1, 24)
	}
	if len(s.WorkDays) == 0 {
		add("workDays", "workDaysEmpty")
	}
	for _, d := range s.WorkDays {
		if d < 1 || d > 7 {
			add("workDays", "workDays")
			break
		}
	}
	for i, v := range s.Vacations {
		f, errF := time.Parse("2006-01-02", v.From)
		t, errT := time.Parse("2006-01-02", v.To)
		if errF != nil || errT != nil {
			add("vacations", "vacationDate", i+1)
		} else if t.Before(f) {
			add("vacations", "vacationOrder", i+1)
		}
	}
	for _, h := range s.Holidays {
		if _, err := time.Parse("2006-01-02", h); err != nil {
			add("holidays", "holiday", h)
			break
		}
	}
	if len(s.InProgressStatuses) == 0 {
		add("inProgressStatuses", "statuses")
	}
	if s.CommentPollMinutes < 1 || s.CommentPollMinutes > 60 {
		add("commentPollMinutes", "range", 1, 60)
	}
	if len(s.BoardJQL) > 2000 {
		add("boardJql", "jqlLong")
	}
	return errs
}

func dedupInts(a []int) []int {
	out := a[:0]
	for i, v := range a {
		if i == 0 || v != a[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func dedupStrings(a []string) []string {
	out := a[:0]
	for i, v := range a {
		if i == 0 || v != a[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Protector encrypts secrets at rest (DPAPI on Windows).
type Protector interface {
	Protect(plain []byte) ([]byte, error)
	Unprotect(blob []byte) ([]byte, error)
}

// fileFormat is the on-disk layout of config.json.
type fileFormat struct {
	Settings
	TokenEnc string `json:"tokenEnc,omitempty"`
}

// ErrValidation is returned by Store.Save together with field errors.
var ErrValidation = errors.New("invalid settings")

// Store holds the settings in memory and persists them to a JSON file.
// It is safe for concurrent use.
type Store struct {
	path string
	prot Protector

	mu    sync.RWMutex
	s     Settings
	token string
}

// Open loads settings from path. A missing file yields defaults. A corrupt
// file is moved aside to path+".bad" and defaults are used; the returned error
// then describes the problem but the Store is still usable.
func Open(path string, prot Protector) (*Store, error) {
	st := &Store{path: path, prot: prot, s: Defaults()}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("read config: %w", err)
	}
	ff := fileFormat{Settings: Defaults()}
	if err := json.Unmarshal(b, &ff); err != nil {
		_ = os.Rename(path, path+".bad")
		return st, fmt.Errorf("config %s is corrupt (moved to .bad, defaults used): %w", path, err)
	}
	ff.Settings.Normalize()
	if errs := ff.Settings.Validate(); len(errs) > 0 {
		// Keep what is valid; fall back to defaults for broken fields.
		ff.Settings = repair(ff.Settings, errs)
	}
	st.s = ff.Settings
	if ff.TokenEnc != "" {
		blob, err := base64.StdEncoding.DecodeString(ff.TokenEnc)
		if err == nil {
			var plain []byte
			plain, err = prot.Unprotect(blob)
			if err == nil {
				st.token = string(plain)
			}
		}
		if err != nil {
			return st, fmt.Errorf("decrypt token (re-enter it in settings): %w", err)
		}
	}
	return st, nil
}

// repair replaces fields that failed validation with their defaults.
func repair(s Settings, errs []FieldError) Settings {
	d := Defaults()
	for _, e := range errs {
		switch e.Field {
		case "jiraUrl":
			s.JiraURL = d.JiraURL
		case "language":
			s.Language = d.Language
		case "worklogTime":
			s.WorklogTime = d.WorklogTime
			if wt, err := ParseClock(s.WorklogTime); err == nil {
				if we, err := ParseClock(s.WorkdayEnd); err == nil && wt < we {
					s.WorklogTime = s.WorkdayEnd
				}
			}
		case "workdayStart", "workdayEnd":
			s.WorkdayStart, s.WorkdayEnd = d.WorkdayStart, d.WorkdayEnd
		case "lunchStart", "lunchEnd":
			s.LunchStart, s.LunchEnd = d.LunchStart, d.LunchEnd
		case "dailyCapHours":
			s.DailyCapHours = d.DailyCapHours
		case "hoursPerDay":
			s.HoursPerDay = d.HoursPerDay
		case "workDays":
			s.WorkDays = d.WorkDays
		case "vacations":
			s.Vacations = d.Vacations
		case "holidays":
			s.Holidays = d.Holidays
		case "inProgressStatuses":
			s.InProgressStatuses = d.InProgressStatuses
		case "commentPollMinutes":
			s.CommentPollMinutes = d.CommentPollMinutes
		case "boardJql":
			s.BoardJQL = d.BoardJQL
		}
	}
	return s
}

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s.Clone()
}

// Token returns the decrypted Jira token ("" if none).
func (st *Store) Token() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.token
}

// HasToken reports whether a token is stored.
func (st *Store) HasToken() bool { return st.Token() != "" }

// Configured reports whether Jira URL and token are set.
func (st *Store) Configured() bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.s.JiraURL != "" && st.token != ""
}

// Save validates and persists s. token == nil keeps the stored token.
// On validation failure it returns ErrValidation and the field errors.
func (st *Store) Save(s Settings, token *string) (Settings, []FieldError, error) {
	s = s.Clone()
	s.Normalize()
	if errs := s.Validate(); len(errs) > 0 {
		return Settings{}, errs, ErrValidation
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	newToken := st.token
	if token != nil {
		newToken = strings.TrimSpace(*token)
	}
	if err := st.write(s, newToken); err != nil {
		return Settings{}, nil, err
	}
	st.s, st.token = s, newToken
	return s.Clone(), nil, nil
}

func (st *Store) write(s Settings, token string) error {
	ff := fileFormat{Settings: s}
	if token != "" {
		blob, err := st.prot.Protect([]byte(token))
		if err != nil {
			return fmt.Errorf("encrypt token: %w", err)
		}
		ff.TokenEnc = base64.StdEncoding.EncodeToString(blob)
	}
	b, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return WriteFileAtomic(st.path, b)
}

// WriteFileAtomic writes data to a temp file next to path and renames it
// over path, creating the directory if needed.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// Flush to disk before the rename so a crash cannot leave an empty file.
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Override replaces the Jira URL and token in memory only (never persisted).
// Used by the headless preview mode with JIRAFFE_URL / JIRAFFE_TOKEN. A URL
// with a different origin and no token drops the stored token, so it is
// never sent to another server (the result is "not configured").
func (st *Store) Override(jiraURL, token string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	token = strings.TrimSpace(token)
	if jiraURL != "" {
		u := NormalizeURL(jiraURL)
		if token == "" && !SameOrigin(u, st.s.JiraURL) {
			st.token = ""
		}
		st.s.JiraURL = u
	}
	if token != "" {
		st.token = token
	}
}
