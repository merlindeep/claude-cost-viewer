package render

import (
	"encoding/json"
	"io"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// jsonMeter is the machine-readable representation of a single meter.
type jsonMeter struct {
	Key             string  `json:"key"`
	Label           string  `json:"label"`
	Kind            string  `json:"kind"`
	Percent         float64 `json:"percent"`
	ResetsAt        *string `json:"resets_at,omitempty"`
	ResetsInSeconds *int64  `json:"resets_in_seconds,omitempty"`
	Detail          string  `json:"detail,omitempty"`
}

// jsonDocument is the top-level JSON output.
type jsonDocument struct {
	Plan        string      `json:"plan,omitempty"`
	GeneratedAt string      `json:"generated_at"`
	Meters      []jsonMeter `json:"meters"`
}

// kindString maps a meter kind to a stable JSON token.
func kindString(k usage.Kind) string {
	switch k {
	case usage.KindSession:
		return "session"
	case usage.KindWeekly:
		return "weekly"
	case usage.KindWeeklyModel:
		return "weekly_model"
	case usage.KindExtra:
		return "extra"
	default:
		return "unknown"
	}
}

// renderJSON writes the snapshot as indented JSON. Reset timestamps are emitted
// in RFC3339 form alongside the number of seconds remaining, so consumers can
// use whichever is convenient.
func renderJSON(w io.Writer, u *usage.Usage, opt Options) error {
	now := opt.now()
	doc := jsonDocument{
		Plan:        opt.PlanLabel,
		GeneratedAt: now.Format(time.RFC3339),
		Meters:      []jsonMeter{},
	}
	for _, m := range u.Meters(opt.meterOptions()) {
		jm := jsonMeter{
			Key:     m.Key,
			Label:   m.Label,
			Kind:    kindString(m.Kind),
			Percent: m.Percent,
			Detail:  m.Detail,
		}
		if m.HasReset {
			ts := m.ResetsAt.Format(time.RFC3339)
			jm.ResetsAt = &ts
			secs := int64(m.ResetsAt.Sub(now).Seconds())
			jm.ResetsInSeconds = &secs
		}
		doc.Meters = append(doc.Meters, jm)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
