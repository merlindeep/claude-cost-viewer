package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/merlindeep/claude-cost-viewer/internal/auth"
	"github.com/merlindeep/claude-cost-viewer/internal/client"
	"github.com/merlindeep/claude-cost-viewer/internal/render"
	"github.com/merlindeep/claude-cost-viewer/internal/tui"
	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

var refNow = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func sampleUsage() *usage.Usage {
	return &usage.Usage{
		FiveHour:     &usage.Window{Utilization: f(42), ResetsAt: "2026-06-13T15:44:00Z"},
		SevenDay:     &usage.Window{Utilization: f(13), ResetsAt: "2026-06-20T00:00:00Z"},
		SevenDayOpus: &usage.Window{Utilization: f(90), ResetsAt: "2026-06-20T00:00:00Z"},
		ExtraUsage:   usage.ExtraUsage{IsEnabled: true, Utilization: f(16), UsedCredits: f(3.2), MonthlyLimit: f(20)},
	}
}

type fakeResolver struct {
	creds auth.Credentials
	err   error
}

func (r *fakeResolver) Resolve() (auth.Credentials, error) { return r.creds, r.err }

// fakeDiagResolver also implements the optional Diagnose() capability that
// runDebug uses to print a per-source breakdown.
type fakeDiagResolver struct {
	fakeResolver
	diags []auth.SourceDiagnostic
}

func (r *fakeDiagResolver) Diagnose() []auth.SourceDiagnostic { return r.diags }

type fakeFetcher struct {
	u     *usage.Usage
	raw   []byte
	err   error
	calls int
}

func (f *fakeFetcher) Fetch(_ context.Context, _ string) (*usage.Usage, []byte, error) {
	f.calls++
	return f.u, f.raw, f.err
}

// scriptResolver returns a sequence of credentials on successive Resolve calls,
// repeating the last entry once the script is exhausted. It models Claude Code
// refreshing the stored token between resolutions. A non-nil entry in errs is
// returned alongside the credentials at the same index.
type scriptResolver struct {
	creds []auth.Credentials
	errs  []error
	calls int
}

func (r *scriptResolver) Resolve() (auth.Credentials, error) {
	i := r.calls
	r.calls++
	if i >= len(r.creds) {
		i = len(r.creds) - 1
	}
	var err error
	if i < len(r.errs) {
		err = r.errs[i]
	}
	return r.creds[i], err
}

// tokenFetcher returns HTTP 401 for any token marked expired and a successful
// snapshot otherwise, so tests can model a token that starts stale and is later
// reloaded.
type tokenFetcher struct {
	u       *usage.Usage
	expired map[string]bool
	calls   int
}

func (f *tokenFetcher) Fetch(_ context.Context, token string) (*usage.Usage, []byte, error) {
	f.calls++
	if f.expired[token] {
		return nil, nil, &client.APIError{Status: 401, Body: "expired"}
	}
	return f.u, []byte(`{"ok":true}`), nil
}

// statusTokenFetcher returns an APIError with the configured status for any
// token marked bad, and a successful snapshot otherwise. It models a token the
// endpoint rejects (for example a 429 for an expired token) that is later
// reloaded to a working one.
type statusTokenFetcher struct {
	u      *usage.Usage
	status int
	bad    map[string]bool
	calls  int
}

func (f *statusTokenFetcher) Fetch(_ context.Context, token string) (*usage.Usage, []byte, error) {
	f.calls++
	if f.bad[token] {
		return nil, nil, &client.APIError{Status: f.status, Body: "rejected"}
	}
	return f.u, []byte(`{"ok":true}`), nil
}

func newTestDeps() (deps, *fakeResolver, *fakeFetcher, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errb := &bytes.Buffer{}
	res := &fakeResolver{creds: auth.Credentials{AccessToken: "tok-1234567890", Source: "test", Plan: "max_20x"}}
	fet := &fakeFetcher{u: sampleUsage(), raw: []byte(`{"ok":true}`)}
	d := deps{
		Resolver:    res,
		NewFetcher:  func(string) fetcher { return fet },
		Version:     func() string { return "9.9.9" },
		Now:         func() time.Time { return refNow },
		Sleep:       func(context.Context, time.Duration) bool { return true },
		RunTUI:      func(context.Context, tui.Config) error { return nil },
		Reload:      func(context.Context) error { return nil },
		ClearScreen: false,
		Out:         out,
		Err:         errb,
		MockFile:    func() string { return "" },
		MockPlan:    func() string { return "" },
		ReadFile:    os.ReadFile,
	}
	return d, res, fet, out, errb
}

