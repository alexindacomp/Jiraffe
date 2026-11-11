package worklog

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 18, 0, 0, 0, kyiv) }

func TestCalendar(t *testing.T) {
	weekdays := []int{1, 2, 3, 4, 5}
	tests := []struct {
		name   string
		cal    Calendar
		day    time.Time
		ok     bool
		reason string
	}{
		// Vacation 2026-11-16 (Mon) .. 2026-11-20 (Fri): both ends inclusive.
		{"day before vacation", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-11-16", "2026-11-20"}}}, day(2026, 11, 13), true, ""},
		{"vacation first day", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-11-16", "2026-11-20"}}}, day(2026, 11, 16), false, ReasonVacation},
		{"vacation last day", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-11-16", "2026-11-20"}}}, day(2026, 11, 20), false, ReasonVacation},
		{"day after vacation (Mon)", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-11-16", "2026-11-20"}}}, day(2026, 11, 23), true, ""},
		{"one-day vacation", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-11-11", "2026-11-11"}}}, day(2026, 11, 11), false, ReasonVacation},
		{"second of several vacations", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-06-01", "2026-06-05"}, {"2026-11-10", "2026-11-12"}}}, day(2026, 11, 11), false, ReasonVacation},

		// Vacation spanning the new year.
		{"new-year vacation, Dec 31", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-12-28", "2027-01-08"}}}, day(2026, 12, 31), false, ReasonVacation},
		{"new-year vacation, Jan 4", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-12-28", "2027-01-08"}}}, day(2027, 1, 4), false, ReasonVacation},
		{"new-year vacation, last day Jan 8", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-12-28", "2027-01-08"}}}, day(2027, 1, 8), false, ReasonVacation},
		{"after new-year vacation, Jan 11", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-12-28", "2027-01-08"}}}, day(2027, 1, 11), true, ""},
		{"before new-year vacation, Dec 25", Calendar{WorkDays: weekdays, Vacations: []DateRange{{"2026-12-28", "2027-01-08"}}}, day(2026, 12, 25), true, ""},

		// Holidays.
		{"holiday", Calendar{WorkDays: weekdays, Holidays: []string{"2026-12-25", "2027-01-01"}}, day(2027, 1, 1), false, ReasonHoliday},
		{"not a holiday", Calendar{WorkDays: weekdays, Holidays: []string{"2026-12-25", "2027-01-01"}}, day(2026, 12, 24), true, ""},
		{"holiday wins over vacation", Calendar{WorkDays: weekdays, Holidays: []string{"2026-12-25"}, Vacations: []DateRange{{"2026-12-21", "2026-12-31"}}}, day(2026, 12, 25), false, ReasonHoliday},
		{"weekend wins over holiday", Calendar{WorkDays: weekdays, Holidays: []string{"2026-11-14"}}, day(2026, 11, 14), false, ReasonWeekend},

		// Custom work week Sun–Thu.
		{"Sun–Thu: Sunday works", Calendar{WorkDays: []int{7, 1, 2, 3, 4}}, day(2026, 11, 15), true, ""},
		{"Sun–Thu: Thursday works", Calendar{WorkDays: []int{7, 1, 2, 3, 4}}, day(2026, 11, 12), true, ""},
		{"Sun–Thu: Friday off", Calendar{WorkDays: []int{7, 1, 2, 3, 4}}, day(2026, 11, 13), false, ReasonWeekend},
		{"Sun–Thu: Saturday off", Calendar{WorkDays: []int{7, 1, 2, 3, 4}}, day(2026, 11, 14), false, ReasonWeekend},
		{"no work days at all", Calendar{}, day(2026, 11, 11), false, ReasonWeekend},
		{"Mon–Fri: Wednesday works", Calendar{WorkDays: weekdays}, day(2026, 11, 11), true, ""},
		{"Mon–Fri: Saturday off", Calendar{WorkDays: weekdays}, day(2026, 11, 14), false, ReasonWeekend},
		{"Mon–Fri: Sunday off", Calendar{WorkDays: weekdays}, day(2026, 11, 15), false, ReasonWeekend},
		{"weekend-only worker: Saturday works", Calendar{WorkDays: []int{6, 7}}, day(2026, 11, 14), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := tc.cal.Check(tc.day)
			if ok != tc.ok || reason != tc.reason {
				t.Errorf("%s (%s): got (%v,%q), want (%v,%q)", tc.day.Format(DateLayout), tc.day.Weekday(), ok, reason, tc.ok, tc.reason)
			}
		})
	}
}

func TestCalendarUsesTheDateInTheTimesLocation(t *testing.T) {
	// 2026-11-12 00:30 in Kyiv is still 2026-11-11 in UTC: the local date decides.
	cal := Calendar{WorkDays: []int{1, 2, 3, 4, 5}, Holidays: []string{"2026-11-12"}}
	local := time.Date(2026, 11, 12, 0, 30, 0, 0, kyiv)
	if ok, reason := cal.Check(local); ok || reason != ReasonHoliday {
		t.Errorf("local: got (%v,%q)", ok, reason)
	}
	if ok, _ := cal.Check(local.UTC()); !ok {
		t.Error("the same instant in UTC is 2026-11-11, a working day")
	}
}

func TestISOWeekday(t *testing.T) {
	for i, want := range []int{1, 2, 3, 4, 5, 6, 7} { // 2026-11-09 is a Monday
		if got := ISOWeekday(time.Date(2026, 11, 9+i, 12, 0, 0, 0, kyiv)); got != want {
			t.Errorf("2026-11-%02d: got %d, want %d", 9+i, got, want)
		}
	}
}
