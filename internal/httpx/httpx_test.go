package httpx

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jiraffe/internal/jira"
)

func TestDoSendsRequestAndReadsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer x" || r.Header.Get("User-Agent") != "Jiraffe/test" {
			w.WriteHeader(400)
			return
		}
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte("got " + b.String()))
	}))
	defer srv.Close()
	tr := New("Jiraffe/test")
	defer tr.Close()
	resp, err := tr.Do(context.Background(), &jira.Request{Method: "POST", URL: srv.URL, Headers: map[string]string{"Authorization": "Bearer x"}, Body: []byte("hi")})
	if err != nil || resp.StatusCode != 201 || string(resp.Body) != "got hi" {
		t.Fatalf("resp %+v, err %v", resp, err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid/next", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := New("t").Do(context.Background(), &jira.Request{Method: "GET", URL: srv.URL})
	if err != nil || resp.StatusCode != 302 || resp.Location != "https://example.invalid/next" {
		t.Fatalf("resp %+v, err %v", resp, err)
	}
}

func TestStreamLimitAndNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("not found page"))
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()
	tr := New("t")
	var b bytes.Buffer
	if st, _, err := tr.Stream(context.Background(), srv.URL+"/f", nil, &b, 100); err != nil || st != 200 || b.Len() != 100 {
		t.Fatalf("exact limit: %d %v %d", st, err, b.Len())
	}
	b.Reset()
	if _, _, err := tr.Stream(context.Background(), srv.URL+"/f", nil, &b, 99); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("over limit: %v", err)
	}
	b.Reset()
	if st, _, err := tr.Stream(context.Background(), srv.URL+"/missing", nil, &b, 99); err != nil || st != 404 || b.Len() != 0 {
		t.Fatalf("404: %d %v %q", st, err, b.String())
	}
}

func TestTLSIsVerified(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := New("t").Do(context.Background(), &jira.Request{Method: "GET", URL: srv.URL})
	var te *jira.TransportError
	if !errors.As(err, &te) || te.Kind != jira.NetTLSUntrust {
		t.Fatalf("self-signed server: %v", err)
	}
}

func TestErrorKinds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens there now
	_, err := New("t").Do(context.Background(), &jira.Request{Method: "GET", URL: url})
	var te *jira.TransportError
	if !errors.As(err, &te) || te.Kind != jira.NetConnect {
		t.Fatalf("closed port: %v", err)
	}
	if _, err := New("t").Do(context.Background(), &jira.Request{Method: "GET", URL: "ftp://x"}); !errors.As(err, &te) || te.Kind != jira.NetInvalidURL {
		t.Fatalf("ftp: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) }))
	defer slow.Close()
	if _, err := New("t").Do(ctx, &jira.Request{Method: "GET", URL: slow.URL}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled: %v", err)
	}
}
