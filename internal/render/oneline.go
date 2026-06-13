package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// onelineLabel produces a terse label for the single-line view: "5h" for the
// session window, "7d" for the weekly window, the lower-cased model name for
// per-model windows, and "extra" for extra usage.
func onelineLabel(m usage.Meter) string {
	switch m.Kind {
	case usage.KindSession:
		return "5h"
	case usage.KindWeekly:
		return "7d"
	case usage.KindExtra:
		return "extra"
	default:
		return strings.ToLower(m.Label)
	}
}

// renderOneline writes a single compact line suitable for embedding in a status
// bar, e.g. "Claude 5h:42% 7d:13% opus:8%". A trailing newline is included.
func renderOneline(w io.Writer, u *usage.Usage, opt Options) error {
	prefix := wrap(opt.Color, ansiCyan, "Claude")
	meters := u.Meters(opt.meterOptions())
	if len(meters) == 0 {
		return writeString(w, prefix+": no data\n")
	}

	parts := make([]string, 0, len(meters))
	for _, m := range meters {
		val := fmt.Sprintf("%d%%", roundPct(m.Percent))
		val = wrap(opt.Color, colorFor(m.Percent), val)
		parts = append(parts, onelineLabel(m)+":"+val)
	}
	return writeString(w, prefix+" "+strings.Join(parts, " ")+"\n")
}
