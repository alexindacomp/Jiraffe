// Package worklog contains the pure daily worklog logic: rebuilding the
// intervals an issue spent "in progress", clipping them to today's working
// hours, splitting parallel time between issues and formatting durations.
//
// It is a faithful port of /home/alex/jira-worklog/worklog.py (proven against
// the real Jira), extended with the calendar rules from SPEC.md. It has no OS
// or network dependencies.
package worklog

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MinSeconds is the smallest amount of time worth logging.
const MinSeconds = 60

// Interval is a half-open time range [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// Seconds returns the length of the interval in seconds.
func (i Interval) Seconds() float64 { return i.End.Sub(i.Start).Seconds() }

// TotalSeconds sums the lengths of the intervals.
func TotalSeconds(spans []Interval) float64 {
	var t float64
	for _, s := range spans {
		t += s.Seconds()
	}
	return t
}

// StatusChange is one status transition from the issue changelog.
type StatusChange struct {
	At   time.Time
	From string // fromString
	To   string // toString
}

// StatusSet is a case-insensitive set of status names.
type StatusSet map[string]struct{}

// NewStatusSet builds a StatusSet from status names.
func NewStatusSet(names []string) StatusSet {
	s := make(StatusSet, len(names))
	for _, n := range names {
		if n = NormStatus(n); n != "" {
			s[n] = struct{}{}
		}
	}
	return s
}

// Has reports whether name is in the set (case-insensitive).
func (s StatusSet) Has(name string) bool {
	_, ok := s[NormStatus(name)]
	return ok
}

// NormStatus is the comparison key of a status name: lower case without
// spaces, so "In Testing" matches a Jira status "InTesting" (v0.8.4).
func NormStatus(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), ""))
}

// InProgressIntervals rebuilds the intervals the issue spent in an in-progress
// status. The initial status is the first change's From, or the current status
// when there are no changes; the initial interval starts at created (a zero
// created means "unknown", as in the Python original). An interval that is
// still open ends at now.
func InProgressIntervals(created time.Time, current string, changes []StatusChange, inProgress StatusSet, now time.Time) []Interval {
	sorted := make([]StatusChange, len(changes))
	copy(sorted, changes)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })

	cur := current
	if len(sorted) > 0 {
		cur = sorted[0].From
	}
	since, hasSince := created, !created.IsZero()
	var out []Interval
	for _, ch := range sorted {
		if inProgress.Has(cur) && hasSince {
			out = append(out, Interval{since, ch.At})
		}
		cur, since, hasSince = ch.To, ch.At, true
	}
	if inProgress.Has(cur) && hasSince {
		out = append(out, Interval{since, now})
	}
	return out
}

// Day describes today's countable working time.
type Day struct {
	Start      time.Time // workday start
	End        time.Time // min(now, workday end)
	HasLunch   bool
	LunchStart time.Time
	LunchEnd   time.Time
}

// Clip clips spans to the working hours and cuts out the lunch break
// (work_time in the Python original). Empty pieces are dropped.
func (d Day) Clip(spans []Interval) []Interval {
	var out []Interval
	for _, sp := range spans {
		s, e := maxTime(sp.Start, d.Start), minTime(sp.End, d.End)
		if !d.HasLunch {
			if e.After(s) {
				out = append(out, Interval{s, e})
			}
			continue
		}
		pieces := [2]Interval{{s, minTime(e, d.LunchStart)}, {maxTime(s, d.LunchEnd), e}}
		for _, p := range pieces {
			if p.End.After(p.Start) {
				out = append(out, p)
			}
		}
	}
	return out
}

// Allocate splits today's time between issues and keeps the day within cap
// seconds (allocate in the Python original).
//
// At every elementary time slice the time is divided equally between the
// issues in progress during that slice, so parallel issues never add up to
// more than the wall-clock time. logged is what I already logged today per
// issue. If the remaining work exceeds cap minus already logged, everything is
// scaled down proportionally. Returns the share per issue (seconds) and the
// seconds to log, rounded down to whole minutes.
func Allocate(spans map[string][]Interval, logged map[string]int64, capSeconds int64) (map[string]float64, map[string]int64) {
	share := Shares(spans)
	todo := make(map[string]float64, len(spans))
	var already, need float64
	for k := range spans {
		l := float64(logged[k])
		already += l
		todo[k] = math.Max(0, share[k]-l)
		need += todo[k]
	}
	budget := math.Max(0, float64(capSeconds)-already)
	scaleInto(todo, need, budget)
	out := make(map[string]int64, len(todo))
	for k, v := range todo {
		out[k] = floorMinutes(v)
	}
	return share, out
}

// Shares divides the wall-clock time of the spans between the issues: at
// every elementary time slice equally between the issues active in it.
func Shares(spans map[string][]Interval) map[string]float64 {
	pointSet := map[int64]struct{}{}
	for _, sp := range spans {
		for _, iv := range sp {
			pointSet[iv.Start.UnixNano()] = struct{}{}
			pointSet[iv.End.UnixNano()] = struct{}{}
		}
	}
	points := make([]int64, 0, len(pointSet))
	for p := range pointSet {
		points = append(points, p)
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })

	share := make(map[string]float64, len(spans))
	for k := range spans {
		share[k] = 0
	}
	active := make([]string, 0, len(spans))
	for i := 0; i+1 < len(points); i++ {
		a, b := points[i], points[i+1]
		active = active[:0]
		for k, sp := range spans {
			for _, iv := range sp {
				if iv.Start.UnixNano() <= a && b <= iv.End.UnixNano() {
					active = append(active, k)
					break
				}
			}
		}
		if len(active) == 0 {
			continue
		}
		part := float64(b-a) / float64(time.Second) / float64(len(active))
		for _, k := range active {
			share[k] += part
		}
	}
	return share
}

