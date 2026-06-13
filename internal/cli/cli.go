// Package cli wires the command-line interface together with Cobra and hosts
// the run logic for the watch loop, the debug view, and the interactive TUI.
//
// The default invocation (no subcommand) refreshes a compact dashboard on an
// interval, preserving the behaviour of the original prototype:
//
//	ccview                 # refresh every minute, compact view
//	ccview 30              # refresh every 30 seconds (positional, legacy)
//	ccview --interval 2m   # refresh every two minutes
//	ccview --once          # one snapshot and exit
//	ccview --mode table    # alternative output modes: compact|table|json|oneline|tui
//	ccview --debug         # diagnostics
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/merlindeep/claude-cost-viewer/internal/auth"
	"github.com/merlindeep/claude-cost-viewer/internal/buildinfo"
	"github.com/merlindeep/claude-cost-viewer/internal/client"
	"github.com/merlindeep/claude-cost-viewer/internal/render"
	"github.com/merlindeep/claude-cost-viewer/internal/tui"
)

const longDescription = `ccview is a compact console monitor for Claude usage limits.

It shows the same utilization the "/usage" view in Claude Code and the desktop
app report, read from Claude's OAuth usage endpoint:

  - Session   the rolling 5-hour window
  - Weekly    the 7-day window (plus per-model Sonnet/Opus windows on Max plans)
  - Extra     pay-as-you-go extra-usage credits, when enabled

Which rows appear depends on your subscription plan and how it is connected;
ccview renders whatever the endpoint returns.

The OAuth token is read (never written) from, in priority order:
  1. $CLAUDE_CODE_OAUTH_TOKEN
  2. the macOS Keychain entry "Claude Code-credentials"
  3. ~/.claude/.credentials.json`

// Execute is the entry point used by the main package. It runs the CLI and
// exits with the resulting status code.
func Execute() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run builds the root command with real dependencies, wires signal-based
// cancellation, executes it, and returns the process exit code. It is separated
// from [Execute] so it can be exercised in tests without calling os.Exit.
func run(args []string, out, errW io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(defaultDeps(out, errW))
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(errW, "ccview:", err)
		return 1
	}
	return 0
}

// defaultDeps returns dependencies wired to the real host environment.
func defaultDeps(out, errW io.Writer) deps {
	return deps{
		Resolver:    auth.New(),
		NewFetcher:  func(version string) fetcher { return client.New(version) },
		Version:     func() string { return client.DetectClaudeVersionDefault(os.Getenv) },
		Now:         time.Now,
		Sleep:       sleepCtx,
		RunTUI:      tui.Run,
		ClearScreen: isTerminal(out),
		Out:         out,
		Err:         errW,
		MockFile:    func() string { return os.Getenv("CCVIEW_MOCK_FILE") },
		MockPlan:    func() string { return os.Getenv("CCVIEW_MOCK_PLAN") },
		ReadFile:    os.ReadFile,
	}
}

// newRootCmd constructs the root command bound to the given dependencies.
func newRootCmd(d deps) *cobra.Command {
	var (
		interval time.Duration
		once     bool
		debug    bool
		noColor  bool
		showAll  bool
		asJSON   bool
		modeStr  string
	)

	root := &cobra.Command{
		Use:           "ccview [interval-seconds]",
		Short:         "Compact console monitor for Claude usage limits",
		Long:          longDescription,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildinfo.Get().Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := modeStr
			if asJSON {
				mode = string(render.ModeJSON)
			}

			effInterval, err := resolveInterval(cmd, args, interval)
			if err != nil {
				return err
			}

			color := wantColor(d.Out, noColor)
			o := runOptions{
				Interval:       effInterval,
				Once:           once,
				Color:          color,
				ShowZeroModels: showAll,
			}

			// The TUI is selected via mode but does not go through the render
			// package, so handle it before parsing render modes.
			if strings.EqualFold(mode, "tui") {
				warnIfFast(d.Err, effInterval, color)
				return runTUI(cmd.Context(), d, o)
			}

			rmode, err := render.ParseMode(mode)
			if err != nil {
				return err
			}
			o.Mode = rmode

			if debug {
				return runDebug(cmd.Context(), d, o)
			}
			warnIfFast(d.Err, effInterval, color)
			return runWatch(cmd.Context(), d, o)
		},
	}

	root.SetOut(d.Out)
	root.SetErr(d.Err)

	flags := root.Flags()
	flags.DurationVarP(&interval, "interval", "i", time.Minute, "refresh interval (e.g. 30s, 1m, 2m); default is 1 minute")
	flags.BoolVar(&once, "once", false, "fetch a single snapshot and exit")
	flags.BoolVarP(&debug, "debug", "d", false, "print diagnostics: token source, HTTP status, raw response")
	flags.StringVarP(&modeStr, "mode", "m", string(render.ModeCompact),
		fmt.Sprintf("output mode: %s, tui", strings.Join(render.Modes(), ", ")))
	flags.BoolVar(&asJSON, "json", false, "shortcut for --mode json")
	flags.BoolVar(&noColor, "no-color", false, "disable ANSI colours (also honours the NO_COLOR environment variable)")
	flags.BoolVarP(&showAll, "all", "a", false, "show per-model windows even when at 0%")

	root.SetVersionTemplate("ccview {{.Version}}\n")
	root.AddCommand(newVersionCmd(d))
	return root
}

// newVersionCmd prints full build metadata.
func newVersionCmd(d deps) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print detailed version information",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(d.Out, buildinfo.Get().String())
			return err
		},
	}
}

// resolveInterval applies the legacy positional "interval-seconds" argument
// when --interval was not explicitly set.
func resolveInterval(cmd *cobra.Command, args []string, flagValue time.Duration) (time.Duration, error) {
	if len(args) == 1 && !cmd.Flags().Changed("interval") {
		secs, err := strconv.Atoi(strings.TrimSpace(args[0]))
		if err != nil || secs <= 0 {
			return 0, fmt.Errorf("invalid interval %q: expected positive integer seconds (or use --interval)", args[0])
		}
		return time.Duration(secs) * time.Second, nil
	}
	return flagValue, nil
}

// wantColor decides whether to emit ANSI colour: never when --no-color or
// NO_COLOR is set, otherwise only when the output is a terminal.
func wantColor(out io.Writer, noColor bool) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return isTerminal(out)
}

// isTerminal reports whether w is an *os.File attached to a terminal.
func isTerminal(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return isatty.IsTerminal(f.Fd())
	}
	return false
}
