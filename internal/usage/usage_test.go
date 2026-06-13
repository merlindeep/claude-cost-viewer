package usage

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func TestWindowPresentAndPercent(t *testing.T) {
	tests := []struct {
		name        string
		w           *Window
		wantPresent bool
		wantPercent float64
	}{
		{"nil window", nil, false, 0},
		{"nil utilization", &Window{}, false, 0},
		{"zero utilization", &Window{Utilization: f(0)}, true, 0},
		{"some utilization", &Window{Utilization: f(42.5)}, true, 42.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Present(); got != tc.wantPresent {
				t.Errorf("Present() = %v, want %v", got, tc.wantPresent)
			}
			if got := tc.w.Percent(); got != tc.wantPercent {
				t.Errorf("Percent() = %v, want %v", got, tc.wantPercent)
			}
		})
	}
}

func TestWindowResetTime(t *testing.T) {
	if _, ok := (*Window)(nil).ResetTime(); ok {
		t.Error("nil window should not yield a reset time")
	}
	if _, ok := (&Window{ResetsAt: ""}).ResetTime(); ok {
		t.Error("empty ResetsAt should not parse")
	}
	w := &Window{ResetsAt: "2026-06-13T15:04:05Z"}
	got, ok := w.ResetTime()
	if !ok {
		t.Fatal("expected valid reset time")
	}
	if !got.Equal(time.Date(2026, 6, 13, 15, 4, 5, 0, time.UTC)) {
		t.Errorf("unexpected reset time: %v", got)
	}
}

func TestExtraUsagePresent(t *testing.T) {
	tests := []struct {
		name string
		e    ExtraUsage
		want bool
	}{
		{"disabled", ExtraUsage{IsEnabled: false, Utilization: f(10)}, false},
		{"enabled no util", ExtraUsage{IsEnabled: true}, false},
		{"enabled with util", ExtraUsage{IsEnabled: true, Utilization: f(10)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.Present(); got != tc.want {
				t.Errorf("Present() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	const payload = `{
		"five_hour": {"utilization": 42, "resets_at": "2026-06-13T15:00:00Z"},
		"seven_day": {"utilization": 13.5, "resets_at": "2026-06-20T00:00:00Z"}
	}`
	u, err := Parse([]byte(payload))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if u.FiveHour.Percent() != 42 {
		t.Errorf("five_hour percent = %v, want 42", u.FiveHour.Percent())
	}
	if u.SevenDay.Percent() != 13.5 {
		t.Errorf("seven_day percent = %v, want 13.5", u.SevenDay.Percent())
	}
}

func TestParseInvalid(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestIsEmpty(t *testing.T) {
	if !(*Usage)(nil).IsEmpty() {
		t.Error("nil usage should be empty")
	}
	if !(&Usage{}).IsEmpty() {
		t.Error("zero usage should be empty")
	}
	if (&Usage{FiveHour: &Window{Utilization: f(1)}}).IsEmpty() {
		t.Error("usage with a window should not be empty")
	}
	if (&Usage{ExtraUsage: ExtraUsage{IsEnabled: true, Utilization: f(5)}}).IsEmpty() {
		t.Error("usage with extra should not be empty")
	}
}

func TestMetersNil(t *testing.T) {
	if got := (*Usage)(nil).Meters(MeterOptions{}); got != nil {
		t.Errorf("nil usage Meters() = %v, want nil", got)
	}
}

func TestMetersFreePlanShape(t *testing.T) {
	// Free/Pro often expose only the session window.
	u := &Usage{FiveHour: &Window{Utilization: f(30), ResetsAt: "2026-06-13T15:00:00Z"}}
	meters := u.Meters(MeterOptions{})
	if len(meters) != 1 {
		t.Fatalf("got %d meters, want 1", len(meters))
	}
	m := meters[0]
	if m.Key != "five_hour" || m.Label != "Session" || m.Kind != KindSession {
		t.Errorf("unexpected meter: %+v", m)
	}
	if !m.HasReset {
		t.Error("expected reset time to be parsed")
	}
}

func TestMetersMaxPlanShape(t *testing.T) {
	// Max plans expose every window, including per-model and extra usage.
	u := &Usage{
		FiveHour:       &Window{Utilization: f(50), ResetsAt: "2026-06-13T15:00:00Z"},
		SevenDay:       &Window{Utilization: f(20), ResetsAt: "2026-06-20T00:00:00Z"},
		SevenDaySonnet: &Window{Utilization: f(12), ResetsAt: "2026-06-20T00:00:00Z"},
		SevenDayOpus:   &Window{Utilization: f(8), ResetsAt: "2026-06-20T00:00:00Z"},
		ExtraUsage:     ExtraUsage{IsEnabled: true, Utilization: f(16), UsedCredits: f(3.2), MonthlyLimit: f(20)},
	}
	meters := u.Meters(MeterOptions{})
	wantOrder := []string{"five_hour", "seven_day", "seven_day_sonnet", "seven_day_opus", "extra_usage"}
	if len(meters) != len(wantOrder) {
		t.Fatalf("got %d meters, want %d", len(meters), len(wantOrder))
	}
	for i, key := range wantOrder {
		if meters[i].Key != key {
			t.Errorf("meter[%d].Key = %q, want %q", i, meters[i].Key, key)
		}
	}
	extra := meters[4]
	if extra.Detail != "$3.20 / $20.00" {
		t.Errorf("extra detail = %q, want %q", extra.Detail, "$3.20 / $20.00")
	}
}

func TestMetersZeroModelsHiddenByDefault(t *testing.T) {
	u := &Usage{
		SevenDaySonnet: &Window{Utilization: f(0)},
		SevenDayOpus:   &Window{Utilization: f(0)},
	}
	if got := u.Meters(MeterOptions{}); len(got) != 0 {
		t.Errorf("zero-percent model windows should be hidden, got %d", len(got))
	}
	got := u.Meters(MeterOptions{IncludeZeroModels: true})
	if len(got) != 2 {
		t.Errorf("with IncludeZeroModels, got %d meters, want 2", len(got))
	}
}

func TestMetersExtraWithoutDetail(t *testing.T) {
	u := &Usage{ExtraUsage: ExtraUsage{IsEnabled: true, Utilization: f(5)}}
	meters := u.Meters(MeterOptions{})
	if len(meters) != 1 || meters[0].Detail != "" {
		t.Errorf("expected single extra meter with empty detail, got %+v", meters)
	}
}

func TestParseTime(t *testing.T) {
	valid := []string{
		"2026-06-13T15:04:05Z",
		"2026-06-13T15:04:05.123456789Z",
		"2026-06-13T15:04:05+02:00",
		"2026-06-13T15:04:05.999999-07:00",
	}
	for _, s := range valid {
		if _, ok := ParseTime(s); !ok {
			t.Errorf("ParseTime(%q) failed, want success", s)
		}
	}
	for _, s := range []string{"", "   ", "nonsense", "13/06/2026"} {
		if _, ok := ParseTime(s); ok {
			t.Errorf("ParseTime(%q) succeeded, want failure", s)
		}
	}
}
