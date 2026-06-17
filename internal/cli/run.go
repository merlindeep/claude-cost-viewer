package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/auth"
	"github.com/merlindeep/claude-cost-viewer/internal/client"
	"github.com/merlindeep/claude-cost-viewer/internal/render"
	"github.com/merlindeep/claude-cost-viewer/internal/tui"
	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// maxBackoff caps the exponential backoff applied after HTTP 429 responses.
const maxBackoff = 15 * time.Minute

// minRecommendedInterval is the smallest refresh interval that is unlikely to
// trip the endpoint's aggressive rate limiting. Faster intervals are allowed
// but warned about.
const minRecommendedInterval = 60 * time.Second

// Minimal ANSI codes for status/error lines (the render package owns the rest).
const (
	cReset  = "\033[0m"
	cDim    = "\033[2m"
	cRed    = "\033[31m"
	cYellow = "\033[33m"
)

func colorize(enabled bool, code, s string) string {
	if !enabled || code == "" {
		return s
	}
	return code + s + cReset
}

// fetcher fetches a usage snapshot for a token.
type fetcher interface {
	Fetch(ctx context.Context, token string) (*usage.Usage, []byte, error)
}

// credResolver resolves OAuth credentials.
type credResolver interface {
	Resolve() (auth.Credentials, error)
}

// deps holds every external dependency, injected so the run logic is testable
// without a network, terminal, or real credentials.
type deps struct {
	Resolver    credResolver
	NewFetcher  func(version string) fetcher
	Version     func() string
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) bool
	RunTUI      func(ctx context.Context, cfg tui.Config) error
	Reload      func(ctx context.Context) error
	ClearScreen bool
	Out         io.Writer
	Err         io.Writer
	MockFile    func() string
	MockPlan    func() string
	ReadFile    func(string) ([]byte, error)
}

// runOptions are the resolved, validated options for a single invocation.
type runOptions struct {
	Interval        time.Duration
	Once            bool
	Mode            render.Mode
	Color           bool
	ShowZeroModels  bool
	AutoReloadToken bool
}

func (o runOptions) renderOptions(now time.Time, plan string) render.Options {
	return render.Options{
		Color:          o.Color,
		Now:            now,
		ShowZeroModels: o.ShowZeroModels,
		PlanLabel:      plan,
	}
}

// isHumanDashboard reports whether the mode is a full-screen, human-oriented
// view (which clears the screen and prints a footer) rather than machine output.
func (o runOptions) isHumanDashboard() bool {
	return o.Mode == render.ModeCompact || o.Mode == render.ModeTable
}

// isUnauthorized reports whether err is an HTTP 401 from the usage endpoint,
// i.e. the OAuth token was rejected because it has expired or been revoked.
func isUnauthorized(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 401
}

// isRateLimited reports whether err is an HTTP 429 from the usage endpoint. The
// endpoint returns 429 both for genuine rate limiting and to reject a token it
// dislikes — notably an expired token or a foreign User-Agent.
func isRateLimited(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 429
}

// credsExpired reports whether creds carry a known expiry that is at or before
// now. Credentials without expiry information are treated as not expired.
func credsExpired(creds auth.Credentials, now time.Time) bool {
	exp, ok := creds.ExpiresAtTime()
	return ok && !exp.After(now)
}

// shouldReauth reports whether err warrants re-resolving credentials and
// retrying the request once. The endpoint rejects a stale token in one of two
// ways: an explicit HTTP 401, or — for an already-expired token — an HTTP 429
// that is otherwise indistinguishable from real rate limiting. In both cases
// Claude Code may have refreshed the stored token in the background, so a single
// re-resolve is worth attempting.
func shouldReauth(err error, creds auth.Credentials, now time.Time) bool {
	return isUnauthorized(err) || (isRateLimited(err) && credsExpired(creds, now))
}

