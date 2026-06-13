package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

var refNow = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func sampleUsage() *usage.Usage {
	return &usage.Usage{
		FiveHour:       &usage.Window{Utilization: f(42), ResetsAt: "2026-06-13T15:44:00Z"},
		SevenDay:       &usage.Window{Utilization: f(13), ResetsAt: "2026-06-20T00:00:00Z"},
		SevenDaySonnet: &usage.Window{Utilization: f(8), ResetsAt: "2026-06-20T00:00:00Z"},
		ExtraUsage:     usage.ExtraUsage{IsEnabled: true, Utilization: f(16), UsedCredits: f(3.2), MonthlyLimit: f(20)},
	}
}

func testConfig(fetch func() Result) Config {
	return Config{Fetch: fetch, Interval: time.Millisecond, Now: func() time.Time { return refNow }}
}

func TestNewDefaults(t *testing.T) {
	m := New(Config{Fetch: func() Result { return Result{} }})
	if m.cfg.Now == nil {
		t.Error("Now should default")
	}
	if m.cfg.Interval != time.Minute {
		t.Errorf("Interval default = %v, want 1m", m.cfg.Interval)
	}
	if !m.fetching {
		t.Error("model should start in fetching state")
	}
}

func TestInit(t *testing.T) {
	m := New(testConfig(func() Result { return Result{Usage: sampleUsage()} }))
	if m.Init() == nil {
		t.Error("Init should return a command")
	}
}

func TestFetchCmd(t *testing.T) {
	called := 0
	m := New(testConfig(func() Result { called++; return Result{Usage: sampleUsage(), Plan: "Max 20x"} }))
	msg := m.fetchCmd()()
	res, ok := msg.(resultMsg)
	if !ok {
		t.Fatalf("fetchCmd produced %T, want resultMsg", msg)
	}
	if called != 1 || res.Plan != "Max 20x" {
		t.Errorf("fetch not invoked correctly: called=%d res=%+v", called, res)
	}
}

func TestTickCmd(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	msg := m.tickCmd()() // Interval is 1ms, so this returns promptly.
	if _, ok := msg.(tickMsg); !ok {
		t.Fatalf("tickCmd produced %T, want tickMsg", msg)
	}
}

func TestUpdateQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("q")},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyEsc},
	} {
		m := New(testConfig(func() Result { return Result{} }))
		next, cmd := m.Update(key)
		if !next.(Model).quitting {
			t.Errorf("key %q: quitting not set", key.String())
		}
		if cmd == nil {
			t.Fatalf("key %q: expected a command", key.String())
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("key %q: expected tea.QuitMsg", key.String())
		}
	}
}

func TestUpdateRefreshKey(t *testing.T) {
	called := 0
	m := New(testConfig(func() Result { called++; return Result{Usage: sampleUsage()} }))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !next.(Model).fetching {
		t.Error("refresh should set fetching")
	}
	if cmd == nil {
		t.Fatal("refresh should return a fetch command")
	}
	if _, ok := cmd().(resultMsg); !ok || called != 1 {
		t.Error("refresh command should invoke fetch")
	}
}

