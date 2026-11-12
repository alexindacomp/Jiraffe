package winapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"jiraffe/internal/jira"
)

const (
	winhttpAccessTypeDefaultProxy   = 0
	winhttpAccessTypeAutomaticProxy = 4
	winhttpFlagSecure               = 0x00800000
	winhttpFlagEscapeDisable        = 0x00000040
	winhttpQueryStatusCode          = 19
	winhttpQueryFlagNumber          = 0x20000000
	winhttpOptionDecompression      = 118
	winhttpOptionRedirectPolicy     = 88
	winhttpRedirectPolicyNever      = 0
	winhttpQueryLocation            = 33
	winhttpDecompressionFlagAll     = 0x3
	winhttpAddreqFlagAdd            = 0x20000000
	winhttpAddreqFlagReplace        = 0x80000000

	maxResponseBytes = 64 << 20
)

// Transport is a jira.Transport over WinHTTP. It uses the Windows certificate
// store (so corporate CAs work), never disables certificate checks, and
// honours the system/automatic proxy configuration.
type Transport struct {
	session uintptr
}

var _ jira.Transport = (*Transport)(nil)

// NewTransport opens a WinHTTP session.
func NewTransport(userAgent string) (*Transport, error) {
	ua, err := windows.UTF16PtrFromString(userAgent)
	if err != nil {
		return nil, err
	}
	// Automatic proxy (WPAD/PAC) needs Windows 8.1+; fall back to the default.
	h, _, e := procWinHttpOpen.Call(uintptr(unsafe.Pointer(ua)), winhttpAccessTypeAutomaticProxy, 0, 0, 0)
	if h == 0 {
		h, _, e = procWinHttpOpen.Call(uintptr(unsafe.Pointer(ua)), winhttpAccessTypeDefaultProxy, 0, 0, 0)
	}
	if h == 0 {
		return nil, fmt.Errorf("WinHttpOpen: %w", winhttpError(e))
	}
	return &Transport{session: h}, nil
}

// Close releases the session.
func (t *Transport) Close() {
	if t.session != 0 {
		procWinHttpCloseHandle.Call(t.session)
		t.session = 0
	}
}

// Do performs one request synchronously; cancelling ctx closes the request
// handle, which aborts the blocking WinHTTP call.
func (t *Transport) Do(ctx context.Context, req *jira.Request) (*jira.Response, error) {
	var data []byte
	status, location, err := t.exchange(ctx, req, func(_ int, chunk []byte) error {
		data = append(data, chunk...)
		if len(data) > maxResponseBytes {
			return fmt.Errorf("response from Jira is too large (over %d MB)", maxResponseBytes>>20)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &jira.Response{StatusCode: status, Body: data, Location: location}, nil
}

// ErrBodyTooLarge is returned by Stream when the body exceeds its limit.
var ErrBodyTooLarge = errors.New("response body too large")

// Stream performs a GET like Do (redirects are never followed) but writes
// the body of a 2xx response to w as it arrives, failing with
// ErrBodyTooLarge beyond limit bytes; other bodies are discarded. It returns
// the status and, for 3xx, the Location header. Used by the updater with a
// transport of its own (no Jira credentials).
func (t *Transport) Stream(ctx context.Context, rawURL string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	var n int64
	return t.exchange(ctx, &jira.Request{Method: "GET", URL: rawURL, Headers: headers}, func(status int, chunk []byte) error {
		if status < 200 || status >= 300 {
			return nil
		}
		if n += int64(len(chunk)); n > limit {
			return ErrBodyTooLarge
		}
		_, err := w.Write(chunk)
		return err
	})
}

// exchange sends req and passes the response body to sink in chunks (not
// called for 3xx responses), while ctx can still abort the transfer.
func (t *Transport) exchange(ctx context.Context, req *jira.Request, sink func(status int, chunk []byte) error) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" {
		return 0, "", fmt.Errorf("invalid URL")
	}
	secure := false
	switch strings.ToLower(u.Scheme) {
	case "https":
		secure = true
	case "http":
	default:
		return 0, "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	port := 80
	if secure {
		port = 443
	}
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port <= 0 || port > 65535 {
			return 0, "", fmt.Errorf("invalid port %q", p)
		}
	}

	host16, err := windows.UTF16PtrFromString(u.Hostname())
	if err != nil {
		return 0, "", fmt.Errorf("invalid host")
	}
	hConnect, _, e := procWinHttpConnect.Call(t.session, uintptr(unsafe.Pointer(host16)), uintptr(port), 0)
	if hConnect == 0 {
		return 0, "", winhttpError(e)
	}
	defer procWinHttpCloseHandle.Call(hConnect)

	method16, _ := windows.UTF16PtrFromString(req.Method)
	path16, err := windows.UTF16PtrFromString(u.RequestURI())
	if err != nil {
		return 0, "", fmt.Errorf("invalid path")
	}
	flags := uintptr(winhttpFlagEscapeDisable)
	if secure {
		flags |= winhttpFlagSecure
	}
	hReq, _, e := procWinHttpOpenRequest.Call(hConnect, uintptr(unsafe.Pointer(method16)), uintptr(unsafe.Pointer(path16)), 0, 0, 0, flags)
	if hReq == 0 {
		return 0, "", winhttpError(e)
	}
	var closeOnce sync.Once
	closeReq := func() { closeOnce.Do(func() { procWinHttpCloseHandle.Call(hReq) }) }
	defer closeReq()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			closeReq()
		case <-done:
		}
	}()

	// resolve, connect, send, receive timeouts (ms)
	procWinHttpSetTimeouts.Call(hReq, 15000, 20000, 60000, 90000)
	// Never follow redirects: WinHTTP would resend the Authorization header
	// to wherever the server points. 3xx is reported to the caller instead.
	policy := uint32(winhttpRedirectPolicyNever)
	if r, _, e := procWinHttpSetOption.Call(hReq, winhttpOptionRedirectPolicy, uintptr(unsafe.Pointer(&policy)), 4); r == 0 {
		return 0, "", fmt.Errorf("disable redirects: %w", winhttpError(e))
	}
	dec := uint32(winhttpDecompressionFlagAll)
	procWinHttpSetOption.Call(hReq, winhttpOptionDecompression, uintptr(unsafe.Pointer(&dec)), 4) // Win 8.1+, best effort

	var hdr strings.Builder
	for k, v := range req.Headers {
		if strings.ContainsAny(k+v, "\r\n") {
			return 0, "", fmt.Errorf("invalid header %q", k)
		}
		hdr.WriteString(k)
		hdr.WriteString(": ")
		hdr.WriteString(v)
		hdr.WriteString("\r\n")
	}
	if hdr.Len() > 0 {
		h16, err := windows.UTF16FromString(hdr.String())
		if err != nil {
			return 0, "", fmt.Errorf("invalid headers")
		}
		r, _, e := procWinHttpAddRequestHeaders.Call(hReq, uintptr(unsafe.Pointer(&h16[0])), uintptr(len(h16)-1), winhttpAddreqFlagAdd|winhttpAddreqFlagReplace)
		if r == 0 {
			return 0, "", t.fail(ctx, e)
		}
	}

	var body unsafe.Pointer
	if len(req.Body) > 0 {
		body = unsafe.Pointer(&req.Body[0])
	}
	r, _, e := procWinHttpSendRequest.Call(hReq, 0, 0, uintptr(body), uintptr(len(req.Body)), uintptr(len(req.Body)), 0)
	if r == 0 {
		return 0, "", t.fail(ctx, e)
	}
	r, _, e = procWinHttpReceiveResponse.Call(hReq, 0)
	if r == 0 {
		return 0, "", t.fail(ctx, e)
	}

	var status, size uint32 = 0, 4
	r, _, e = procWinHttpQueryHeaders.Call(hReq, winhttpQueryStatusCode|winhttpQueryFlagNumber, 0,
		uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&size)), 0)
	if r == 0 {
		return 0, "", t.fail(ctx, e)
	}

	location := ""
	if status >= 300 && status < 400 {
		location = queryHeaderString(hReq, winhttpQueryLocation)
		return int(status), location, nil
	}

	buf := make([]byte, 64<<10)
	for {
		var n uint32
		r, _, e = procWinHttpReadData.Call(hReq, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			return 0, "", t.fail(ctx, e)
		}
		if n == 0 {
			break
		}
		if err := sink(int(status), buf[:n]); err != nil {
			return 0, "", err
		}
	}
	return int(status), location, nil
}