// fetchWithRetry fetches a usage snapshot for creds. If the endpoint rejects the
// token — with HTTP 401, or with HTTP 429 while the token has already expired —
// it re-resolves credentials through the standard chain (in case Claude Code
// refreshed the token in the background) and retries once with the fresh token.
// The retry is skipped when re-resolution fails or yields the same token (which
// would only earn another rejection).
//
// It returns the credentials actually used, so a successful refresh is reflected
// upstream (for example in the plan label).
func fetchWithRetry(ctx context.Context, d deps, f fetcher, creds auth.Credentials) (*usage.Usage, auth.Credentials, error) {
	u, _, err := f.Fetch(ctx, creds.AccessToken)
	if !shouldReauth(err, creds, d.Now()) {
		return u, creds, err
	}
	fresh, rerr := d.Resolver.Resolve()
	if rerr != nil || fresh.AccessToken == creds.AccessToken {
		return u, creds, err
	}
	u, _, err = f.Fetch(ctx, fresh.AccessToken)
	return u, fresh, err
}

// reloadGate serializes auto-reload attempts and enforces the cooldown between
// them. It is safe for concurrent use: the TUI fetches from multiple goroutines.
type reloadGate struct {
	mu   sync.Mutex
	last time.Time
}

// due reports whether a reload may be attempted at now, recording the attempt
// time when it returns true. It returns false inside the cooldown window.
func (g *reloadGate) due(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.last) < reloadCooldown {
		return false
	}
	g.last = now
	return true
}

// maybeReloadToken asks Claude Code to reload an expired token, at most once per
// reloadCooldown, when --auto-reload-expired-token is set. It returns the
// possibly-refreshed credentials. notify, when non-nil, is called just before a
// reload attempt (the dashboard prints a status line; the TUI passes nil because
// it owns the screen). ccview never reads or writes the token itself — it only
// spawns the helper and re-resolves through the standard chain.
func maybeReloadToken(ctx context.Context, d deps, o runOptions, g *reloadGate, creds auth.Credentials, now time.Time, notify func()) auth.Credentials {
	if !o.AutoReloadToken || !credsExpired(creds, now) || !g.due(now) {
		return creds
	}
	if notify != nil {
		notify()
	}
	_ = d.Reload(ctx)
	if fresh, err := d.Resolver.Resolve(); err == nil {
		creds = fresh
	}
	return creds
}

// runWatch runs the polling loop. With Once set it performs a single iteration.
func runWatch(ctx context.Context, d deps, o runOptions) error {
	ua := d.Version()
	f := d.NewFetcher(ua)
	human := o.isHumanDashboard()
	var backoff time.Duration
	var gate reloadGate

	for {
		if ctx.Err() != nil {
			return nil
		}
		if human && d.ClearScreen && !o.Once {
			_, _ = io.WriteString(d.Out, "\033[H\033[2J")
		}
		now := d.Now()

		// statusW receives human status/error lines. For machine modes it is
		// stderr so it never pollutes stdout.
		statusW := d.Out
		if !human {
			statusW = d.Err
		}

		// Mock mode: render a canned payload from a file (used for demos/tests).
		if mf := d.MockFile(); mf != "" {
			if b, err := d.ReadFile(mf); err == nil {
				if u, perr := usage.Parse(b); perr == nil {
					_ = render.Render(d.Out, u, o.Mode, o.renderOptions(now, usage.ClassifyPlan(d.MockPlan()).Label()))
				}
			}
			if human {
				writeFooter(d.Out, o, now, "[mock]")
			}
			if o.Once {
				return nil
			}
			if !d.Sleep(ctx, o.Interval) {
				return nil
			}
			continue
		}

		creds, err := d.Resolver.Resolve()
		if err != nil {
			fmt.Fprintln(statusW, colorize(o.Color, cRed, "No Claude Code OAuth token found."))
			fmt.Fprintln(statusW, colorize(o.Color, cDim, "Run Claude Code at least once, or export CLAUDE_CODE_OAUTH_TOKEN."))
			if human {
				writeFooter(d.Out, o, now, "")
			}
			if o.Once {
				return nil
			}
			if !d.Sleep(ctx, o.Interval) {
				return nil
			}
			continue
		}

		creds = maybeReloadToken(ctx, d, o, &gate, creds, now, func() {
			fmt.Fprintln(statusW, colorize(o.Color, cDim, "Token expired — running Claude Code once to reload it."))
		})

		u, creds, ferr := fetchWithRetry(ctx, d, f, creds)
		if ferr != nil {
			switch {
			case isUnauthorized(ferr):
				printExpiredTokenNotice(statusW, o.Color, 401)
			case isRateLimited(ferr) && credsExpired(creds, now):
				// An expired token is rejected with 429, not 401. Surface it as
				// an auth problem instead of backing off as if rate limited.
				printExpiredTokenNotice(statusW, o.Color, 429)
			case isRateLimited(ferr):
				backoff = client.NextBackoff(backoff, o.Interval, maxBackoff)
				fmt.Fprintln(statusW, colorize(o.Color, cYellow,
					fmt.Sprintf("Rate limited (HTTP 429) — backing off to %s.", backoff)))
			default:
				fmt.Fprintln(statusW, colorize(o.Color, cRed, "Error: "+ferr.Error()))
			}
			if human {
				writeFooter(d.Out, o, now, "")
			}
			if o.Once {
				return nil
			}
			sleepDur := o.Interval
			if backoff > 0 {
				sleepDur = backoff
			}
			if !d.Sleep(ctx, sleepDur) {
				return nil
			}
			continue
		}

		backoff = 0
		plan := usage.ClassifyPlan(creds.Plan).Label()
		if err := render.Render(d.Out, u, o.Mode, o.renderOptions(now, plan)); err != nil {
			return err
		}
		if human {
			writeFooter(d.Out, o, now, "")
		}
		if o.Once {
			return nil
		}
		if !d.Sleep(ctx, o.Interval) {
			return nil
		}
	}
}

