//go:build linux

package linuxdesk

import (
	"bytes"
	"os"
	"path/filepath"
)

// MenuEntry is the application menu entry for exe (~/.local/share/applications).
func MenuEntry(exe string) string {
	return "[Desktop Entry]\nType=Application\nName=Jiraffe\nComment=Jira in the tray: board, sprints, worklog\n" +
		"Exec=" + execQuote(exe) + "\nIcon=jiraffe\nTerminal=false\nCategories=Office;ProjectManagement;\n" +
		"StartupWMClass=jiraffe\nStartupNotify=true\n"
}

// EnsureDesktopIntegration installs the menu entry and the icon for exe in
// the user's folders, rewriting them only when they differ (the binary may
// have moved). The window and the tray find the icon by the name "jiraffe".
func EnsureDesktopIntegration(exe string, iconPNG []byte) error {
	data := xdg("XDG_DATA_HOME", ".local/share")
	if err := writeIfChanged(filepath.Join(data, "icons", "hicolor", "256x256", "apps", "jiraffe.png"), iconPNG); err != nil {
		return err
	}
	return writeIfChanged(filepath.Join(data, "applications", "jiraffe.desktop"), []byte(MenuEntry(exe)))
}

func writeIfChanged(path string, content []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