// scaleInto scales the values down proportionally when their sum (need)
// exceeds budget.
func scaleInto(todo map[string]float64, need, budget float64) {
	if need > budget {
		for k, v := range todo {
			if need > 0 {
				todo[k] = v * budget / need
			}
		}
	}
}

// floorMinutes rounds seconds down to whole minutes. The epsilon guards
// against float noise such as 7199.9999999 turning a clean 2h into 1h 59m.
func floorMinutes(v float64) int64 {
	return int64(math.Floor((v+1e-6)/60)) * 60
}

// Candidate is an issue with today's clipped status spans and/or fixed time.
type Candidate struct {
	Key     string
	Summary string
	Status  string
	Spans   []Interval // clipped to today, chronological (status roles)
	Logged  int64      // seconds I already logged today
	// Roles I hold on the issue that day (RoleOrder order).
	Roles []string
	// Comments is the number of my comments that day.
	Comments int
	// Fixed is the effective fixed time (author / commenter roles).
	Fixed FixedPart
	// FixedStart is when a fixed-only worklog starts (creation / first
	// comment, already clamped into the workday).
	FixedStart time.Time
	// Worked are the status roles whose own statuses had time that day
	// (v0.8.4: the description names only these; nil = all status roles).
	Worked []string
}

// hasFixedRole reports whether the candidate is author or commenter.
func (c Candidate) hasFixedRole() bool {
	return c.Fixed.CreateSource != "" || c.Fixed.CommentSource != ""
}

// Planned is the allocation result for one candidate.
type Planned struct {
	Candidate
	Share   float64 // seconds of the status time attributed to the issue
	Todo    int64   // seconds to log now (whole minutes; < MinSeconds means nothing)
	Started time.Time
}

// Plan allocates the day's time between candidates, preserving their order;
// capSeconds is the cap minus my time logged that day on other issues.
// Candidates without spans and without a fixed role are ignored, as are
// repeated keys (the first one wins).
//
// The budget is capSeconds minus what is already logged on the candidates.
// Fixed time comes first: per issue max(0, fixed − logged), all scaled down
// proportionally when they exceed the budget (status time then gets
// nothing). The status shares get the rest: per issue max(0, share −
// max(0, logged − fixed)), scaled into what the fixed time left. Todo is
// the sum, rounded down to whole minutes, so re-running a day never logs
// twice and the cap is never exceeded.
func Plan(cands []Candidate, capSeconds int64) []Planned {
	spans := make(map[string][]Interval, len(cands))
	seen := make(map[string]bool, len(cands))
	kept := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		if len(c.Spans) == 0 && !c.hasFixedRole() || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		if len(c.Spans) > 0 {
			spans[c.Key] = c.Spans
		}
		kept = append(kept, c)
	}
	share := Shares(spans)

	var already, fixedNeed float64
	fixedTodo := make(map[string]float64, len(kept))
	for _, c := range kept {
		already += float64(c.Logged)
		fixedTodo[c.Key] = math.Max(0, float64(c.Fixed.Seconds()-c.Logged))
		fixedNeed += fixedTodo[c.Key]
	}
	budget := math.Max(0, float64(capSeconds)-already)
	scaleInto(fixedTodo, fixedNeed, budget)
	rest := math.Max(0, budget-fixedNeed)

	statusTodo := make(map[string]float64, len(kept))
	var statusNeed float64
	for _, c := range kept {
		over := math.Max(0, float64(c.Logged-c.Fixed.Seconds())) // logged beyond the fixed part
		statusTodo[c.Key] = math.Max(0, share[c.Key]-over)
		statusNeed += statusTodo[c.Key]
	}
	scaleInto(statusTodo, statusNeed, rest)

	out := make([]Planned, 0, len(kept))
	for _, c := range kept {
		started := c.FixedStart
		if len(c.Spans) > 0 {
			started = c.Spans[0].Start
		}
		out = append(out, Planned{Candidate: c, Share: share[c.Key], Todo: floorMinutes(fixedTodo[c.Key] + statusTodo[c.Key]), Started: started})
	}
	return out
}

// FormatDuration formats seconds the Jira way, e.g. "1w 2d 3h 15m", using
// hoursPerDay hours per day and 5 days per week. Zero or negative is "0m";
// any positive amount below a minute is "1m".
func FormatDuration(seconds float64, hoursPerDay float64) string {
	if seconds <= 0 {
		return "0m"
	}
	m := int64(math.Round(seconds / 60))
	if m == 0 {
		return "1m"
	}
	day := int64(math.Round(hoursPerDay * 60))
	if day <= 0 {
		day = 8 * 60
	}
	units := []struct {
		name string
		size int64
	}{{"w", day * 5}, {"d", day}, {"h", 60}, {"m", 1}}
	var parts []string
	for _, u := range units {
		if m >= u.size {
			parts = append(parts, strconv.FormatInt(m/u.size, 10)+u.name)
			m %= u.size
		}
	}
	return strings.Join(parts, " ")
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
