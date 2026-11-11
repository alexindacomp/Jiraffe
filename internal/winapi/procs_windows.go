package winapi

import "golang.org/x/sys/windows"

var (
	modUser32  = windows.NewLazySystemDLL("user32.dll")
	modShell32 = windows.NewLazySystemDLL("shell32.dll")
	modKernel  = windows.NewLazySystemDLL("kernel32.dll")
	modWinHTTP = windows.NewLazySystemDLL("winhttp.dll")
	modComctl  = windows.NewLazySystemDLL("comctl32.dll")

	procRegisterClassExW         = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW          = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW           = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow            = modUser32.NewProc("DestroyWindow")
	procPostMessageW             = modUser32.NewProc("PostMessageW")
	procGetMessageW              = modUser32.NewProc("GetMessageW")
	procTranslateMessage         = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW         = modUser32.NewProc("DispatchMessageW")
	procLoadImageW               = modUser32.NewProc("LoadImageW")
	procCreatePopupMenu          = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW              = modUser32.NewProc("AppendMenuW")
	procSetMenuDefaultItem       = modUser32.NewProc("SetMenuDefaultItem")
	procTrackPopupMenu           = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu              = modUser32.NewProc("DestroyMenu")
	procSetForegroundWindow      = modUser32.NewProc("SetForegroundWindow")
	procGetCursorPos             = modUser32.NewProc("GetCursorPos")
	procFindWindowW              = modUser32.NewProc("FindWindowW")
	procShowWindow               = modUser32.NewProc("ShowWindow")
	procIsWindow                 = modUser32.NewProc("IsWindow")
	procIsIconic                 = modUser32.NewProc("IsIconic")
	procRegisterWindowMessageW   = modUser32.NewProc("RegisterWindowMessageW")
	procGetSystemMetrics         = modUser32.NewProc("GetSystemMetrics")
	procAllowSetForegroundWindow = modUser32.NewProc("AllowSetForegroundWindow")
	procGetWindowThreadProcessId = modUser32.NewProc("GetWindowThreadProcessId")
	procGetDpiForSystem          = modUser32.NewProc("GetDpiForSystem")
	procSystemParametersInfoW    = modUser32.NewProc("SystemParametersInfoW")
	procDestroyIcon              = modUser32.NewProc("DestroyIcon")

	procShellNotifyIconW = modShell32.NewProc("Shell_NotifyIconW")

	procCreateMutexW = modKernel.NewProc("CreateMutexW")

	procLoadIconMetric = modComctl.NewProc("LoadIconMetric")

	procWinHttpOpen              = modWinHTTP.NewProc("WinHttpOpen")
	procWinHttpConnect           = modWinHTTP.NewProc("WinHttpConnect")
	procWinHttpOpenRequest       = modWinHTTP.NewProc("WinHttpOpenRequest")
	procWinHttpSendRequest       = modWinHTTP.NewProc("WinHttpSendRequest")
	procWinHttpReceiveResponse   = modWinHTTP.NewProc("WinHttpReceiveResponse")
	procWinHttpQueryHeaders      = modWinHTTP.NewProc("WinHttpQueryHeaders")
	procWinHttpReadData          = modWinHTTP.NewProc("WinHttpReadData")
	procWinHttpCloseHandle       = modWinHTTP.NewProc("WinHttpCloseHandle")
	procWinHttpSetTimeouts       = modWinHTTP.NewProc("WinHttpSetTimeouts")
	procWinHttpSetOption         = modWinHTTP.NewProc("WinHttpSetOption")
	procWinHttpAddRequestHeaders = modWinHTTP.NewProc("WinHttpAddRequestHeaders")
)

// Window messages and related constants.
const (
	WMNull        = 0x0000
	WMDestroy     = 0x0002
	WMClose       = 0x0010
	WMCommand     = 0x0111
	WMContextMenu = 0x007B
	WMLButtonUp   = 0x0202
	WMRButtonUp   = 0x0205
	// WMApp is WM_APP; go-webview2 uses WM_APP itself for Dispatch, so the
	// app's own messages use WMApp+N with N > 0.
	WMApp = 0x8000

	SWShow    = 5
	SWRestore = 9
)
