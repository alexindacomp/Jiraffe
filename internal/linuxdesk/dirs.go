//go:build linux

package linuxdesk

import (
	"os"
	"path/filepath"
)

// AppDir is the folder name under the XDG base folders. It differs from the
// lower-case tool folders on purpose.
const AppDir = "Jiraffe"

// xdg returns $env, else ~/fallback.
func xdg(env, fallback string) string {
	if d := os.Getenv(env); filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "jiraffe-"+os.Getenv("USER"))
	}
	return filepath.Join(home, fallback)
}

// ConfigDir holds config.json, state.json and the log (~/.config/Jiraffe).
func ConfigDir() string { return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), AppDir) }

// DataDir holds the web view's storage (~/.local/share/Jiraffe).
func DataDir() string { return filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), AppDir) }

// CacheDir holds downloads and caches (~/.cache/Jiraffe).
func CacheDir() string { return filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), AppDir) }
