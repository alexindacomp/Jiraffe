package winapi

import "testing"

func TestAutostartPointsTo(t *testing.T) {
	exe := `C:\Users\me\Apps\Jiraffe\jiraffe.exe`
	tests := []struct {
		cmd  string
		want bool
	}{
		{`"C:\Users\me\Apps\Jiraffe\jiraffe.exe" --minimized`, true},
		{`"c:\users\ME\apps\jiraffe\JIRAFFE.EXE" --minimized`, true},
		{`C:\Users\me\Apps\Jiraffe\jiraffe.exe --minimized`, true},
		{`"C:/Users/me/Apps/Jiraffe/jiraffe.exe"`, true},
		{`"C:\Old\jiraffe.exe" --minimized`, false},
		{`"C:\Users\me\Apps\Jiraffe\jiraffe.exe --minimized`, false},
		{``, false},
	}
	for _, tc := range tests {
		if got := autostartPointsTo(tc.cmd, exe); got != tc.want {
			t.Errorf("autostartPointsTo(%q) = %v", tc.cmd, got)
		}
	}
}