func opts(mode render.Mode, once bool) runOptions {
	return runOptions{Interval: time.Minute, Once: once, Mode: mode, Color: false}
}

// --- runWatch ---------------------------------------------------------------

func TestRunWatchSuccessOnce(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	if fet.calls != 1 {
		t.Errorf("fetch calls = %d, want 1", fet.calls)
	}
	if !strings.Contains(out.String(), "Claude usage  Max 20x") || !strings.Contains(out.String(), "Session") {
		t.Errorf("output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Ctrl+C") {
		t.Error("footer should be suppressed for --once")
	}
}

func TestRunWatchLoopInterrupted(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Sleep = func(context.Context, time.Duration) bool { return false } // interrupt after first iteration
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Ctrl+C to quit") {
		t.Error("loop mode should print the footer")
	}
}

func TestRunWatchContextCancelled(t *testing.T) {
	d, _, fet, _, _ := newTestDeps()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runWatch(ctx, d, opts(render.ModeCompact, false)); err != nil {
		t.Fatal(err)
	}
	if fet.calls != 0 {
		t.Error("cancelled context should short-circuit before fetching")
	}
}

func TestRunWatchNoToken(t *testing.T) {
	d, res, _, out, _ := newTestDeps()
	res.err = auth.ErrNotFound
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No Claude Code OAuth token found.") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunWatchUnauthorized(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = &client.APIError{Status: 401, Body: "unauthorized"}
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if !strings.Contains(out.String(), "HTTP 401") {
		t.Errorf("output:\n%s", out.String())
	}
}

// On a 401 the watcher re-resolves through the standard chain (where Claude Code
// may have reloaded the token) and retries once, recovering silently.
func TestRunWatchReauthSucceeds(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale-token"},
		{AccessToken: "fresh-token", Plan: "max_20x"},
	}}
	fet := &tokenFetcher{u: sampleUsage(), expired: map[string]bool{"stale-token": true}}
	d.NewFetcher = func(string) fetcher { return fet }

	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	if fet.calls != 2 {
		t.Errorf("fetch calls = %d, want 2 (stale + retry)", fet.calls)
	}
	// The plan label proves the reloaded credentials were used downstream.
	if !strings.Contains(out.String(), "Claude usage  Max 20x") {
		t.Errorf("expected a successful render from the reloaded token:\n%s", out.String())
	}
	if strings.Contains(out.String(), "401") {
		t.Errorf("a silent re-auth should not surface a 401:\n%s", out.String())
	}
}

// When re-resolution returns the same (still-stale) token, the retry is skipped
// and an informative message is printed.
func TestRunWatchReauthSameTokenSkipsRetry(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = &client.APIError{Status: 401, Body: "expired"}
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 1 {
		t.Errorf("an unchanged token should not be retried, got %d calls", fet.calls)
	}
	if !strings.Contains(out.String(), "re-authentication failed") || !strings.Contains(out.String(), "try again") {
		t.Errorf("expected an informative message:\n%s", out.String())
	}
}

// A freshly resolved token that is also rejected falls through to the message.
func TestRunWatchReauthFreshTokenStillUnauthorized(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale"}, {AccessToken: "also-bad"},
	}}
	fet := &tokenFetcher{u: sampleUsage(), expired: map[string]bool{"stale": true, "also-bad": true}}
	d.NewFetcher = func(string) fetcher { return fet }
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 2 {
		t.Errorf("fetch calls = %d, want 2", fet.calls)
	}
	if !strings.Contains(out.String(), "re-authentication failed") {
		t.Errorf("expected an informative message after a failed retry:\n%s", out.String())
	}
}

// If re-resolution itself fails, the retry fetch is skipped and the original 401
// is surfaced informatively.
func TestRunWatchReauthResolveFails(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{
		creds: []auth.Credentials{{AccessToken: "stale"}, {}},
		errs:  []error{nil, auth.ErrNotFound},
	}
	fet := &tokenFetcher{u: sampleUsage(), expired: map[string]bool{"stale": true}}
	d.NewFetcher = func(string) fetcher { return fet }
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 1 {
		t.Errorf("a failed re-resolution should skip the retry fetch, got %d calls", fet.calls)
	}
	if !strings.Contains(out.String(), "re-authentication failed") {
		t.Errorf("expected an informative message:\n%s", out.String())
	}
}

