// Package usage models the payload returned by Claude's OAuth usage endpoint
// (GET https://api.anthropic.com/api/oauth/usage) and normalizes it into a
// uniform, render-friendly representation.
//
// The endpoint returns a different subset of fields depending on the account's
// subscription plan and how it is connected:
//
//   - Free / Pro accounts typically expose only the rolling 5-hour session
//     window and, on Pro, the 7-day window.
//   - Max (5x / 20x) accounts additionally expose per-model 7-day windows
//     (Sonnet, Opus) and may expose extra-usage credit information.
//   - Team / Enterprise seats may report a subset of the above.
//
// Rather than special-casing each plan, the renderers consume the output of
// [Usage.Meters], which emits one [Meter] per window that is actually present
// in the payload. This keeps display logic identical across every plan and
// connection type, and makes the tool forward-compatible with windows that may
// be added later.
package usage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Window is a single rate-limit window. Utilization is a percentage in the
// range [0, 100]; it is a pointer so a missing value can be distinguished from
// a genuine zero.
type Window struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// Present reports whether the window carries a usable utilization value.
func (w *Window) Present() bool {
	return w != nil && w.Utilization != nil
}

// Percent returns the utilization percentage, or 0 when the window is absent.
func (w *Window) Percent() float64 {
	if !w.Present() {
		return 0
	}
	return *w.Utilization
}

// ResetTime parses the ResetsAt timestamp. The boolean is false when the field
// is empty or cannot be parsed.
func (w *Window) ResetTime() (time.Time, bool) {
	if w == nil {
		return time.Time{}, false
	}
	return ParseTime(w.ResetsAt)
}

// ExtraUsage describes pay-as-you-go "extra usage" credits that some plans can
// opt into once their included allowance is exhausted.
type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

// Present reports whether extra usage is enabled and carries a utilization
// value worth displaying.
func (e ExtraUsage) Present() bool {
	return e.IsEnabled && e.Utilization != nil
}

// Usage is the decoded usage payload. Every window is optional; absent windows
// are represented by nil pointers and simply omitted from the output.
type Usage struct {
	FiveHour       *Window    `json:"five_hour"`
	SevenDay       *Window    `json:"seven_day"`
	SevenDayOpus   *Window    `json:"seven_day_opus"`
	SevenDaySonnet *Window    `json:"seven_day_sonnet"`
	ExtraUsage     ExtraUsage `json:"extra_usage"`
}

// Parse decodes a usage payload from raw JSON.
func Parse(b []byte) (*Usage, error) {
	var u Usage
	if err := json.Unmarshal(b, &u); err != nil {
		return nil, fmt.Errorf("decode usage payload: %w", err)
	}
	return &u, nil
}

// IsEmpty reports whether the payload contains no displayable window at all.
func (u *Usage) IsEmpty() bool {
	if u == nil {
		return true
	}
	return !u.FiveHour.Present() &&
		!u.SevenDay.Present() &&
		!u.SevenDayOpus.Present() &&
		!u.SevenDaySonnet.Present() &&
		!u.ExtraUsage.Present()
}

// Kind classifies a meter so that renderers can style or group it.
type Kind int

const (
	// KindSession is the rolling 5-hour session window.
	KindSession Kind = iota
	// KindWeekly is the aggregate 7-day window.
	KindWeekly
	// KindWeeklyModel is a per-model 7-day window (e.g. Sonnet or Opus).
	KindWeeklyModel
	// KindExtra is the pay-as-you-go extra-usage meter.
	KindExtra
)

// Meter is a normalized, render-ready view of a single usage window. Renderers
// iterate over the slice returned by [Usage.Meters] without needing to know
// which plan produced it.
type Meter struct {
	// Key is a stable machine identifier (e.g. "five_hour").
	Key string
	// Label is the human-readable name (e.g. "Session").
	Label string
	// Percent is the utilization in the range [0, 100].
	Percent float64
	// Kind classifies the meter for styling and grouping.
	Kind Kind
	// ResetsAt is the moment the window resets; HasReset is false when the
	// upstream payload did not provide a parseable timestamp.
	ResetsAt time.Time
	HasReset bool
	// Detail is optional supplementary text, currently used to show extra-usage
	// credit consumption (e.g. "$3.20 / $20.00").
	Detail string
}

// MeterOptions controls which optional meters are emitted.
type MeterOptions struct {
	// IncludeZeroModels emits per-model 7-day windows even when their
	// utilization is exactly zero. By default such windows are hidden to match
	// the compact view, which only surfaces models that have been used.
	IncludeZeroModels bool
}

// Meters converts the payload into an ordered slice of meters, one per window
// that is present (subject to opt). The order is stable: session, weekly,
// per-model weekly windows, then extra usage.
func (u *Usage) Meters(opt MeterOptions) []Meter {
	if u == nil {
		return nil
	}
	var meters []Meter

	if u.FiveHour.Present() {
		meters = append(meters, window2meter("five_hour", "Session", KindSession, u.FiveHour))
	}
	if u.SevenDay.Present() {
		meters = append(meters, window2meter("seven_day", "Weekly", KindWeekly, u.SevenDay))
	}
	if w := u.SevenDaySonnet; w.Present() && (opt.IncludeZeroModels || w.Percent() > 0) {
		meters = append(meters, window2meter("seven_day_sonnet", "Sonnet", KindWeeklyModel, w))
	}
	if w := u.SevenDayOpus; w.Present() && (opt.IncludeZeroModels || w.Percent() > 0) {
		meters = append(meters, window2meter("seven_day_opus", "Opus", KindWeeklyModel, w))
	}
	if u.ExtraUsage.Present() {
		m := Meter{
			Key:     "extra_usage",
			Label:   "Extra",
			Percent: *u.ExtraUsage.Utilization,
			Kind:    KindExtra,
			Detail:  extraDetail(u.ExtraUsage),
		}
		meters = append(meters, m)
	}
	return meters
}

func window2meter(key, label string, kind Kind, w *Window) Meter {
	m := Meter{
		Key:     key,
		Label:   label,
		Percent: w.Percent(),
		Kind:    kind,
	}
	if t, ok := w.ResetTime(); ok {
		m.ResetsAt = t
		m.HasReset = true
	}
	return m
}

// extraDetail formats the "used / limit" credit string when both values are
// available, returning an empty string otherwise.
func extraDetail(e ExtraUsage) string {
	if e.UsedCredits == nil || e.MonthlyLimit == nil {
		return ""
	}
	return fmt.Sprintf("$%.2f / $%.2f", *e.UsedCredits, *e.MonthlyLimit)
}

// ParseTime parses an RFC3339-style timestamp as returned by the usage
// endpoint. It tolerates a few layout variants observed in the wild.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999-07:00",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
