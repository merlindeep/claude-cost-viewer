package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	ClearScreen bool
	Out         io.Writer
	Err         io.Writer
	MockFile    func() string
	MockPlan    func() string
	ReadFile    func(string) ([]byte, error)
}

// runOptions are the resolved, validated options for a single invocation.
type runOptions struct {
	Interval       time.Duration
	Once           bool
	Mode           render.Mode
	Color          bool
	ShowZeroModels bool
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

// runWatch runs the polling loop. With Once set it performs a single iteration.
func runWatch(ctx context.Context, d deps, o runOptions) error {
	ua := d.Version()
	f := d.NewFetcher(ua)
	human := o.isHumanDashboard()
	var backoff time.Duration

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

		u, _, ferr := f.Fetch(ctx, creds.AccessToken)
		if ferr != nil {
			var apiErr *client.APIError
			isAPI := errors.As(ferr, &apiErr)
			switch {
			case isAPI && apiErr.Status == 401:
				fmt.Fprintln(statusW, colorize(o.Color, cRed, "Token expired or invalid (HTTP 401)."))
				fmt.Fprintln(statusW, colorize(o.Color, cDim, "Run any Claude Code command to refresh the token."))
			case isAPI && apiErr.Status == 429:
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
		u, _, ferr := f.Fetch(ctx, creds.AccessToken)
		if ferr != nil {
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
