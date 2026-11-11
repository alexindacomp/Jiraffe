package winapi

import "testing"

func TestStartupApprovedValue(t *testing.T) {
	tests := []struct {
		b    []byte
		want bool
	}{
		{nil, true},
		{[]byte{0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, true},
		{[]byte{0x06, 0, 0, 0}, true},
		{[]byte{0x03, 0, 0, 0, 0x1a, 0x2b}, false}, // disabled in Task Manager
		{[]byte{0x07}, false},
		{[]byte{0x00}, false},
	}
	for _, tc := range tests {
		if got := startupApprovedValue(tc.b); got != tc.want {
			t.Errorf("startupApprovedValue(% x) = %v", tc.b, got)
		}
	}
}
