package worklog

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrDuration reports a text that is not a Jira duration.
var ErrDuration = errors.New("not a Jira duration")

// maxDurationSeconds bounds parsed durations (about 20 years of work).
const maxDurationSeconds = 1e10

// ParseDuration parses a Jira duration such as "1w 2d 3h 30m", "2h30m" or
// "1.5h" (each unit w/d/h/m at most once, any order, case-insensitive;
// 1w = 5d, 1d = hoursPerDay hours). It returns the seconds and the text
// normalized as Jira reads it ("1w 2d 3h 30m": lower case, one space
// between tokens, numbers as written).
func ParseDuration(s string, hoursPerDay float64) (float64, string, error) {
	if hoursPerDay <= 0 {
		hoursPerDay = 8
	}
	size := map[byte]float64{'w': 5 * hoursPerDay * 3600, 'd': hoursPerDay * 3600, 'h': 3600, 'm': 60}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 64 {
		return 0, "", ErrDuration
	}
	var total float64
	var tokens []string
	seen := map[byte]bool{}
	for i := 0; i < len(s); {
		if s[i] == ' ' {
			i++
			continue
		}
		start, dots := i, 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			if s[i] == '.' {
				dots++
			}
			i++
		}
		num := s[start:i]
		if num == "" || num == "." || dots > 1 || i >= len(s) {
			return 0, "", ErrDuration
		}
		unit := s[i]
		if _, ok := size[unit]; !ok || seen[unit] {
			return 0, "", ErrDuration
		}
		i++
		seen[unit] = true
		v, err := strconv.ParseFloat(num, 64)
		if err != nil || math.IsInf(v, 0) {
			return 0, "", ErrDuration
		}
		total += v * size[unit]
		tokens = append(tokens, num+string(unit))
	}
	if total > maxDurationSeconds {
		return 0, "", ErrDuration
	}
	return total, strings.Join(tokens, " "), nil
}