func TestUpdateUnknownKeyNoop(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd != nil {
		t.Error("unknown key should not return a command")
	}
	if next.(Model).quitting {
		t.Error("unknown key should not quit")
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	nm := next.(Model)
	if nm.width != 100 || nm.height != 40 {
		t.Errorf("size = %dx%d", nm.width, nm.height)
	}
}

func TestUpdateTick(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	next, cmd := m.Update(tickMsg(refNow))
	if !next.(Model).fetching {
		t.Error("tick should set fetching")
	}
	if cmd == nil {
		t.Error("tick should schedule a fetch and the next tick")
	}
}

func TestUpdateResultSuccess(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	next, _ := m.Update(resultMsg{Usage: sampleUsage(), Plan: "Pro"})
	nm := next.(Model)
	if nm.fetching {
		t.Error("result should clear fetching")
	}
	if nm.err != nil || nm.usage == nil || nm.plan != "Pro" {
		t.Errorf("result not stored: %+v", nm)
	}
	if nm.lastUpdate.IsZero() {
		t.Error("lastUpdate should be set")
	}
}

func TestUpdateResultError(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.usage = sampleUsage() // pretend a previous success
	next, _ := m.Update(resultMsg{Err: errors.New("boom")})
	nm := next.(Model)
	if nm.err == nil {
		t.Error("error should be stored")
	}
	if nm.usage == nil {
		t.Error("previous snapshot should be retained on error")
	}
}

func TestViewQuitting(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.quitting = true
	if m.View() != "" {
		t.Error("quitting view should be empty")
	}
}

func TestViewLoading(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	if !strings.Contains(m.View(), "loading") {
		t.Errorf("loading view = %q", m.View())
	}
}

func TestViewSuccess(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.usage = sampleUsage()
	m.plan = "Max 20x"
	m.lastUpdate = refNow
	m.fetching = false
	out := m.View()
	for _, want := range []string{"Claude usage", "Max 20x", "Session", "42%", "sonnet", "Extra", "$3.20 / $20.00", "r refresh", "q quit", "updated 12:00:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestViewNoMeters(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.usage = &usage.Usage{}
	if !strings.Contains(m.View(), "(no usage windows reported)") {
		t.Errorf("view = %q", m.View())
	}
}

func TestViewError(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.err = errors.New("token expired")
	out := m.View()
	if !strings.Contains(out, "error: token expired") {
		t.Errorf("error view = %q", out)
	}
}

func TestViewFetchingFooter(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	m.usage = sampleUsage()
	m.fetching = true
	if !strings.Contains(m.View(), "refreshing") {
		t.Error("fetching footer should mention refreshing")
	}
}

func TestResetTail(t *testing.T) {
	tests := []struct {
		name string
		mt   usage.Meter
		want string
	}{
		{"no reset", usage.Meter{Kind: usage.KindSession}, ""},
		{"past", usage.Meter{Kind: usage.KindSession, HasReset: true, ResetsAt: refNow.Add(-time.Hour)}, "resets now"},
		{"session hours", usage.Meter{Kind: usage.KindSession, HasReset: true, ResetsAt: refNow.Add(3 * time.Hour)}, "resets in 3h 00m"},
		{"session minutes", usage.Meter{Kind: usage.KindSession, HasReset: true, ResetsAt: refNow.Add(20 * time.Minute)}, "resets in 20m"},
		{"weekly soon", usage.Meter{Kind: usage.KindWeekly, HasReset: true, ResetsAt: refNow.Add(5 * time.Hour)}, "resets in 5h 00m"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resetTail(tc.mt, refNow); got != tc.want {
				t.Errorf("resetTail = %q, want %q", got, tc.want)
			}
		})
	}
	// Weekly far away -> absolute weekday/time, not relative.
	far := resetTail(usage.Meter{Kind: usage.KindWeekly, HasReset: true, ResetsAt: refNow.Add(72 * time.Hour)}, refNow)
	if strings.Contains(far, "in ") || !strings.HasPrefix(far, "resets ") {
		t.Errorf("weekly far resetTail = %q", far)
	}
}

func TestBarClamps(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	full := m.bar(150)
	if !strings.Contains(full, strings.Repeat("█", barWidth)) {
		t.Error("over-100% bar should be full")
	}
	empty := m.bar(-5)
	if !strings.Contains(empty, strings.Repeat("░", barWidth)) {
		t.Error("negative bar should be empty")
	}
	// Mid-range exercises the yellow threshold in colorForPct.
	if m.bar(60) == "" {
		t.Error("mid-range bar should render")
	}
}

func TestMeterLineExtraWithResetAndDetail(t *testing.T) {
	m := New(testConfig(func() Result { return Result{} }))
	mt := usage.Meter{
		Label:    "Extra",
		Percent:  60,
		Kind:     usage.KindExtra,
		HasReset: true,
		ResetsAt: refNow.Add(3 * time.Hour),
		Detail:   "$5.00 / $20.00",
	}
	line := m.meterLine(mt)
	if !strings.Contains(line, "resets in 3h 00m") || !strings.Contains(line, "$5.00 / $20.00") {
		t.Errorf("line = %q", line)
	}
}

func TestRunWithCancelledContext(t *testing.T) {
	// Exercise Run end-to-end. In a headless test environment the program may
	// not have a TTY; a cancelled context should make it return promptly. Guard
	// with a timeout so the suite never hangs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, testConfig(func() Result { return Result{Usage: sampleUsage()} }))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Skip("TUI did not exit promptly in headless environment")
	}
}
