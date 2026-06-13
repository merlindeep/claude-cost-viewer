package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewUserAgent(t *testing.T) {
	if got := New("2.0.14").UserAgent; got != "claude-code/2.0.14" {
		t.Errorf("UserAgent = %q", got)
	}
	if got := New("  ").UserAgent; got != "claude-code/"+FallbackVersion {
		t.Errorf("blank version UserAgent = %q", got)
	}
}

func TestNewOptions(t *testing.T) {
	hc := &http.Client{Timeout: time.Second}
	c := New("1.0.0", WithHTTPClient(hc), WithBaseURL("http://example.test"), WithBeta("beta-x"))
	if c.HTTP != hc || c.BaseURL != "http://example.test" || c.Beta != "beta-x" {
		t.Errorf("options not applied: %+v", c)
	}
}

func TestFetchSuccessAndHeaders(t *testing.T) {
	var gotAuth, gotBeta, gotUA, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotUA = r.Header.Get("User-Agent")
		gotCT = r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":42,"resets_at":"2026-06-13T15:00:00Z"}}`))
	}))
	defer srv.Close()

	c := New("2.0.14", WithBaseURL(srv.URL))
	u, raw, err := c.Fetch(context.Background(), "secret-token")
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if u.FiveHour.Percent() != 42 {
		t.Errorf("five_hour = %v, want 42", u.FiveHour.Percent())
	}
	if len(raw) == 0 {
		t.Error("expected raw body")
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBeta != DefaultBeta {
		t.Errorf("anthropic-beta = %q", gotBeta)
	}
	if gotUA != "claude-code/2.0.14" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
}

func TestFetchNon200(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("error body"))
		}))
		c := New("1", WithBaseURL(srv.URL))
		_, raw, err := c.Fetch(context.Background(), "t")
		srv.Close()

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("status %d: error = %v, want *APIError", status, err)
		}
		if apiErr.Status != status {
			t.Errorf("APIError.Status = %d, want %d", apiErr.Status, status)
		}
		if string(raw) != "error body" {
			t.Errorf("raw = %q", raw)
		}
		if apiErr.Error() == "" {
			t.Error("APIError.Error() should be non-empty")
		}
	}
}

func TestFetchMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("definitely not json"))
	}))
	defer srv.Close()
	c := New("1", WithBaseURL(srv.URL))
	_, raw, err := c.Fetch(context.Background(), "t")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if string(raw) != "definitely not json" {
		t.Errorf("raw = %q", raw)
	}
}

func TestFetchNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // ensure the connection is refused
	c := New("1", WithBaseURL(url))
	if _, _, err := c.Fetch(context.Background(), "t"); err == nil {
		t.Fatal("expected network error")
	}
}

func TestFetchBadURL(t *testing.T) {
	c := New("1", WithBaseURL("://not-a-url"))
	if _, _, err := c.Fetch(context.Background(), "t"); err == nil {
		t.Fatal("expected request-build error")
	}
}

func TestSnippet(t *testing.T) {
	if got := Snippet([]byte("  hi  ")); got != "hi" {
		t.Errorf("Snippet = %q", got)
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	got := Snippet(long)
	if len([]rune(got)) != 161 { // 160 chars + ellipsis rune
		t.Errorf("Snippet length = %d, want 161", len([]rune(got)))
	}
}

func TestNextBackoff(t *testing.T) {
	base := 60 * time.Second
	limit := 15 * time.Minute
	if got := NextBackoff(0, base, limit); got != base {
		t.Errorf("first backoff = %v, want %v", got, base)
	}
	if got := NextBackoff(base, base, limit); got != 2*base {
		t.Errorf("second backoff = %v, want %v", got, 2*base)
	}
	if got := NextBackoff(20*time.Minute, base, limit); got != limit {
		t.Errorf("capped backoff = %v, want %v", got, limit)
	}
}

func TestDetectClaudeVersion(t *testing.T) {
	// Environment variable wins.
	got := DetectClaudeVersion(func(string) string { return "9.9.9" }, nil)
	if got != "9.9.9" {
		t.Errorf("env version = %q", got)
	}
	// Falls back to the command output.
	got = DetectClaudeVersion(
		func(string) string { return "" },
		func() ([]byte, error) { return []byte("2.0.14 (Claude Code)\n"), nil },
	)
	if got != "2.0.14" {
		t.Errorf("cmd version = %q", got)
	}
	// Command error -> fallback.
	got = DetectClaudeVersion(
		func(string) string { return "" },
		func() ([]byte, error) { return nil, errors.New("not installed") },
	)
	if got != FallbackVersion {
		t.Errorf("fallback version = %q", got)
	}
	// Nil dependencies -> fallback.
	if got := DetectClaudeVersion(nil, nil); got != FallbackVersion {
		t.Errorf("nil-deps version = %q", got)
	}
	// Empty command output -> fallback.
	got = DetectClaudeVersion(func(string) string { return "" }, func() ([]byte, error) { return []byte("   "), nil })
	if got != FallbackVersion {
		t.Errorf("empty-output version = %q", got)
	}
}

func TestDetectClaudeVersionDefault(t *testing.T) {
	// Env path.
	if got := DetectClaudeVersionDefault(func(string) string { return "7.7.7" }); got != "7.7.7" {
		t.Errorf("default detect env = %q", got)
	}
	// Exercise the real command runner (claude is absent in tests -> fallback).
	if got := DetectClaudeVersionDefault(func(string) string { return "" }); got == "" {
		t.Error("expected a non-empty version string")
	}
}