// printExpiredTokenNotice explains that the stored token has expired and could
// not be refreshed, plus how to fix it. status is the HTTP status the endpoint
// used to reject the token: 401, or 429 for a token that was already expired
// (this endpoint rejects expired tokens with 429 rather than 401).
func printExpiredTokenNotice(w io.Writer, color bool, status int) {
	fmt.Fprintln(w, colorize(color, cRed,
		fmt.Sprintf("Token expired and re-authentication failed (HTTP %d).", status)))
	fmt.Fprintln(w, colorize(color, cDim, "Run any Claude Code command to refresh the token, then try again."))
}

// writeFooter prints the status footer for the human dashboard modes. It is a
// no-op for one-shot (--once) runs, keeping single snapshots clean for piping.
func writeFooter(w io.Writer, o runOptions, now time.Time, note string) {
	if o.Once {
		return
	}
	s := fmt.Sprintf("updated %s · every %s · Ctrl+C to quit", now.Format("15:04:05"), o.Interval)
	if note != "" {
		s += " " + note
	}
	fmt.Fprintln(w, "\n"+colorize(o.Color, cDim, s))
}

// runDebug prints diagnostics: the User-Agent, the credential source, a masked
// token, expiry, plan, the HTTP status, and a snippet of the raw response,
// followed by a compact render on success.
func runDebug(ctx context.Context, d deps, o runOptions) error {
	out := d.Out
	ua := d.Version()
	fmt.Fprintln(out, "ccview --debug")
	fmt.Fprintf(out, "User-Agent: claude-code/%s\n", ua)

	// When the resolver supports it, print a per-source breakdown so a failed
	// lookup is self-diagnosable (which sources were tried and what each held).
	if dg, ok := d.Resolver.(interface {
		Diagnose() []auth.SourceDiagnostic
	}); ok {
		fmt.Fprintln(out, "\ncredential sources (priority order):")
		for i, s := range dg.Diagnose() {
			fmt.Fprintf(out, "  %d. %s — %s\n", i+1, s.Name, s.Detail)
		}
		fmt.Fprintln(out)
	}

	creds, err := d.Resolver.Resolve()
	if err != nil {
		fmt.Fprintf(out, "creds: NOT FOUND — %v\n", err)
		return nil
	}
	fmt.Fprintf(out, "creds source: %s\n", creds.Source)
	fmt.Fprintf(out, "token: %s\n", creds.MaskedToken())
	if exp, ok := creds.ExpiresAtTime(); ok {
		fmt.Fprintf(out, "expiresAt: %s (%s)\n", exp.Local().Format("15:04:05"), humanLeft(exp.Sub(d.Now())))
	}
	if creds.Plan != "" {
		fmt.Fprintf(out, "plan: %s (%s)\n", creds.Plan, usage.ClassifyPlan(creds.Plan).Label())
	}

	f := d.NewFetcher(ua)
	u, raw, ferr := f.Fetch(ctx, creds.AccessToken)
	fmt.Fprintf(out, "\nGET %s\n", client.DefaultBaseURL)
	if ferr != nil {
		fmt.Fprintf(out, "error: %v\n", ferr)
		fmt.Fprintf(out, "raw: %s\n", client.Snippet(raw))
		return nil
	}
	fmt.Fprintln(out, "status: OK")
	fmt.Fprintf(out, "raw: %s\n\n", client.Snippet(raw))
	return render.Render(out, u, render.ModeCompact, o.renderOptions(d.Now(), usage.ClassifyPlan(creds.Plan).Label()))
}

