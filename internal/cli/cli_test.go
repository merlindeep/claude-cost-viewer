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
	if d.Resolver == nil || d.NewFetcher == nil || d.Version == nil || d.RunTUI == nil {
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
