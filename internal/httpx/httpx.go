// Package httpx is the jira.Transport of the Linux build: net/http with the
// proxy from the environment (HTTPS_PROXY / NO_PROXY), the system CA store
// and full TLS verification. Like WinHTTP on Windows it never follows
// redirects (the caller sees the 3xx and its Location) and classifies network
// failures as jira.TransportError kinds.
package httpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"jiraffe/internal/jira"
)

// MaxResponseBytes bounds a response body read by Do.
const MaxResponseBytes = 64 << 20

// ErrBodyTooLarge is returned by Stream when the body exceeds its limit.
var ErrBodyTooLarge = errors.New("response body too large")

// Transport performs requests with one shared connection pool.
type Transport struct {
	ua string
	c  *http.Client
}

var _ jira.Transport = (*Transport)(nil)

// New creates a transport sending userAgent.
func New(userAgent string) *Transport {
	rt := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &Transport{ua: userAgent, c: &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Close drops idle connections.
func (t *Transport) Close() { t.c.CloseIdleConnections() }

// Do performs one request and reads the whole body (at most
// MaxResponseBytes).
func (t *Transport) Do(ctx context.Context, req *jira.Request) (*jira.Response, error) {
	var buf bytes.Buffer
	status, loc, err := t.exchange(ctx, req, func(_ int, body io.Reader) error {
		n, err := io.Copy(&buf, io.LimitReader(body, MaxResponseBytes+1))
		if err != nil {
			return err
		}
		if n > MaxResponseBytes {
			return fmt.Errorf("response from Jira is too large (over %d MB)", MaxResponseBytes>>20)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &jira.Response{StatusCode: status, Body: buf.Bytes(), Location: loc}, nil
}

// Stream performs a GET like Do but writes the body of a 2xx response to w,
// failing with ErrBodyTooLarge beyond limit bytes; other bodies are
// discarded. It returns the status and, for 3xx, the Location header.
func (t *Transport) Stream(ctx context.Context, rawURL string, headers map[string]string, w io.Writer, limit int64) (int, string, error) {
	return t.exchange(ctx, &jira.Request{Method: "GET", URL: rawURL, Headers: headers}, func(status int, body io.Reader) error {
		if status < 200 || status >= 300 {
			return nil
		}
		n, err := io.Copy(w, io.LimitReader(body, limit+1))
		if err != nil {
			return err
		}
		if n > limit {
			return ErrBodyTooLarge
		}
		return nil
	})
}

func (t *Transport) exchange(ctx context.Context, req *jira.Request, sink func(status int, body io.Reader) error) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return 0, "", &jira.TransportError{Kind: jira.NetInvalidURL, Message: "the Jira URL is invalid"}
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return 0, "", &jira.TransportError{Kind: jira.NetInvalidURL, Message: "the Jira URL is invalid"}
	}
	hr.Header.Set("User-Agent", t.ua)
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := t.c.Do(hr)
	if err != nil {
		return 0, "", classify(ctx, err)
	}
	defer resp.Body.Close()
	loc := ""
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc = resp.Header.Get("Location")
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return resp.StatusCode, loc, nil
	}
	if err := sink(resp.StatusCode, bodyReader{resp.Body}); err != nil {
		var re readError
		if errors.As(err, &re) { // the connection failed mid-body
			return resp.StatusCode, "", classify(ctx, re.error)
		}
		return resp.StatusCode, "", err // too large, or the writer failed
	}
	return resp.StatusCode, "", nil
}

// bodyReader marks errors of reading the response body, so that they are
// told apart from errors of the sink's own writer.
type bodyReader struct{ r io.Reader }

type readError struct{ error }

func (b bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && err != io.EOF {
		err = readError{err}
	}
	return n, err
}

// classify turns a net/http error into a jira.TransportError (or the
// context's error when ctx ended).
func classify(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	kind, msg := jira.NetOther, "network error: "+err.Error()
	var (
		dnsErr   *net.DNSError
		unknown  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		certErr  x509.CertificateInvalidError
		recErr   tls.RecordHeaderError
		verifErr *tls.CertificateVerificationError
		netErr   net.Error
	)
	switch {
	case errors.As(err, &dnsErr):
		kind, msg = jira.NetDNS, "the Jira server name could not be resolved (check the URL, network or VPN)"
	case errors.As(err, &unknown):
		kind, msg = jira.NetTLSUntrust, "the server's TLS certificate is not trusted by the system (unknown certificate authority)"
	case errors.As(err, &hostErr):
		kind, msg = jira.NetTLSName, "the server's TLS certificate does not match the host name"
	case errors.As(err, &certErr) && certErr.Reason == x509.Expired:
		kind, msg = jira.NetTLSExpired, "the server's TLS certificate has expired or is not yet valid"
	case errors.As(err, &certErr), errors.As(err, &verifErr), errors.As(err, &recErr):
		kind, msg = jira.NetTLS, "a TLS/certificate error occurred while connecting to Jira"
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		kind, msg = jira.NetConnect, "cannot connect to the Jira server (check the URL, network or VPN)"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.ErrUnexpectedEOF):
		kind, msg = jira.NetReset, "the connection to Jira was reset"
	case errors.As(err, &netErr) && netErr.Timeout():
		kind, msg = jira.NetTimeout, "the connection to Jira timed out"
	case strings.Contains(err.Error(), "proxyconnect"):
		kind, msg = jira.NetProxy, "cannot connect through the proxy"
	case strings.Contains(err.Error(), "malformed HTTP"):
		kind, msg = jira.NetBadResponse, "the server returned an invalid response (is this a Jira URL?)"
	}
	return &jira.TransportError{Kind: kind, Message: msg}
}