func TestRunWatchRateLimited(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = &client.APIError{Status: 429, Body: "slow down"}
	// Non-once with interrupt to exercise the backoff sleep branch.
	d.Sleep = func(context.Context, time.Duration) bool { return false }
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, false))
	if !strings.Contains(out.String(), "Rate limited (HTTP 429)") {
		t.Errorf("output:\n%s", out.String())
	}
}

// An expired token is rejected by the endpoint with 429 (not 401). When
// re-resolution yields the same still-expired token, the watcher reports it as
// an expired-token problem rather than backing off as if rate limited.
func TestRunWatchExpiredTokenSurfacedAsAuth(t *testing.T) {
	d, res, fet, out, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429, Body: "rate_limit_error"}
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 1 {
		t.Errorf("an unchanged expired token should not be retried, got %d calls", fet.calls)
	}
	if !strings.Contains(out.String(), "Token expired and re-authentication failed (HTTP 429)") {
		t.Errorf("expected an expired-token notice:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Rate limited") {
		t.Errorf("an expired token must not be reported as rate limiting:\n%s", out.String())
	}
}

// On a 429 caused by an expired token, the watcher re-resolves (where Claude
// Code may have reloaded the token in the background) and retries once,
// recovering silently.
func TestRunWatchExpiredTokenRefreshSucceeds(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale-token", ExpiresAt: refNow.Add(-time.Minute).UnixMilli()},
		{AccessToken: "fresh-token", Plan: "max_20x"},
	}}
	fet := &statusTokenFetcher{u: sampleUsage(), status: 429, bad: map[string]bool{"stale-token": true}}
	d.NewFetcher = func(string) fetcher { return fet }

	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	if fet.calls != 2 {
		t.Errorf("fetch calls = %d, want 2 (stale 429 + retry)", fet.calls)
	}
	if !strings.Contains(out.String(), "Claude usage  Max 20x") {
		t.Errorf("expected a successful render from the reloaded token:\n%s", out.String())
	}
	if strings.Contains(out.String(), "429") || strings.Contains(out.String(), "Rate limited") {
		t.Errorf("a silent recovery should not surface a 429:\n%s", out.String())
	}
}

// If the reloaded token is valid (not expired) but the endpoint still returns
// 429, that is genuine rate limiting and is reported as such.
func TestRunWatchExpiredTokenRefreshStillRateLimited(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale-token", ExpiresAt: refNow.Add(-time.Minute).UnixMilli()},
		{AccessToken: "fresh-token"},
	}}
	fet := &statusTokenFetcher{u: sampleUsage(), status: 429, bad: map[string]bool{"stale-token": true, "fresh-token": true}}
	d.NewFetcher = func(string) fetcher { return fet }
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 2 {
		t.Errorf("fetch calls = %d, want 2", fet.calls)
	}
	if !strings.Contains(out.String(), "Rate limited (HTTP 429)") {
		t.Errorf("a non-expired token that is still 429 is genuine rate limiting:\n%s", out.String())
	}
}

// A 429 while the token is still valid (a future expiry) is genuine rate
// limiting: the watcher backs off and does not attempt a re-auth retry.
func TestRunWatchRateLimitedTokenStillValid(t *testing.T) {
	d, res, fet, out, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(30 * time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429, Body: "slow down"}
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if fet.calls != 1 {
		t.Errorf("a valid token must not trigger a re-auth retry, got %d calls", fet.calls)
	}
	if !strings.Contains(out.String(), "Rate limited (HTTP 429)") {
		t.Errorf("output:\n%s", out.String())
	}
}

// --- auto-reload -----------------------------------------------------------

