// Package jira is a minimal Jira Server/Data Center REST API v2 client.
//
// HTTP goes through the Transport interface so the package stays free of
// net/http (binary size) and can be tested with a fake; on Windows the
// transport is WinHTTP (internal/winapi).
package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Request is an HTTP request issued through a Transport.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Response is an HTTP response.
type Response struct {
	StatusCode int
	Body       []byte
	Location   string // Location header of 3xx responses (redirects are never followed)
}

// Transport error kinds (TransportError.Kind), used to localize messages.
const (
	NetTimeout     = "timeout"
	NetDNS         = "dns"
	NetConnect     = "connect"
	NetReset       = "reset"
	NetTLS         = "tls"
	NetTLSUntrust  = "tls_untrusted"
	NetTLSExpired  = "tls_expired"
	NetTLSName     = "tls_name"
	NetTLSClient   = "tls_client_cert"
	NetTLSRevoke   = "tls_revocation"
	NetInvalidURL  = "invalid_url"
	NetCancelled   = "cancelled"
	NetBadResponse = "bad_server_response"
	NetProxy       = "proxy"
	NetOther       = "other"
)

// TransportError is a classified transport failure. Transports should return
// it so the app can show a localized message.
type TransportError struct {
	Kind    string // one of the Net* constants
	Message string // English description (logs, fallback)
}

func (e *TransportError) Error() string { return e.Message }

// Transport performs HTTP requests. Implementations must verify TLS
// certificates and honour ctx cancellation.
type Transport interface {
	Do(ctx context.Context, req *Request) (*Response, error)
}

// Error codes of *Error.
const (
	CodeNetwork      = "network"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeBadRequest   = "bad_request"
	CodeServer       = "jira_error"
	CodeBadResponse  = "bad_response"
)

// Kinds of *Error that are not transport kinds.
const (
	KindHTML       = "html"        // CodeBadResponse: HTML instead of JSON
	KindJSON       = "json"        // CodeBadResponse: malformed JSON
	KindRedirect   = "redirect"    // CodeBadResponse: 3xx, Detail is the Location host
	KindInvalidKey = "invalid_key" // CodeBadRequest: rejected before sending
)

// Error is a readable Jira/transport failure.
type Error struct {
	Status  int    // HTTP status, 0 for transport errors
	Code    string // one of the Code* constants
	Kind    string // finer classification (Net* or Kind*), may be empty
	Detail  string // text from Jira (errorMessages) or the redirect host; untranslated
	Message string // English, human readable, safe to show to the user
	// FieldErrors and Messages are Jira's error body ("errors" by field id,
	// "errorMessages"), as sent; set for 4xx responses that carry them.
	FieldErrors map[string]string
	Messages    []string
}

func (e *Error) Error() string { return e.Message }

// Client talks to one Jira instance with a personal access token.
type Client struct {
	base  string
	token string
	t     Transport
}

// New creates a client. baseURL is like "https://sd.local" (a trailing slash
// is ignored).
func New(baseURL, token string, t Transport) *Client {
	return &Client{base: strings.TrimRight(strings.TrimSpace(baseURL), "/"), token: token, t: t}
}

// BaseURL returns the normalized base URL.
func (c *Client) BaseURL() string { return c.base }

// BrowseURL returns the web URL of an issue.
func (c *Client) BrowseURL(key string) string { return c.base + "/browse/" + url.PathEscape(key) }

