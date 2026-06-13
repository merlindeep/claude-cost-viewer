package render

import (
	"fmt"
	"sort"
	"strings"
)

// Mode identifies an output format.
type Mode string

const (
	// ModeCompact is the at-a-glance bar view (the default).
	ModeCompact Mode = "compact"
	// ModeTable is an aligned, column-oriented view.
	ModeTable Mode = "table"
	// ModeJSON is machine-readable output.
	ModeJSON Mode = "json"
	// ModeOneline is a single-line view for status bars.
	ModeOneline Mode = "oneline"
)

// allModes lists every selectable non-interactive mode.
var allModes = []Mode{ModeCompact, ModeTable, ModeJSON, ModeOneline}

// Modes returns the list of valid non-interactive mode names.
func Modes() []string {
	out := make([]string, len(allModes))
	for i, m := range allModes {
		out[i] = string(m)
	}
	sort.Strings(out)
	return out
}

// ParseMode validates and normalizes a mode name.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	for _, valid := range allModes {
		if m == valid {
			return m, nil
		}
	}
	return "", fmt.Errorf("invalid mode %q (valid: %s)", s, strings.Join(Modes(), ", "))
}

// String implements fmt.Stringer.
func (m Mode) String() string { return string(m) }
