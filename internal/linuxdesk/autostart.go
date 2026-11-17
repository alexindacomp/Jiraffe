//go:build linux

package linuxdesk

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// autostartFile is the XDG autostart entry (~/.config/autostart).
func autostartFile() string {
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "autostart", "jiraffe.desktop")
}

// execQuote quotes an Exec argument (Desktop Entry spec): inside double
// quotes ", `, $ and \ are escaped with a backslash, and the value escapes
// every backslash once more.
func execQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`).Replace(s)
	return `"` + strings.ReplaceAll(r, `\`, `\\`) + `"`
}

// AutostartEntry is the content of the autostart entry for exe.
func AutostartEntry(exe string) string {
	return "[Desktop Entry]\nType=Application\nName=Jiraffe\nComment=Jira in the tray\n" +
		"Exec=" + execQuote(exe) + " --minimized\nIcon=jiraffe\nTerminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
}

// SetAutostart writes or removes the autostart entry for exe.
func SetAutostart(exe string, enabled bool) error {
	path := autostartFile()
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(AutostartEntry(exe)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AutostartEnabled reports whether the entry exists, is not disabled and
// starts exe.
func AutostartEnabled(exe string) (bool, error) {
	b, err := os.ReadFile(autostartFile())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return entryStarts(b, exe), nil
}

func entryStarts(b []byte, exe string) bool {
	starts := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Hidden", "X-GNOME-Autostart-enabled":
			if hidden := strings.TrimSpace(k) == "Hidden"; hidden == (strings.TrimSpace(v) == "true") {
				return false
			}
		case "Exec":
			starts = strings.HasPrefix(strings.TrimSpace(v), execQuote(exe)+" ") || strings.TrimSpace(v) == execQuote(exe)
		}
	}
	return starts
}
