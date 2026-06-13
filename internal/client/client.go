// Package client talks to Claude's OAuth usage endpoint.
//
// The request contract is deliberately identical to the one used by the
// official Claude Code client, because the endpoint is undocumented and rejects
// anything that does not look like Claude Code:
//
//   - Method/URL: GET https://api.anthropic.com/api/oauth/usage
//   - Authorization: Bearer <oauth-token>
//   - anthropic-beta: oauth-2025-04-20
//   - User-Agent: claude-code/<version>   (REQUIRED — a missing or foreign
//     User-Agent yields immediate HTTP 429 rate limiting)
//
// The endpoint is the source of truth for utilization percentages; it is what
// powers the "/usage" view in Claude Code and the desktop app.
package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// Default request parameters.
const (
	// DefaultBaseURL is the usage endpoint.
	DefaultBaseURL = "https://api.anthropic.com/api/oauth/usage"
	// DefaultBeta is the anthropic-beta header value the endpoint expects.
	DefaultBeta = "oauth-2025-04-20"
	// DefaultTimeout bounds a single request.
	DefaultTimeout = 15 * time.Second
	// FallbackVersion is used for the User-Agent when the installed Claude Code
	// version cannot be detected.
	FallbackVersion = "2.0.0"
)

// APIError is returned when the endpoint responds with a non-200 status.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// Client performs usage requests.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	Beta      string
	UserAgent string
}

// Option customizes a [Client].
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client (useful in tests).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.HTTP = h } }

// WithBaseURL overrides the endpoint URL (useful in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.BaseURL = u } }

// WithBeta overrides the anthropic-beta header value.
func WithBeta(b string) Option { return func(c *Client) { c.Beta = b } }

// New returns a Client whose User-Agent is "claude-code/<claudeVersion>".
func New(claudeVersion string, opts ...Option) *Client {
	if strings.TrimSpace(claudeVersion) == "" {
		claudeVersion = FallbackVersion
	}
	c := &Client{
		HTTP:      &http.Client{Timeout: DefaultTimeout},
		BaseURL:   DefaultBaseURL,
		Beta:      DefaultBeta,
		UserAgent: "claude-code/" + claudeVersion,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Fetch requests the current usage snapshot. On success it returns the decoded
// usage and the raw response body. On a non-200 response it returns the raw
// body together with an [*APIError]. The raw body is always returned when
// available so callers can surface it in debug output.
func (c *Client) Fetch(ctx context.Context, token string) (*usage.Usage, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", c.Beta)
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request usage endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, body, &APIError{Status: resp.StatusCode, Body: Snippet(body)}
	}

	u, err := usage.Parse(body)
	if err != nil {
		return nil, body, err
	}
	return u, body, nil
}

// Snippet returns a trimmed, length-capped view of a response body for logging.
func Snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	const maxLen = 160
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}

// NextBackoff computes the next exponential backoff duration. The first call
// (current == 0) returns base; subsequent calls double the previous value,
// capped at limit.
func NextBackoff(current, base, limit time.Duration) time.Duration {
	if current <= 0 {
		current = base
	} else {
		current *= 2
	}
	if current > limit {
		current = limit
	}
	return current
}

// DetectClaudeVersion determines the Claude Code version string for the
// User-Agent. It prefers the CLAUDE_CODE_VERSION environment variable, then
// falls back to running "claude --version", and finally to [FallbackVersion].
//
// Both the environment lookup and the command runner are injected so the
// detection can be tested deterministically.
func DetectClaudeVersion(getenv func(string) string, run func() ([]byte, error)) string {
	if getenv != nil {
		if v := strings.TrimSpace(getenv("CLAUDE_CODE_VERSION")); v != "" {
			return v
		}
	}
	if run != nil {
		if out, err := run(); err == nil {
			// Output looks like "2.0.14 (Claude Code)"; take the first field.
			if fields := strings.Fields(strings.TrimSpace(string(out))); len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return FallbackVersion
}

// claudeVersionCmd runs "claude --version".
func claudeVersionCmd() ([]byte, error) {
	return exec.Command("claude", "--version").Output()
}

// DetectClaudeVersionDefault detects the version using the real environment and
// the "claude" binary.
func DetectClaudeVersionDefault(getenv func(string) string) string {
	return DetectClaudeVersion(getenv, claudeVersionCmd)
}
