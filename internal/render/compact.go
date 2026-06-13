package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// renderCompact reproduces the original at-a-glance view: a title with the plan
// name, then one indented bar line per available window. This is the default
// mode and the one intended for the long-running watch loop.
func renderCompact(w io.Writer, u *usage.Usage, opt Options) error {
	var b strings.Builder
	now := opt.now()

	title := wrap(opt.Color, ansiBold+ansiCyan, "Claude usage")
	if opt.PlanLabel != "" {
		title += "  " + wrap(opt.Color, ansiDim, opt.PlanLabel)
	}
	b.WriteString(title + "\n")

	meters := u.Meters(opt.meterOptions())
	if len(meters) == 0 {
		b.WriteString("  " + wrap(opt.Color, ansiDim, "(no usage windows reported)") + "\n")
		return writeString(w, b.String())
	}

	for _, m := range meters {
		b.WriteString(compactLine(m, opt, now) + "\n")
	}
	return writeString(w, b.String())
}

// compactLine formats one meter as:
//
//	"  Session   ████░░░░░░░░░░░░░░░░  42%   resets in 3h 44m"
//
// Per-model windows are indented and lower-cased to match the original layout.
func compactLine(m usage.Meter, opt Options, now time.Time) string {
	label := m.Label
	if m.Kind == usage.KindWeeklyModel {
		label = "  " + strings.ToLower(m.Label)
	}

	barStr := bar(m.Percent, opt.width(), opt.Color)
	pct := fmt.Sprintf("%3d%%", roundPct(m.Percent))
	pct = wrap(opt.Color, colorFor(m.Percent), pct)

	line := fmt.Sprintf("  %-9s %s %s", label, barStr, pct)

	tail := ""
	if r := resetText(m, now); r != "" {
		tail = "resets " + r
	}
	if m.Kind == usage.KindExtra && m.Detail != "" {
		if tail != "" {
			tail += "  "
		}
		tail += m.Detail
	}
	if tail != "" {
		line += "   " + wrap(opt.Color, ansiDim, tail)
	}
	return line
}
