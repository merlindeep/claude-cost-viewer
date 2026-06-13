// Package auth resolves the Claude Code OAuth access token from the same
// sources the official client uses, in priority order:
//
//  1. The CLAUDE_CODE_OAUTH_TOKEN environment variable (a bare token).
//  2. The macOS Keychain entry "Claude Code-credentials".
//  3. The ~/.claude/.credentials.json file.
//
// This mirrors the behaviour of the original prototype and is intentionally
// read-only: the tool never writes, refreshes, or transmits the token anywhere
// other than to Anthropic's usage endpoint.
//
// All external interactions (environment, filesystem, Keychain, OS detection)
// are injected through [Resolver] fields so the resolution logic can be tested
// without touching the host machine.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// KeychainService is the macOS Keychain generic-password service name under
// which Claude Code stores its credentials.
const KeychainService = "Claude Code-credentials"

// ErrNotFound is returned when no credential source yields a token.
var ErrNotFound = errors.New("OAuth token not found (checked $CLAUDE_CODE_OAUTH_TOKEN, macOS Keychain, ~/.claude/.credentials.json)")

// Credentials holds a resolved access token plus whatever metadata the source
// provided.
type Credentials struct {
	// AccessToken is the bearer token used to call the usage endpoint.
	AccessToken string
	// ExpiresAt is the token expiry in Unix milliseconds, or 0 if unknown.
	ExpiresAt int64
	// Plan is the raw subscriptionType string, or empty if unknown.
	Plan string
	// Source is a human-readable description of where the token came from.
	Source string
}

// ExpiresAtTime returns the expiry as a time.Time. The boolean is false when
// the expiry is unknown.
func (c Credentials) ExpiresAtTime() (time.Time, bool) {
	if c.ExpiresAt <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(c.ExpiresAt), true
}

// MaskedToken returns the token with its middle redacted, safe for logging.
func (c Credentials) MaskedToken() string {
	t := c.AccessToken
	if len(t) <= 12 {
		return "***"
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// rawCreds matches the JSON shape stored by Claude Code in both the Keychain
// blob and the on-disk credentials file.
type rawCreds struct {
	ClaudeAiOauth struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAt        int64  `json:"expiresAt"`
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

// parseRaw decodes a credentials blob, returning false when it does not contain
// an access token.
func parseRaw(b []byte, source string) (Credentials, bool) {
	var r rawCreds
	if err := json.Unmarshal(b, &r); err != nil {
		return Credentials{}, false
	}
	if r.ClaudeAiOauth.AccessToken == "" {
		return Credentials{}, false
	}
	return Credentials{
		AccessToken: r.ClaudeAiOauth.AccessToken,
		ExpiresAt:   r.ClaudeAiOauth.ExpiresAt,
		Plan:        r.ClaudeAiOauth.SubscriptionType,
		Source:      source,
	}, true
}

// Resolver locates credentials. The function fields allow tests to substitute
// the environment, filesystem, and Keychain. A zero Resolver is not usable;
// construct one with [New].
type Resolver struct {
	// GOOS is the operating system identifier (defaults to runtime.GOOS).
	GOOS string
	// Getenv reads an environment variable.
	Getenv func(string) string
	// UserHomeDir returns the current user's home directory.
	UserHomeDir func() (string, error)
	// ReadFile reads a file's contents.
	ReadFile func(string) ([]byte, error)
	// Keychain returns the raw macOS Keychain blob, or an error if it cannot be
	// read. It is only consulted on darwin.
	Keychain func() ([]byte, error)
}

// New returns a Resolver wired to the real host environment.
func New() *Resolver {
	return &Resolver{
		GOOS:        runtime.GOOS,
		Getenv:      os.Getenv,
		UserHomeDir: os.UserHomeDir,
		ReadFile:    os.ReadFile,
		Keychain:    readKeychain,
	}
}

// readKeychain shells out to the macOS "security" tool to fetch the stored
// credentials blob.
func readKeychain() ([]byte, error) {
	return keychainFromRunner(func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	})
}

// keychainFromRunner runs the given command and trims the result. It is
// separated from [readKeychain] so the trimming/error logic is testable without
// the real "security" binary.
func keychainFromRunner(run func(name string, args ...string) ([]byte, error)) ([]byte, error) {
	out, err := run("security", "find-generic-password", "-s", KeychainService, "-w")
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out), nil
}

// Resolve returns the first credentials it can locate, following the documented
// priority order. It returns [ErrNotFound] when every source is exhausted.
func (r *Resolver) Resolve() (Credentials, error) {
	// 1) Environment variable holding a bare token.
	if r.Getenv != nil {
		if token := trim(r.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); token != "" {
			return Credentials{AccessToken: token, Source: "env CLAUDE_CODE_OAUTH_TOKEN"}, nil
		}
	}

	// 2) macOS Keychain.
	if r.GOOS == "darwin" && r.Keychain != nil {
		if blob, err := r.Keychain(); err == nil {
			if c, ok := parseRaw(blob, "macOS Keychain"); ok {
				return c, nil
			}
		}
	}

	// 3) ~/.claude/.credentials.json
	if r.UserHomeDir != nil && r.ReadFile != nil {
		if home, err := r.UserHomeDir(); err == nil {
			path := filepath.Join(home, ".claude", ".credentials.json")
			if b, err := r.ReadFile(path); err == nil {
				if c, ok := parseRaw(b, path); ok {
					return c, nil
				}
			}
		}
	}

	return Credentials{}, ErrNotFound
}

func trim(s string) string {
	return string(bytes.TrimSpace([]byte(s)))
}
