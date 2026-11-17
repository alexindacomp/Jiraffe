#!/bin/sh
# Installs or updates Jiraffe for the current user on Ubuntu (22.04+) / Debian:
#
#   curl -fsSL https://github.com/alexindacomp/Jiraffe/releases/latest/download/install.sh | sh
#
# Downloads the latest jiraffe-linux-amd64, checks its SHA-256, installs it to
# ~/.local/bin/jiraffe (no sudo) and starts it; on its first start Jiraffe adds
# itself to the application menu. Later versions are installed by the app
# itself (signed releases only).
#
#   sh install.sh --uninstall    removes the program, its menu entry and autostart
#                                (settings in ~/.config/Jiraffe are kept)
set -eu

REPO="https://github.com/alexindacomp/Jiraffe/releases/latest/download"
ASSET="jiraffe-linux-amd64"
BIN_DIR="${XDG_BIN_HOME:-$HOME/.local/bin}"
DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
CONF="${XDG_CONFIG_HOME:-$HOME/.config}"
EXE="$BIN_DIR/jiraffe"

say() { printf '%s\n' "$*"; }
die() { printf 'Error: %s\n' "$*" >&2; exit 1; }

if [ "${1:-}" = "--uninstall" ]; then
  pkill -x jiraffe 2>/dev/null || true
  rm -f "$EXE" "$EXE.old" "$EXE.new" "$DATA/applications/jiraffe.desktop" \
    "$DATA/icons/hicolor/256x256/apps/jiraffe.png" "$CONF/autostart/jiraffe.desktop"
  say "Jiraffe removed. Settings are kept in $CONF/Jiraffe."
  exit 0
fi

[ "$(uname -s)" = Linux ] || die "this installer is for Linux"
[ "$(uname -m)" = x86_64 ] || die "only x86-64 builds are published"
command -v curl >/dev/null 2>&1 || die "curl is required: sudo apt install curl"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required (coreutils)"

# Runtime libraries (present on Ubuntu Desktop; minimal installs may lack them).
LDCONFIG="$(command -v ldconfig || echo /sbin/ldconfig)"
missing=""
for lib in libwebkit2gtk-4.1.so.0 libgtk-3.so.0; do
  "$LDCONFIG" -p 2>/dev/null | grep -q "$lib" || missing="$missing $lib"
done
if [ -n "$missing" ]; then
  say "Missing libraries:$missing"
  say "Install them and run this script again:"
  say "  sudo apt install libwebkit2gtk-4.1-0 libgtk-3-0"
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading Jiraffe..."
curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/$ASSET" "$REPO/$ASSET" || die "download failed"
curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/$ASSET.sha256" "$REPO/$ASSET.sha256" || die "download failed"
(cd "$tmp" && sha256sum -c --status "$ASSET.sha256") || die "checksum mismatch, the download is damaged"
head -c 4 "$tmp/$ASSET" | grep -q ELF || die "the download is not a Linux program"

mkdir -p "$BIN_DIR"
pkill -x jiraffe 2>/dev/null && sleep 1 || true
install -m 0755 "$tmp/$ASSET" "$EXE.new"
mv -f "$EXE.new" "$EXE"
say "Installed $EXE"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) say "Note: $BIN_DIR is not in PATH; start Jiraffe from the application menu." ;;
esac

if [ "${XDG_CURRENT_DESKTOP:-}" != "${XDG_CURRENT_DESKTOP#*GNOME}" ] && command -v gnome-extensions >/dev/null 2>&1; then
  if ! gnome-extensions list --enabled 2>/dev/null | grep -qi appindicator; then
    say "Note: the tray icon needs the AppIndicator extension:"
    say "  sudo apt install gnome-shell-extension-appindicator   (then log out and in)"
  fi
fi

if [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
  nohup "$EXE" >/dev/null 2>&1 &
  say "Jiraffe started."
else
  say "Start Jiraffe from the application menu."
fi
