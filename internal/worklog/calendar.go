package worklog

import "time"

// DateLayout is the layout of calendar dates in settings and runs.
const DateLayout = "2006-01-02"

// Skip reasons reported in Run.SkippedReason.
const (
	ReasonWeekend       = "weekend"
	ReasonVacation      = "vacation"
	ReasonHoliday       = "holiday"
	ReasonDisabled      = "disabled"
	ReasonNotConfigured = "not-configured"
)

// DateRange is an inclusive range of calendar dates ("2006-01-02").
type DateRange struct {
	From string
	To   string
}

// Calendar decides which days are working days.
type Calendar struct {
	WorkDays  []int // ISO weekdays, Mon=1 … Sun=7
	Vacations []DateRange
	Holidays  []string
}

// ISOWeekday returns the ISO weekday of t (Mon=1 … Sun=7).
func ISOWeekday(t time.Time) int {
	wd := int(t.Weekday())
	if wd == 0 {
		return 7
	}
	return wd
}

// Check reports whether the calendar day of t (in t's location) is a working
// day; if not, it returns the reason: "weekend", "holiday" or "vacation"
// (checked in that order).
func (c Calendar) Check(t time.Time) (bool, string) {
	wd := ISOWeekday(t)
	work := false
	for _, d := range c.WorkDays {
		if d == wd {
			work = true
			break
		}
	}
	if !work {
		return false, ReasonWeekend
	}
	date := t.Format(DateLayout)
	for _, h := range c.Holidays {
		if h == date {
			return false, ReasonHoliday
		}
	}
	for _, v := range c.Vacations {
		// ISO dates compare correctly as strings.
		if v.From <= date && date <= v.To {
			return false, ReasonVacation
		}
	}
	return true, ""
}
