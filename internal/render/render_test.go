package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

func f(v float64) *float64 { return &v }

var refNow = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

// sampleUsage is a Max-plan shaped payload exercising every meter type.
func sampleUsage() *usage.Usage {
	return &usage.Usage{
		FiveHour:       &usage.Window{Utilization: f(42), ResetsAt: "2026-06-13T15:44:00Z"},
		SevenDay:       &usage.Window{Utilization: f(13), ResetsAt: "2026-06-13T20:00:00Z"},
		SevenDaySonnet: &usage.Window{Utilization: f(8), ResetsAt: "2026-06-13T20:00:00Z"},
		SevenDayOpus:   &usage.Window{Utilization: f(90), ResetsAt: "2026-06-13T20:00:00Z"},
		ExtraUsage:     usage.ExtraUsage{IsEnabled: true, Utilization: f(16), UsedCredits: f(3.2), MonthlyLimit: f(20)},
	}
}

func baseOpts() Options {
	return Options{Color: false, Now: refNow, PlanLabel: "Max 20x"}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestParseMode(t *testing.T) {
	for _, s := range []string{"compact", "TABLE", " json ", "Oneline"} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q) error = %v", s, err)
		}
	}
	if _, err := ParseMode("bogus"); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestModesAndString(t *testing.T) {
	modes := Modes()
	if len(modes) != 4 {
		t.Fatalf("Modes() = %v", modes)
	}
	if ModeCompact.String() != "compact" {
		t.Errorf("String() = %q", ModeCompact.String())
	}
}

func TestColorFor(t *testing.T) {
	if colorFor(10) != ansiGreen {
		t.Error("10% should be green")
	}
	if colorFor(60) != ansiYellow {
		t.Error("60% should be yellow")
	}
	if colorFor(90) != ansiRed {
		t.Error("90% should be red")
	}
}

func TestBar(t *testing.T) {
	if got := bar(50, 20, false); got != strings.Repeat("█", 10)+strings.Repeat("░", 10) {
		t.Errorf("bar(50) = %q", got)
	}
	if got := bar(0, 10, false); got != strings.Repeat("░", 10) {
		t.Errorf("bar(0) = %q", got)
	}
	if got := bar(100, 10, false); got != strings.Repeat("█", 10) {
		t.Errorf("bar(100) = %q", got)
	}
	// Clamping.
	if got := bar(150, 5, false); got != strings.Repeat("█", 5) {
		t.Errorf("bar(150) = %q", got)
	}
	if got := bar(-10, 5, false); got != strings.Repeat("░", 5) {
		t.Errorf("bar(-10) = %q", got)
	}
	// Colour wraps the bar.
	if got := bar(10, 5, true); !strings.Contains(got, ansiGreen) || !strings.Contains(got, ansiReset) {
		t.Errorf("coloured bar missing codes: %q", got)
	}
}

func TestUntilText(t *testing.T) {
	tests := []struct {
		delta time.Duration
		want  string
	}{
		{-time.Hour, "now"},
		{0, "now"},
		{30 * time.Minute, "in 30m"},
		{3*time.Hour + 44*time.Minute, "in 3h 44m"},
		{2 * time.Hour, "in 2h 00m"},
	}
	for _, tc := range tests {
		if got := untilText(refNow, refNow.Add(tc.delta)); got != tc.want {
			t.Errorf("untilText(+%v) = %q, want %q", tc.delta, got, tc.want)
		}
	}
}

func TestWeeklyText(t *testing.T) {
	// Less than a day away -> relative.
	if got := weeklyText(refNow, refNow.Add(8*time.Hour)); got != "in 8h 00m" {
		t.Errorf("weeklyText(<24h) = %q", got)
	}
	// More than a day away -> absolute, not relative.
	got := weeklyText(refNow, refNow.Add(72*time.Hour))
	if strings.HasPrefix(got, "in ") || !strings.Contains(got, ":") {
		t.Errorf("weeklyText(>24h) = %q, want absolute weekday/time", got)
	}
}

func TestResetText(t *testing.T) {
	if got := resetText(usage.Meter{Kind: usage.KindSession, HasReset: false}, refNow); got != "" {
		t.Errorf("no-reset meter = %q", got)
	}
	session := usage.Meter{Kind: usage.KindSession, HasReset: true, ResetsAt: refNow.Add(time.Hour)}
	if got := resetText(session, refNow); got != "in 1h 00m" {
		t.Errorf("session reset = %q", got)
	}
}

func TestRoundPct(t *testing.T) {
	if roundPct(42.4) != 42 || roundPct(42.6) != 43 {
		t.Error("roundPct rounding incorrect")
	}
}

func TestRenderUnknownMode(t *testing.T) {
	if err := Render(io.Discard, sampleUsage(), Mode("bogus"), baseOpts()); err == nil {
		t.Error("expected error for unknown mode")
	}
}

func TestRenderCompactGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleUsage(), ModeCompact, baseOpts()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Claude usage  Max 20x",
		"  Session   ████████░░░░░░░░░░░░  42%   resets in 3h 44m",
		"  Weekly    ███░░░░░░░░░░░░░░░░░  13%   resets in 8h 00m",
		"    sonnet  ██░░░░░░░░░░░░░░░░░░   8%   resets in 8h 00m",
		"    opus    ██████████████████░░  90%   resets in 8h 00m",
		"  Extra     ███░░░░░░░░░░░░░░░░░  16%   $3.20 / $20.00",
	}
	got := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestCompactLineExtraWithResetAndDetail(t *testing.T) {
	// An extra meter that carries both a reset time and a credit detail
	// exercises the branch that joins the two with a separator.
	m := usage.Meter{
		Key:      "extra_usage",
		Label:    "Extra",
		Percent:  16,
		Kind:     usage.KindExtra,
		HasReset: true,
		ResetsAt: refNow.Add(2 * time.Hour),
		Detail:   "$3.20 / $20.00",
	}
	line := compactLine(m, baseOpts(), refNow)
	if !strings.Contains(line, "resets in 2h 00m") || !strings.Contains(line, "$3.20 / $20.00") {
		t.Errorf("line = %q", line)
	}
}

func TestRenderCompactNoMeters(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, &usage.Usage{}, ModeCompact, Options{Now: refNow}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(no usage windows reported)") {
		t.Errorf("expected no-data notice, got %q", buf.String())
	}
}

func TestRenderCompactColor(t *testing.T) {
	var buf bytes.Buffer
	opt := baseOpts()
	opt.Color = true
	if err := Render(&buf, sampleUsage(), ModeCompact, opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\033[") {
		t.Error("coloured output should contain ANSI escapes")
	}
}

func TestRenderTable(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleUsage(), ModeTable, baseOpts()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Plan: Max 20x", "METER", "USAGE", "RESETS", "DETAIL", "Session", "in 3h 44m", "$3.20 / $20.00", "—"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q:\n%s", want, out)
		}
	}
	// Plan line, blank line, header, 5 data rows.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 8 {
		t.Errorf("got %d table lines, want 8:\n%s", len(lines), out)
	}
}

func TestRenderTableNarrowWidth(t *testing.T) {
	// A bar narrower than the "USAGE" header forces the bar cell to be padded
	// out to the header width.
	var buf bytes.Buffer
	opt := baseOpts()
	opt.Width = 3
	if err := Render(&buf, sampleUsage(), ModeTable, opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "USAGE") {
		t.Errorf("got %q", buf.String())
	}
}

func TestRenderTableNoMeters(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, &usage.Usage{}, ModeTable, Options{Now: refNow}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(no usage windows reported)") {
		t.Errorf("got %q", buf.String())
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleUsage(), ModeJSON, baseOpts()); err != nil {
		t.Fatal(err)
	}
	var doc jsonDocument
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if doc.Plan != "Max 20x" {
		t.Errorf("plan = %q", doc.Plan)
	}
	if doc.GeneratedAt != refNow.Format(time.RFC3339) {
		t.Errorf("generated_at = %q", doc.GeneratedAt)
	}
	if len(doc.Meters) != 5 {
		t.Fatalf("got %d meters", len(doc.Meters))
	}
	session := doc.Meters[0]
	if session.Key != "five_hour" || session.Kind != "session" || session.Percent != 42 {
		t.Errorf("session meter = %+v", session)
	}
	if session.ResetsAt == nil || session.ResetsInSeconds == nil {
		t.Fatal("session should carry reset fields")
	}
	if *session.ResetsInSeconds != int64((3*time.Hour + 44*time.Minute).Seconds()) {
		t.Errorf("resets_in_seconds = %d", *session.ResetsInSeconds)
	}
	extra := doc.Meters[4]
	if extra.Kind != "extra" || extra.Detail != "$3.20 / $20.00" {
		t.Errorf("extra meter = %+v", extra)
	}
	if extra.ResetsAt != nil {
		t.Error("extra meter should not carry a reset timestamp")
	}
}

func TestRenderJSONEmptyMeters(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, &usage.Usage{}, ModeJSON, Options{Now: refNow}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\"meters\": []") {
		t.Errorf("empty meters should render as []:\n%s", buf.String())
	}
}

func TestKindString(t *testing.T) {
	if kindString(usage.Kind(99)) != "unknown" {
		t.Error("unexpected kind should map to 'unknown'")
	}
}

func TestRenderOneline(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleUsage(), ModeOneline, baseOpts()); err != nil {
		t.Fatal(err)
	}
	want := "Claude 5h:42% 7d:13% sonnet:8% opus:90% extra:16%\n"
	if buf.String() != want {
		t.Errorf("oneline =\n %q\nwant\n %q", buf.String(), want)
	}
}

func TestRenderOnelineNoData(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, &usage.Usage{}, ModeOneline, Options{Now: refNow}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "Claude: no data\n" {
		t.Errorf("oneline empty = %q", buf.String())
	}
}

func TestRenderWriteErrors(t *testing.T) {
	modes := []Mode{ModeCompact, ModeTable, ModeJSON, ModeOneline}
	for _, m := range modes {
		if err := Render(errWriter{}, sampleUsage(), m, baseOpts()); err == nil {
			t.Errorf("mode %s: expected write error", m)
		}
		// Also exercise the empty-usage write-error path where applicable.
		_ = Render(errWriter{}, &usage.Usage{}, m, baseOpts())
	}
}

func TestOptionDefaults(t *testing.T) {
	o := Options{}
	if o.now().IsZero() {
		t.Error("now() should fall back to time.Now()")
	}
	if o.width() != DefaultBarWidth {
		t.Errorf("width() = %d, want %d", o.width(), DefaultBarWidth)
	}
}
