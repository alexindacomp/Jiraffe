package winapi

import "strings"

// autostartPointsTo reports whether a Run command line (e.g.
// `"C:\Apps\jiraffe.exe" --minimized`) starts exe. Windows paths compare
// case-insensitively; both separators are accepted.
func autostartPointsTo(cmd, exe string) bool {
	cmd = strings.TrimSpace(cmd)
	var path string
	if strings.HasPrefix(cmd, `"`) {
		end := strings.IndexByte(cmd[1:], '"')
		if end < 0 {
			return false
		}
		path = cmd[1 : 1+end]
	} else {
		path, _, _ = strings.Cut(cmd, " ")
	}
	norm := func(p string) string { return strings.ReplaceAll(strings.TrimSpace(p), "/", `\`) }
	return path != "" && strings.EqualFold(norm(path), norm(exe))
}

// startupApprovedValue interprets the StartupApproved\Run REG_BINARY value
// written by Task Manager / Settings > Startup Apps: first byte 0x02 or 0x06
// means enabled, anything else (typically 0x03/0x07) means disabled by the
// user. An empty value is treated as enabled.
func startupApprovedValue(b []byte) bool {
	return len(b) == 0 || b[0] == 0x02 || b[0] == 0x06
}
