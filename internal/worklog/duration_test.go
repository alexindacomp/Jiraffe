package worklog

import "testing"

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		seconds float64
		norm    string
		ok      bool
	}{
		{"1w 2d 3h 30m", (5*9+2*9+3)*3600 + 30*60, "1w 2d 3h 30m", true},
		{"2h30m", 2.5 * 3600, "2h 30m", true},
		{" 1.5H ", 1.5 * 3600, "1.5h", true},
		{"1d", 9 * 3600, "1d", true},
		{"30m 1h", 1.5 * 3600, "30m 1h", true},
		{"0m", 0, "0m", true},
		{"", 0, "", false},
		{"1", 0, "", false},
		{"h", 0, "", false},
		{"1x", 0, "", false},
		{"1h 2h", 0, "", false},
		{"1..5h", 0, "", false},
		{".h", 0, "", false},
		{"-1h", 0, "", false},
		{"1h,30m", 0, "", false},
		{"99999999999w", 0, "", false},
	}
	for _, tc := range tests {
		sec, norm, err := ParseDuration(tc.in, 9)
		if (err == nil) != tc.ok || tc.ok && (sec != tc.seconds || norm != tc.norm) {
			t.Errorf("ParseDuration(%q) = %v, %q, %v", tc.in, sec, norm, err)
		}
	}
	if sec, _, _ := ParseDuration("1d", 0); sec != 8*3600 {
		t.Errorf("default hours per day: %v", sec)
	}
}
