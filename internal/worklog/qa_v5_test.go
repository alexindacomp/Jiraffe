package worklog

import (
	"math/rand"
	"strings"
	"testing"
)

// QA: Jira duration edge cases (SPEC v0.5 FieldValue "estimate").
func TestV5QAParseDurationEdges(t *testing.T) {
	tests := []struct {
		in      string
		seconds float64
		norm    string
		ok      bool
	}{
		{"1W1D1H1M", (5*8+8+1)*3600 + 60, "1w 1d 1h 1m", true}, // upper case, no spaces
		{"1h    30m", 1.5 * 3600, "1h 30m", true},              // runs of spaces folded
		{"0.5d", 4 * 3600, "0.5d", true},
		{"2w", 2 * 5 * 8 * 3600, "2w", true},
		{"1h\t30m", 0, "", false}, // only spaces separate tokens
		{"1h\n30m", 0, "", false},
		{"1 h", 0, "", false}, // number and unit belong together
		{"1h 30", 0, "", false},
		{"h1", 0, "", false},
		{"1s", 0, "", false}, // seconds are not a Jira estimate unit
		{"1y", 0, "", false},
		{"1e2h", 0, "", false},
		{"+1h", 0, "", false},
		{"1,5h", 0, "", false},
		{"1.2.3h", 0, "", false},
		{"1d 1D", 0, "", false},                                // each unit at most once, also across case
		{"１h", 0, "", false},                                   // full-width digit
		{strings.Repeat("1", 63) + "m", 0, "", false},          // within 64 chars but absurd
		{strings.Repeat(" ", 70) + "1h", 1 * 3600, "1h", true}, // trimmed before the length check
		{"1h" + strings.Repeat(" ", 63), 1 * 3600, "1h", true},
	}
	for _, tc := range tests {
		sec, norm, err := ParseDuration(tc.in, 8)
		if (err == nil) != tc.ok || tc.ok && (sec != tc.seconds || norm != tc.norm) {
			t.Errorf("ParseDuration(%q) = %v, %q, %v; want %v, %q, ok=%v", tc.in, sec, norm, err, tc.seconds, tc.norm, tc.ok)
		}
	}
	// Normalizing is idempotent: the normalized text parses to the same.
	for _, in := range []string{"1W 2d 3H 30m", "2h30m", "1.5h", "30m 1h"} {
		s1, n1, err1 := ParseDuration(in, 7.5)
		s2, n2, err2 := ParseDuration(n1, 7.5)
		if err1 != nil || err2 != nil || s1 != s2 || n1 != n2 {
			t.Errorf("%q → %q → %q (%v, %v)", in, n1, n2, err1, err2)
		}
	}
}

// Any input terminates and yields either an error or a normalized text of
// unit tokens that parses again.
func TestV5QAParseDurationRandom(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	alphabet := []byte("0123456789.wdhmWDHMx ,-\t")
	for i := 0; i < 20000; i++ {
		b := make([]byte, r.Intn(20))
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		sec, norm, err := ParseDuration(string(b), 8)
		if err != nil {
			continue
		}
		if sec < 0 || norm == "" || strings.ContainsAny(norm, "WDHM\t,-x") || strings.Contains(norm, "  ") {
			t.Fatalf("%q → %v %q", b, sec, norm)
		}
		if s2, n2, err := ParseDuration(norm, 8); err != nil || s2 != sec || n2 != norm {
			t.Fatalf("%q → %q does not parse to the same (%v %q %v)", b, norm, s2, n2, err)
		}
	}
}