// queryHeaderString returns a response header as a string ("" if absent).
func queryHeaderString(hReq uintptr, query uint32) string {
	buf := make([]uint16, 2048)
	size := uint32(len(buf) * 2)
	r, _, _ := procWinHttpQueryHeaders.Call(hReq, uintptr(query), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0)
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:size/2])
}

func (t *Transport) fail(ctx context.Context, e error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return winhttpError(e)
}

// winhttpError turns a WinHTTP error code into a classified, readable error.
func winhttpError(e error) error {
	var errno syscall.Errno
	if !errors.As(e, &errno) {
		return &jira.TransportError{Kind: jira.NetOther, Message: e.Error()}
	}
	kind, msg := jira.NetOther, "network error"
	switch errno {
	case 12002:
		kind, msg = jira.NetTimeout, "the connection to Jira timed out"
	case 12005, 12006:
		kind, msg = jira.NetInvalidURL, "the Jira URL is invalid"
	case 12007:
		kind, msg = jira.NetDNS, "the Jira server name could not be resolved (check the URL, network or VPN)"
	case 12017:
		kind, msg = jira.NetCancelled, "the request was cancelled"
	case 12029:
		kind, msg = jira.NetConnect, "cannot connect to the Jira server (check the URL, network or VPN)"
	case 12030:
		kind, msg = jira.NetReset, "the connection to Jira was reset"
	case 12152:
		kind, msg = jira.NetBadResponse, "the server returned an invalid response (is this a Jira URL?)"
	case 12037:
		kind, msg = jira.NetTLSExpired, "the server's TLS certificate has expired or is not yet valid"
	case 12038:
		kind, msg = jira.NetTLSName, "the server's TLS certificate does not match the host name"
	case 12044:
		kind, msg = jira.NetTLSClient, "the server requires a client certificate"
	case 12045:
		kind, msg = jira.NetTLSUntrust, "the server's TLS certificate is not trusted by Windows (unknown certificate authority)"
	case 12057:
		kind, msg = jira.NetTLSRevoke, "the server's TLS certificate revocation could not be checked"
	case 12175:
		kind, msg = jira.NetTLS, "a TLS/certificate error occurred while connecting to Jira"
	case 12180:
		kind, msg = jira.NetProxy, "proxy auto-detection failed"
	}
	return &jira.TransportError{Kind: kind, Message: fmt.Sprintf("%s (WinHTTP %d)", msg, uint32(errno))}
}