func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req := &Request{
		Method: method,
		URL:    u,
		Headers: map[string]string{
			"Authorization": "Bearer " + c.token,
			"Accept":        "application/json",
		},
	}
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		req.Body = b
		req.Headers["Content-Type"] = "application/json"
	}
	resp, err := c.t.Do(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return &Error{Code: CodeNetwork, Kind: NetTimeout, Message: "request to Jira was cancelled or timed out"}
		}
		var te *TransportError
		if errors.As(err, &te) {
			return &Error{Code: CodeNetwork, Kind: te.Kind, Message: te.Message}
		}
		return &Error{Code: CodeNetwork, Kind: NetOther, Message: err.Error()}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		host := resp.Location
		if lu, err := url.Parse(resp.Location); err == nil && lu.Host != "" {
			host = lu.Host
		}
		msg := "Jira redirected the request"
		if host != "" {
			msg += " to " + host
		}
		return &Error{Status: resp.StatusCode, Code: CodeBadResponse, Kind: KindRedirect, Detail: host,
			Message: msg + "; check the Jira URL (https, host name, SSO)"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp)
	}
	if out == nil || resp.StatusCode == 204 || len(bytes.TrimSpace(resp.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		if looksLikeHTML(resp.Body) {
			return &Error{Status: resp.StatusCode, Code: CodeBadResponse, Kind: KindHTML,
				Message: "Jira returned an HTML page instead of JSON; check the Jira URL"}
		}
		return &Error{Status: resp.StatusCode, Code: CodeBadResponse, Kind: KindJSON, Detail: err.Error(),
			Message: "unexpected response from Jira: " + err.Error()}
	}
	return nil
}

func looksLikeHTML(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '<'
}

// jiraErrorBody is Jira's standard error payload.
type jiraErrorBody struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
	Message       string            `json:"message"`
}

func statusError(resp *Response) *Error {
	var details []string
	var body jiraErrorBody
	if json.Unmarshal(resp.Body, &body) == nil {
		details = append(details, body.ErrorMessages...)
		for k, v := range body.Errors {
			details = append(details, k+": "+v)
		}
		if body.Message != "" {
			details = append(details, body.Message)
		}
	}
	detail := strings.Join(details, "; ")
	e := &Error{Status: resp.StatusCode, Messages: body.ErrorMessages}
	if len(body.Errors) > 0 {
		e.FieldErrors = body.Errors
	}
	switch {
	case resp.StatusCode == 401:
		e.Code, e.Message = CodeUnauthorized, "Jira rejected the token (HTTP 401): check the personal access token"
	case resp.StatusCode == 403:
		e.Code, e.Message = CodeForbidden, "access denied by Jira (HTTP 403)"
	case resp.StatusCode == 404:
		e.Code, e.Message = CodeNotFound, "not found in Jira (HTTP 404)"
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		e.Code, e.Message = CodeBadRequest, "Jira rejected the request (HTTP "+strconv.Itoa(resp.StatusCode)+")"
	default:
		e.Code, e.Message = CodeServer, "Jira server error (HTTP "+strconv.Itoa(resp.StatusCode)+")"
	}
	if detail != "" {
		if len(detail) > 500 {
			cut := 500
			for cut > 0 && !utf8.RuneStart(detail[cut]) {
				cut-- // never split a multi-byte rune
			}
			detail = detail[:cut] + "…"
		}
		e.Detail = detail
		e.Message += ": " + detail
	}
	return e
}

// IsCode reports whether err is a *Error with the given code.
func IsCode(err error, code string) bool {
	var je *Error
	return errors.As(err, &je) && je.Code == code
}

// ValidIssueKey reports whether key looks like a Jira issue key (PROJ-123).
// Used to keep user-provided keys from altering request paths.
func ValidIssueKey(key string) bool {
	dash := strings.LastIndexByte(key, '-')
	if dash < 1 || dash == len(key)-1 || len(key) > 64 {
		return false
	}
	for i := 0; i < dash; i++ {
		c := key[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	for i := dash + 1; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	return true
}

// timeLayouts are tried in order by ParseTime. Go accepts an optional
// fractional second after the seconds field when parsing.
var timeLayouts = []string{
	"2006-01-02T15:04:05Z0700",  // 2026-11-09T14:24:46.807+0200 (Jira)
	"2006-01-02T15:04:05Z07:00", // RFC 3339
	"2006-01-02 15:04:05Z0700",
	"2006-01-02T15:04:05",
}

// ParseTime parses Jira timestamps such as "2026-11-09T14:24:46.807+0200".
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized Jira timestamp %q", s)
}

// FormatTime formats t the way Jira expects in worklog "started".
func FormatTime(t time.Time) string { return t.Format("2006-01-02T15:04:05.000-0700") }