// With --auto-reload-expired-token, an expired token triggers a single Claude Code refresh;
// the freshly resolved token then succeeds and the loop recovers silently.
func TestRunWatchAutoReloadTokenRecovers(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale", ExpiresAt: refNow.Add(-time.Minute).UnixMilli()},
		{AccessToken: "fresh", Plan: "max_20x"},
	}}
	fet := &statusTokenFetcher{u: sampleUsage(), status: 429, bad: map[string]bool{"stale": true}}
	d.NewFetcher = func(string) fetcher { return fet }
	reloads := 0
	d.Reload = func(context.Context) error { reloads++; return nil }

	o := opts(render.ModeCompact, true)
	o.AutoReloadToken = true
	if err := runWatch(context.Background(), d, o); err != nil {
		t.Fatal(err)
	}
	if reloads != 1 {
		t.Errorf("reload attempts = %d, want 1", reloads)
	}
	if !strings.Contains(out.String(), "running Claude Code once to reload") {
		t.Errorf("expected a refresh notice:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Claude usage  Max 20x") {
		t.Errorf("expected a successful render after refresh:\n%s", out.String())
	}
}

// With --auto-reload-expired-token, if the refresh does not yield a working token, the watch
// still reports the expiry (and, thanks to the cooldown, will not retry yet).
func TestRunWatchAutoReloadTokenStillExpired(t *testing.T) {
	d, res, fet, out, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429, Body: "rate_limit_error"}
	reloads := 0
	d.Reload = func(context.Context) error { reloads++; return errors.New("claude failed") }

	o := opts(render.ModeCompact, true)
	o.AutoReloadToken = true
	_ = runWatch(context.Background(), d, o)
	if reloads != 1 {
		t.Errorf("reload attempts = %d, want 1", reloads)
	}
	if !strings.Contains(out.String(), "Token expired and re-authentication failed (HTTP 429)") {
		t.Errorf("expected the expired-token notice after a failed refresh:\n%s", out.String())
	}
}

// The 5-minute cooldown means a tight poll loop reloads at most once (the
// injected clock is fixed, so later iterations fall inside the window).
func TestRunWatchAutoReloadTokenCooldown(t *testing.T) {
	d, res, fet, _, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429}
	reloads := 0
	d.Reload = func(context.Context) error { reloads++; return nil }
	d.Sleep = sleepNTimes(2) // three iterations

	o := opts(render.ModeCompact, false)
	o.AutoReloadToken = true
	_ = runWatch(context.Background(), d, o)
	if reloads != 1 {
		t.Errorf("the cooldown should permit a single refresh, got %d", reloads)
	}
}

func TestReloadCmdline(t *testing.T) {
	if got := reloadCmdline(func(string) string { return "" }); got != defaultReloadCmd {
		t.Errorf("empty env should use the default, got %q", got)
	}
	if got := reloadCmdline(func(string) string { return "  my-cmd --x  " }); got != "my-cmd --x" {
		t.Errorf("override = %q", got)
	}
}

func TestRunReload(t *testing.T) {
	// Harmless overrides keep the test off the network and away from claude.
	if err := runReload(context.Background(), func(string) string { return "exit 0" }); err != nil {
		t.Errorf("a zero-exit refresh command should succeed: %v", err)
	}
	if err := runReload(context.Background(), func(string) string { return "exit 3" }); err == nil {
		t.Error("a non-zero refresh command should report an error")
	}
}

func TestRunWatchGenericError(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = errors.New("connection reset")
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, true))
	if !strings.Contains(out.String(), "Error: connection reset") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunWatchMachineModeErrorsToStderr(t *testing.T) {
	d, res, _, out, errb := newTestDeps()
	res.err = auth.ErrNotFound
	if err := runWatch(context.Background(), d, opts(render.ModeJSON, true)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should stay clean in JSON mode, got: %s", out.String())
	}
	if !strings.Contains(errb.String(), "No Claude Code OAuth token found.") {
		t.Errorf("stderr:\n%s", errb.String())
	}
}

