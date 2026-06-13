package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sampleBlob = `{"claudeAiOauth":{"accessToken":"tok-abcdef-1234","expiresAt":1750000000000,"subscriptionType":"max_20x"}}`

// envFrom builds a Getenv function backed by a map.
func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveEnvToken(t *testing.T) {
	r := &Resolver{
		GOOS:   "linux",
		Getenv: envFrom(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "  bare-token  "}),
	}
	c, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if c.AccessToken != "bare-token" {
		t.Errorf("token = %q, want trimmed %q", c.AccessToken, "bare-token")
	}
	if c.Source != "env CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("source = %q", c.Source)
	}
}

func TestResolveKeychainOnDarwin(t *testing.T) {
	r := &Resolver{
		GOOS:     "darwin",
		Getenv:   envFrom(nil),
		Keychain: func() ([]byte, error) { return []byte(sampleBlob), nil },
	}
	c, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if c.AccessToken != "tok-abcdef-1234" || c.Plan != "max_20x" {
		t.Errorf("unexpected creds: %+v", c)
	}
	if c.Source != "macOS Keychain" {
		t.Errorf("source = %q", c.Source)
	}
}

func TestResolveKeychainSkippedOffDarwin(t *testing.T) {
	called := false
	r := &Resolver{
		GOOS:        "linux",
		Getenv:      envFrom(nil),
		Keychain:    func() ([]byte, error) { called = true; return []byte(sampleBlob), nil },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	if _, err := r.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if called {
		t.Error("Keychain must not be consulted off darwin")
	}
}

func TestResolveKeychainErrorFallsThrough(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, sampleBlob)
	r := &Resolver{
		GOOS:        "darwin",
		Getenv:      envFrom(nil),
		Keychain:    func() ([]byte, error) { return nil, errors.New("locked") },
		UserHomeDir: func() (string, error) { return dir, nil },
		ReadFile:    os.ReadFile,
	}
	c, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if c.AccessToken != "tok-abcdef-1234" {
		t.Errorf("expected file fallback, got %+v", c)
	}
}

func TestResolveKeychainInvalidBlobFallsThrough(t *testing.T) {
	r := &Resolver{
		GOOS:        "darwin",
		Getenv:      envFrom(nil),
		Keychain:    func() ([]byte, error) { return []byte("garbage"), nil },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
	if _, err := r.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveFile(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, sampleBlob)
	r := &Resolver{
		GOOS:        "linux",
		Getenv:      envFrom(nil),
		UserHomeDir: func() (string, error) { return dir, nil },
		ReadFile:    os.ReadFile,
	}
	c, err := r.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := filepath.Join(dir, ".claude", ".credentials.json")
	if c.Source != want {
		t.Errorf("source = %q, want %q", c.Source, want)
	}
}

func TestResolveFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeCreds(t, dir, "{not json}")
	r := &Resolver{
		GOOS:        "linux",
		Getenv:      envFrom(nil),
		UserHomeDir: func() (string, error) { return dir, nil },
		ReadFile:    os.ReadFile,
	}
	if _, err := r.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestResolveNotFound(t *testing.T) {
	r := &Resolver{
		GOOS:        "linux",
		Getenv:      envFrom(nil),
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
		ReadFile:    os.ReadFile,
	}
	if _, err := r.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCredentialsExpiresAtTime(t *testing.T) {
	if _, ok := (Credentials{}).ExpiresAtTime(); ok {
		t.Error("zero expiry should report unknown")
	}
	ms := int64(1750000000000)
	c := Credentials{ExpiresAt: ms}
	got, ok := c.ExpiresAtTime()
	if !ok || !got.Equal(time.UnixMilli(ms)) {
		t.Errorf("ExpiresAtTime() = %v, %v", got, ok)
	}
}

func TestMaskedToken(t *testing.T) {
	if got := (Credentials{AccessToken: "short"}).MaskedToken(); got != "***" {
		t.Errorf("short token mask = %q, want ***", got)
	}
	got := (Credentials{AccessToken: "abcdef1234567890"}).MaskedToken()
	if got != "abcdef…7890" {
		t.Errorf("masked token = %q", got)
	}
}

func TestNewWiring(t *testing.T) {
	r := New()
	if r.Getenv == nil || r.ReadFile == nil || r.UserHomeDir == nil || r.Keychain == nil {
		t.Fatal("New() left a dependency unset")
	}
	t.Setenv("CCVIEW_AUTH_TEST", "value")
	if r.Getenv("CCVIEW_AUTH_TEST") != "value" {
		t.Error("New().Getenv is not wired to the real environment")
	}
}

func TestResolveAllNilDependencies(t *testing.T) {
	// A resolver with no function dependencies wired must not panic and must
	// report ErrNotFound, exercising every nil guard in Resolve.
	r := &Resolver{GOOS: "darwin"}
	if _, err := r.Resolve(); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestReadKeychainExecutes(_ *testing.T) {
	// On non-darwin CI the "security" binary is absent, so this exercises the
	// error path without asserting a specific outcome.
	_, _ = readKeychain()
}

func TestParseRaw(t *testing.T) {
	if _, ok := parseRaw([]byte(sampleBlob), "x"); !ok {
		t.Error("valid blob should parse")
	}
	if _, ok := parseRaw([]byte("not json"), "x"); ok {
		t.Error("invalid JSON should not parse")
	}
	// Valid JSON but no access token.
	if _, ok := parseRaw([]byte(`{"claudeAiOauth":{"subscriptionType":"pro"}}`), "x"); ok {
		t.Error("blob without an access token should not parse")
	}
}

func TestKeychainFromRunner(t *testing.T) {
	// Success path trims trailing whitespace.
	out, err := keychainFromRunner(func(string, ...string) ([]byte, error) {
		return []byte("  blob\n"), nil
	})
	if err != nil || string(out) != "blob" {
		t.Errorf("keychainFromRunner success = %q, %v", out, err)
	}
	// Error path is propagated.
	if _, err := keychainFromRunner(func(string, ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}); err == nil {
		t.Error("expected error to propagate")
	}
}

func writeCreds(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