func humanLeft(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	return fmt.Sprintf("%dm left", int(d.Minutes()))
}

// runTUI launches the interactive dashboard. The fetch closure reuses the same
// credential/mock resolution as the watch loop.
func runTUI(ctx context.Context, d deps, o runOptions) error {
	ua := d.Version()
	f := d.NewFetcher(ua)
	var gate reloadGate
	fetch := func() tui.Result {
		if mf := d.MockFile(); mf != "" {
			b, err := d.ReadFile(mf)
			if err != nil {
				return tui.Result{Err: err}
			}
			u, perr := usage.Parse(b)
			if perr != nil {
				return tui.Result{Err: perr}
			}
			return tui.Result{Usage: u, Plan: usage.ClassifyPlan(d.MockPlan()).Label()}
		}
		creds, err := d.Resolver.Resolve()
		if err != nil {
			return tui.Result{Err: err}
		}
		creds = maybeReloadToken(ctx, d, o, &gate, creds, d.Now(), nil)
		u, creds, ferr := fetchWithRetry(ctx, d, f, creds)
		if ferr != nil {
			if shouldReauth(ferr, creds, d.Now()) {
				ferr = errors.New("token expired and re-authentication failed — run any Claude Code command to refresh it, then press r to try again")
			}
			return tui.Result{Err: ferr}
		}
		return tui.Result{Usage: u, Plan: usage.ClassifyPlan(creds.Plan).Label()}
	}
	return d.RunTUI(ctx, tui.Config{Fetch: fetch, Interval: o.Interval, ShowZeroModels: o.ShowZeroModels})
}

// warnIfFast prints a warning when the interval is below the recommended floor.
func warnIfFast(w io.Writer, interval time.Duration, color bool) {
	if interval < minRecommendedInterval {
		fmt.Fprintln(w, colorize(color, cYellow, fmt.Sprintf(
			"warning: interval %s is below the recommended %s; the usage endpoint rate-limits aggressively and may return HTTP 429.",
			interval, minRecommendedInterval)))
	}
}

// sleepCtx sleeps for d or until ctx is cancelled, returning true if the full
// duration elapsed and false if interrupted.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Auto-reload of an expired token (opt-in via --auto-reload-expired-token). The watch loop
// asks Claude Code to refresh the stored token instead of merely reporting the
// expiry, at most once per reloadCooldown.
const (
	// reloadCooldown is the minimum delay between auto-refresh attempts.
	reloadCooldown = 5 * time.Minute
	// reloadTimeout bounds a single refresh command invocation.
	reloadTimeout = 30 * time.Second
	// defaultReloadCmd runs when CCVIEW_RELOAD_CMD is unset: a minimal one-shot
	// Claude Code call on the cheapest model, which refreshes the OAuth token as
	// part of its auth bootstrap. Its output is irrelevant and is discarded.
	defaultReloadCmd = "claude -p --model haiku hi"
)

// reloadCmdline returns the shell command used to refresh the token: the
// CCVIEW_RELOAD_CMD override when set, otherwise [defaultReloadCmd].
func reloadCmdline(getenv func(string) string) string {
	if c := strings.TrimSpace(getenv("CCVIEW_RELOAD_CMD")); c != "" {
		return c
	}
	return defaultReloadCmd
}

// runReload executes the refresh command non-interactively with a timeout. The
// command's stdin/stdout/stderr are left nil (the null device), so it cannot
// block on a prompt and cannot pollute ccview's display. Only the side effect —
// Claude Code rewriting the stored token — matters; the token never passes
// through ccview.
func runReload(ctx context.Context, getenv func(string) string) error {
	ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "sh", "-c", reloadCmdline(getenv)).Run()
}
