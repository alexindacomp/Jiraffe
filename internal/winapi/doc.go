// Package winapi contains the Windows-specific services of Jiraffe: the
// WinHTTP transport, DPAPI token protection, the tray icon with balloons,
// autostart, shell open, message boxes and single-instance detection.
//
// All implementation files are *_windows.go; on other systems the package is
// empty so `go vet ./...` and `go test ./...` work on Linux.
package winapi