func TestRunWatchMock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mock.json")
	if err := os.WriteFile(path, []byte(`{"five_hour":{"utilization":50,"resets_at":"2026-06-13T15:00:00Z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _, fet, out, _ := newTestDeps()
	d.MockFile = func() string { return path }
	d.MockPlan = func() string { return "pro" }
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	if fet.calls != 0 {
		t.Error("mock mode must not hit the fetcher")
	}
	if !strings.Contains(out.String(), "Claude usage  Pro") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunWatchRenderErrorPropagates(t *testing.T) {
	d, _, _, _, _ := newTestDeps()
	d.Out = errWriter{}
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, true)); err == nil {
		t.Error("render write error should propagate")
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// sleepNTimes returns a Sleep stub that elapses fully n times, then reports an
// interruption. This drives the watch loop through n+1 iterations so the
// per-branch "continue" statements are exercised.
func sleepNTimes(n int) func(context.Context, time.Duration) bool {
	calls := 0
	return func(context.Context, time.Duration) bool {
		calls++
		return calls <= n
	}
}

func TestRunWatchClearsScreen(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.ClearScreen = true
	d.Sleep = func(context.Context, time.Duration) bool { return false }
	if err := runWatch(context.Background(), d, opts(render.ModeCompact, false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\033[H\033[2J") {
		t.Error("expected screen-clear sequence in loop mode on a terminal")
	}
}

func TestRunWatchNoTokenLoop(t *testing.T) {
	d, res, _, out, _ := newTestDeps()
	res.err = auth.ErrNotFound
	d.Sleep = sleepNTimes(1)
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, false))
	if !strings.Contains(out.String(), "No Claude Code OAuth token found.") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunWatchUnauthorizedLoop(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = &client.APIError{Status: 401}
	d.Sleep = sleepNTimes(1)
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, false))
	if !strings.Contains(out.String(), "HTTP 401") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunWatchMockLoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mock.json")
	_ = os.WriteFile(path, []byte(`{"five_hour":{"utilization":50,"resets_at":"2026-06-13T15:00:00Z"}}`), 0o600)
	d, _, _, out, _ := newTestDeps()
	d.MockFile = func() string { return path }
	d.Sleep = sleepNTimes(1)
	_ = runWatch(context.Background(), d, opts(render.ModeCompact, false))
	if !strings.Contains(out.String(), "Claude usage") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestColorize(t *testing.T) {
	if got := colorize(true, cRed, "x"); !strings.Contains(got, cRed) || !strings.Contains(got, "x") {
		t.Errorf("coloured = %q", got)
	}
	if got := colorize(false, cRed, "x"); got != "x" {
		t.Errorf("disabled = %q", got)
	}
	if got := colorize(true, "", "x"); got != "x" {
		t.Errorf("empty code = %q", got)
	}
}

func TestDefaultDeps(t *testing.T) {
	out := &bytes.Buffer{}
	errb := &bytes.Buffer{}
	d := defaultDeps(out, errb)
	if d.Resolver == nil || d.NewFetcher == nil || d.Version == nil || d.RunTUI == nil || d.Reload == nil {
		t.Fatal("defaultDeps left a dependency unset")
	}
	// Exercise the closures so they are covered and wired correctly.
	if d.NewFetcher("1.0.0") == nil {
		t.Error("NewFetcher returned nil")
	}
	if d.Version() == "" {
		t.Error("Version returned empty")
	}
	_ = d.MockFile()
	_ = d.MockPlan()
	if d.Now().IsZero() {
		t.Error("Now returned zero time")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.Sleep(ctx, 0) {
		t.Error("Sleep with cancelled context should return false")
	}
	// Exercise Reload with a harmless override so it never spawns claude.
	t.Setenv("CCVIEW_RELOAD_CMD", "exit 0")
	if err := d.Reload(context.Background()); err != nil {
		t.Errorf("Reload with a harmless override should succeed: %v", err)
	}
}

// --- runDebug ---------------------------------------------------------------

func TestRunDebugSuccess(t *testing.T) {
	d, res, _, out, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(30 * time.Minute).UnixMilli()
	if err := runDebug(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ccview --debug", "User-Agent: claude-code/9.9.9", "creds source: test", "token: tok-12", "plan: max_20x (Max 20x)", "status: OK", "Claude usage", "expiresAt:", "left)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("debug output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunDebugNoToken(t *testing.T) {
	d, res, _, out, _ := newTestDeps()
	res.err = auth.ErrNotFound
	_ = runDebug(context.Background(), d, opts(render.ModeCompact, true))
	if !strings.Contains(out.String(), "creds: NOT FOUND") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRunDebugSourceBreakdown(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	d.Resolver = &fakeDiagResolver{
		fakeResolver: fakeResolver{err: auth.ErrNotFound},
		diags: []auth.SourceDiagnostic{
			{Name: "env CLAUDE_CODE_OAUTH_TOKEN", Detail: "not set"},
			{Name: `macOS Keychain "Claude Code-credentials"`, Detail: "not found or unreadable"},
			{Name: "~/.claude/.credentials.json", Detail: "missing"},
		},
	}
	if err := runDebug(context.Background(), d, opts(render.ModeCompact, true)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"credential sources (priority order):",
		"1. env CLAUDE_CODE_OAUTH_TOKEN — not set",
		`2. macOS Keychain "Claude Code-credentials" — not found or unreadable`,
		"3. ~/.claude/.credentials.json — missing",
		"creds: NOT FOUND",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("debug breakdown missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunDebugFetchError(t *testing.T) {
	d, _, fet, out, _ := newTestDeps()
	fet.err = &client.APIError{Status: 500, Body: "boom"}
	_ = runDebug(context.Background(), d, opts(render.ModeCompact, true))
	if !strings.Contains(out.String(), "error:") || !strings.Contains(out.String(), "raw:") {
		t.Errorf("output:\n%s", out.String())
	}
}

// --- runTUI -----------------------------------------------------------------

func TestRunTUIFetchClosures(t *testing.T) {
	d, res, fet, _, _ := newTestDeps()
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }

	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if captured.Fetch == nil {
		t.Fatal("RunTUI did not receive a fetch function")
	}
	// Success.
	if r := captured.Fetch(); r.Err != nil || r.Usage == nil || r.Plan != "Max 20x" {
		t.Errorf("fetch success = %+v", r)
	}
	// Credential error.
	res.err = auth.ErrNotFound
	if r := captured.Fetch(); r.Err == nil {
		t.Error("expected credential error from fetch")
	}
	res.err = nil
	// Fetch error.
	fet.err = errors.New("network")
	if r := captured.Fetch(); r.Err == nil {
		t.Error("expected fetch error")
	}
}

func TestRunTUIMockFetch(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, []byte(`{"five_hour":{"utilization":10,"resets_at":"2026-06-13T15:00:00Z"}}`), 0o600)

	d, _, _, _, _ := newTestDeps()
	d.MockFile = func() string { return good }
	d.MockPlan = func() string { return "pro" }
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }
	_ = runTUI(context.Background(), d, runOptions{Interval: time.Minute})

	if r := captured.Fetch(); r.Err != nil || r.Plan != "Pro" {
		t.Errorf("mock fetch = %+v", r)
	}
	// Missing mock file -> error.
	d.MockFile = func() string { return filepath.Join(dir, "missing.json") }
	_ = runTUI(context.Background(), d, runOptions{Interval: time.Minute})
	if r := captured.Fetch(); r.Err == nil {
		t.Error("expected error for missing mock file")
	}
	// Malformed mock file -> parse error.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o600)
	d.MockFile = func() string { return bad }
	_ = runTUI(context.Background(), d, runOptions{Interval: time.Minute})
	if r := captured.Fetch(); r.Err == nil {
		t.Error("expected parse error for malformed mock file")
	}
}

// A 401 surfaced to the TUI is translated into a friendly, actionable message
// rather than the raw "HTTP 401" error.
func TestRunTUIReauthMessage(t *testing.T) {
	d, _, fet, _, _ := newTestDeps()
	fet.err = &client.APIError{Status: 401}
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }
	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	r := captured.Fetch()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "try again") {
		t.Errorf("expected a friendly re-auth error, got %v", r.Err)
	}
}

// A 429 caused by an expired token is translated by the TUI into the same
// friendly re-auth message as a 401.
func TestRunTUIExpiredTokenMessage(t *testing.T) {
	d, res, fet, _, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429}
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }
	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	r := captured.Fetch()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "try again") {
		t.Errorf("expected a friendly re-auth error for an expired-token 429, got %v", r.Err)
	}
}

// With --auto-reload-expired-token, the TUI fetch reloads an expired token via
// Claude Code and then succeeds with the refreshed credentials.
func TestRunTUIAutoReloadRecovers(t *testing.T) {
	d, _, _, _, _ := newTestDeps()
	d.Resolver = &scriptResolver{creds: []auth.Credentials{
		{AccessToken: "stale", ExpiresAt: refNow.Add(-time.Minute).UnixMilli()},
		{AccessToken: "fresh", Plan: "max_20x"},
	}}
	fet := &statusTokenFetcher{u: sampleUsage(), status: 429, bad: map[string]bool{"stale": true}}
	d.NewFetcher = func(string) fetcher { return fet }
	reloads := 0
	d.Reload = func(context.Context) error { reloads++; return nil }
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }

	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute, AutoReloadToken: true}); err != nil {
		t.Fatal(err)
	}
	r := captured.Fetch()
	if reloads != 1 {
		t.Errorf("reload attempts = %d, want 1", reloads)
	}
	if r.Err != nil || r.Plan != "Max 20x" {
		t.Errorf("expected a successful fetch after reload, got %+v", r)
	}
}

// The 5-minute cooldown applies in the TUI too: repeated fetches reload once.
func TestRunTUIAutoReloadCooldown(t *testing.T) {
	d, res, fet, _, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	fet.err = &client.APIError{Status: 429}
	reloads := 0
	d.Reload = func(context.Context) error { reloads++; return nil }
	var captured tui.Config
	d.RunTUI = func(_ context.Context, cfg tui.Config) error { captured = cfg; return nil }

	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute, AutoReloadToken: true}); err != nil {
		t.Fatal(err)
	}
	_ = captured.Fetch()
	_ = captured.Fetch()
	if reloads != 1 {
		t.Errorf("the cooldown should permit a single reload across fetches, got %d", reloads)
	}
}

func TestRunTUIPropagatesError(t *testing.T) {
	d, _, _, _, _ := newTestDeps()
	d.RunTUI = func(context.Context, tui.Config) error { return errors.New("tui failed") }
	if err := runTUI(context.Background(), d, runOptions{Interval: time.Minute}); err == nil {
		t.Error("expected RunTUI error to propagate")
	}
}

// --- Cobra integration ------------------------------------------------------

func execRoot(d deps, args ...string) error {
	root := newRootCmd(d)
	root.SetArgs(args)
	return root.ExecuteContext(context.Background())
}

func TestRootCompactOnce(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	if err := execRoot(d, "--once"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Claude usage") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestRootModes(t *testing.T) {
	cases := map[string][]string{
		"table":   {"--once", "--mode", "table"},
		"json":    {"--once", "--json"},
		"oneline": {"--once", "--mode", "oneline"},
	}
	wants := map[string]string{"table": "METER", "json": "\"meters\"", "oneline": "Claude 5h:"}
	for name, args := range cases {
		d, _, _, out, _ := newTestDeps()
		if err := execRoot(d, args...); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out.String(), wants[name]) {
			t.Errorf("%s output missing %q:\n%s", name, wants[name], out.String())
		}
	}
}

func TestRootDebugFlag(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	if err := execRoot(d, "--debug"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ccview --debug") {
		t.Errorf("output:\n%s", out.String())
	}
}

// The --auto-reload-expired-token flag is wired through to runWatch, which triggers a single
// refresh attempt for an expired token.
func TestRootAutoReloadTokenFlag(t *testing.T) {
	d, res, _, _, _ := newTestDeps()
	res.creds.ExpiresAt = refNow.Add(-time.Minute).UnixMilli()
	reloaded := false
	d.Reload = func(context.Context) error { reloaded = true; return nil }
	if err := execRoot(d, "--auto-reload-expired-token", "--once"); err != nil {
		t.Fatal(err)
	}
	if !reloaded {
		t.Error("--auto-reload-expired-token should trigger a refresh for an expired token")
	}
}

func TestRootTUIFlag(t *testing.T) {
	d, _, _, _, errb := newTestDeps()
	called := false
	d.RunTUI = func(context.Context, tui.Config) error { called = true; return nil }
	if err := execRoot(d, "--mode", "tui", "--interval", "30s"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("tui mode should call RunTUI")
	}
	if !strings.Contains(errb.String(), "warning: interval 30s") {
		t.Errorf("expected fast-interval warning, stderr:\n%s", errb.String())
	}
}

func TestRootLegacyPositionalInterval(t *testing.T) {
	d, _, _, _, errb := newTestDeps()
	if err := execRoot(d, "45", "--once"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "interval 45s") {
		t.Errorf("expected warning for 45s, stderr:\n%s", errb.String())
	}
}

func TestRootInvalidPositional(t *testing.T) {
	d, _, _, _, _ := newTestDeps()
	if err := execRoot(d, "notanumber"); err == nil {
		t.Error("expected error for non-numeric positional interval")
	}
}

func TestRootInvalidMode(t *testing.T) {
	d, _, _, _, _ := newTestDeps()
	if err := execRoot(d, "--mode", "bogus", "--once"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestVersionSubcommand(t *testing.T) {
	d, _, _, out, _ := newTestDeps()
	if err := execRoot(d, "version"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ccview") {
		t.Errorf("version output:\n%s", out.String())
	}
}

func TestRunFunction(t *testing.T) {
	out := &bytes.Buffer{}
	errb := &bytes.Buffer{}
	if code := run([]string{"version"}, out, errb); code != 0 {
		t.Errorf("run(version) = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "ccview") {
		t.Errorf("run output:\n%s", out.String())
	}
	// Invalid mode -> exit code 1 with a message on stderr.
	out.Reset()
	errb.Reset()
	if code := run([]string{"--mode", "bogus", "--once"}, out, errb); code != 1 {
		t.Errorf("run(bad mode) = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "invalid mode") {
		t.Errorf("stderr:\n%s", errb.String())
	}
}

// --- helpers ----------------------------------------------------------------

func TestResolveInterval(t *testing.T) {
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Run: func(*cobra.Command, []string) {}}
		c.Flags().Duration("interval", time.Minute, "")
		return c
	}
	// Positional seconds.
	if got, err := resolveInterval(newCmd(), []string{"45"}, time.Minute); err != nil || got != 45*time.Second {
		t.Errorf("positional = %v, %v", got, err)
	}
	// No args -> flag value.
	if got, _ := resolveInterval(newCmd(), nil, 2*time.Minute); got != 2*time.Minute {
		t.Errorf("no-arg = %v", got)
	}
	// Invalid / non-positive.
	if _, err := resolveInterval(newCmd(), []string{"abc"}, time.Minute); err == nil {
		t.Error("expected error for non-numeric")
	}
	if _, err := resolveInterval(newCmd(), []string{"0"}, time.Minute); err == nil {
		t.Error("expected error for zero")
	}
	// Explicit --interval wins over positional.
	c := newCmd()
	_ = c.Flags().Set("interval", "2m")
	if got, _ := resolveInterval(c, []string{"45"}, 2*time.Minute); got != 2*time.Minute {
		t.Errorf("flag precedence = %v, want 2m", got)
	}
}

func TestWantColor(t *testing.T) {
	if wantColor(&bytes.Buffer{}, true) {
		t.Error("--no-color should disable colour")
	}
	if wantColor(&bytes.Buffer{}, false) {
		t.Error("non-terminal writer should not get colour")
	}
	t.Setenv("NO_COLOR", "1")
	if wantColor(&bytes.Buffer{}, false) {
		t.Error("NO_COLOR should disable colour")
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("buffer is not a terminal")
	}
	// Exercise the *os.File branch (a pipe is not a terminal).
	_ = isTerminal(os.Stdout)
}

func TestWarnIfFast(t *testing.T) {
	var b bytes.Buffer
	warnIfFast(&b, 30*time.Second, false)
	if !strings.Contains(b.String(), "warning") {
		t.Error("expected warning below threshold")
	}
	b.Reset()
	warnIfFast(&b, time.Minute, false)
	if b.Len() != 0 {
		t.Errorf("no warning expected at threshold, got %q", b.String())
	}
}

func TestHumanLeft(t *testing.T) {
	if humanLeft(-time.Second) != "expired" {
		t.Error("negative duration should be 'expired'")
	}
	if got := humanLeft(90 * time.Minute); got != "90m left" {
		t.Errorf("humanLeft = %q", got)
	}
}

func TestWriteFooter(t *testing.T) {
	var b bytes.Buffer
	writeFooter(&b, runOptions{Interval: time.Minute, Once: true}, refNow, "")
	if b.Len() != 0 {
		t.Error("footer should be suppressed for --once")
	}
	writeFooter(&b, runOptions{Interval: time.Minute}, refNow, "[mock]")
	if !strings.Contains(b.String(), "[mock]") || !strings.Contains(b.String(), "Ctrl+C") {
		t.Errorf("footer = %q", b.String())
	}
}

func TestSleepCtx(t *testing.T) {
	if !sleepCtx(context.Background(), 0) {
		t.Error("zero duration with live context should return true")
	}
	if !sleepCtx(context.Background(), time.Millisecond) {
		t.Error("short sleep should complete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, time.Hour) {
		t.Error("cancelled context should return false")
	}
	if sleepCtx(ctx, 0) {
		t.Error("zero duration with cancelled context should return false")
	}
}
