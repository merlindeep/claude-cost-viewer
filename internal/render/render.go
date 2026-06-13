// Package render turns a usage snapshot into text for the terminal.
//
// Four non-interactive output modes are provided, all built on the normalized
// meters from the usage package so they behave consistently across every plan
// and connection type:
//
//   - compact: the original at-a-glance bar view (the default).
//   - table:   an aligned table with every available column.
//   - json:    machine-readable output for scripting and status bars.
//   - oneline: a single line suitable for tmux / status-bar integrations.
//
// Rendering is deterministic: the reference time is taken from Options.Now, and
// colour can be switched off entirely, so the output is straightforward to test
// with golden assertions.
package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// DefaultBarWidth is the number of cells in a usage bar.
const DefaultBarWidth = 20

// ANSI escape codes used when colour is enabled.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// Options controls rendering.
type Options struct {
	// Color enables ANSI colour codes. Callers should set this based on whether
	// the destination is a terminal and whether NO_COLOR is set.
	Color bool
	// Now is the reference time used to compute "resets in" durations. When
	// zero, time.Now() is used.
	Now time.Time
	// Width is the usage bar width; when <= 0, DefaultBarWidth is used.
	Width int
	// ShowZeroModels includes per-model 7-day windows even at 0% utilization.
	ShowZeroModels bool
	// PlanLabel is the human-readable plan name shown in headers (may be empty).
	PlanLabel string
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

func (o Options) width() int {
	if o.Width <= 0 {
		return DefaultBarWidth
	}
	return o.Width
}

func (o Options) meterOptions() usage.MeterOptions {
	return usage.MeterOptions{IncludeZeroModels: o.ShowZeroModels}
}

// Render writes the usage snapshot to w in the given mode.
func Render(w io.Writer, u *usage.Usage, mode Mode, opt Options) error {
	switch mode {
	case ModeCompact:
		return renderCompact(w, u, opt)
	case ModeTable:
		return renderTable(w, u, opt)
	case ModeJSON:
		return renderJSON(w, u, opt)
	case ModeOneline:
		return renderOneline(w, u, opt)
	default:
		return fmt.Errorf("unknown render mode %q", mode)
	}
}

// colorFor returns the ANSI colour code appropriate for a utilization level:
// green below 50%, yellow below 85%, red at or above 85%.
func colorFor(pct float64) string {
	switch {
	case pct < 50:
		return ansiGreen
	case pct < 85:
		return ansiYellow
	default:
		return ansiRed
	}
}

// wrap applies an ANSI code to text when colour is enabled.
func wrap(color bool, code, text string) string {
	if !color || code == "" {
		return text
	}
	return code + text + ansiReset
}

// bar renders a fixed-width usage bar. Its visible width is always exactly
// width runes regardless of whether colour is enabled, which keeps tables
// aligned.
func bar(pct float64, width int, color bool) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	// pct is clamped to [0,100] above, so filled is always in [0,width].
	filled := int(pct/100*float64(width) + 0.5)
	fill := strings.Repeat("█", filled)
	empty := strings.Repeat("░", width-filled)
	if !color {
		return fill + empty
	}
	return colorFor(pct) + fill + ansiDim + empty + ansiReset
}

// untilText renders the time remaining until t as "in 3h 44m", "in 12m", or
// "now" when t is in the past.
func untilText(now, t time.Time) string {
	d := t.Sub(now)
	if d <= 0 {
		return "now"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("in %dh %02dm", h, m)
	}
	return fmt.Sprintf("in %dm", m)
}

// weeklyText renders a 7-day window reset: relative when it is less than a day
// away, otherwise an absolute local weekday and time.
func weeklyText(now, t time.Time) string {
	if t.Sub(now) < 24*time.Hour {
		return untilText(now, t)
	}
	return t.Local().Format("Mon 15:04")
}

// resetText returns the appropriate reset description for a meter, or an empty
// string when no reset timestamp is available.
func resetText(m usage.Meter, now time.Time) string {
	if !m.HasReset {
		return ""
	}
	switch m.Kind {
	case usage.KindWeekly, usage.KindWeeklyModel:
		return weeklyText(now, m.ResetsAt)
	default:
		return untilText(now, m.ResetsAt)
	}
}

// roundPct rounds a percentage to the nearest integer for display.
func roundPct(pct float64) int {
	return int(pct + 0.5)
}

func writeString(w io.Writer, s string) error {
	_, err := io.WriteString(w, s)
	return err
}
