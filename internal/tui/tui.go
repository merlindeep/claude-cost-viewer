// Package tui implements the interactive, full-screen dashboard built on
// Bubble Tea. It periodically fetches a fresh usage snapshot, renders it with
// coloured bars, and responds to keystrokes:
//
//	r            refresh now
//	q / ctrl+c   quit
//
// The model is decoupled from the network: a [Config.Fetch] function supplies
// snapshots, which keeps the Update/View logic deterministic and unit-testable.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/merlindeep/claude-cost-viewer/internal/usage"
)

// barWidth is the width of the usage bars in the dashboard.
const barWidth = 24

// Result is the outcome of a single fetch.
type Result struct {
	Usage *usage.Usage
	Plan  string
	Err   error
}

// Config configures the dashboard.
type Config struct {
	// Fetch performs one usage fetch. It must be safe to call from a goroutine.
	Fetch func() Result
	// Interval is the auto-refresh cadence.
	Interval time.Duration
	// Now returns the current time (defaults to time.Now).
	Now func() time.Time
	// ShowZeroModels includes per-model windows at 0% utilization.
	ShowZeroModels bool
}

// Internal message types.
type (
	tickMsg   time.Time
	resultMsg Result
)

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	cfg        Config
	usage      *usage.Usage
	plan       string
	err        error
	lastUpdate time.Time
	fetching   bool
	quitting   bool
	width      int
	height     int
}

// New constructs a dashboard model, filling in defaults.
func New(cfg Config) Model {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	return Model{cfg: cfg, fetching: true}
}

// Run starts the dashboard and blocks until the user quits or ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	p := tea.NewProgram(New(cfg), tea.WithContext(ctx), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Init kicks off the first fetch and starts the refresh ticker.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchCmd(), m.tickCmd())
}

func (m Model) fetchCmd() tea.Cmd {
	fetch := m.cfg.Fetch
	return func() tea.Msg { return resultMsg(fetch()) }
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(m.cfg.Interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles messages and returns the next model state.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		case "r":
			m.fetching = true
			return m, m.fetchCmd()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		m.fetching = true
		return m, tea.Batch(m.fetchCmd(), m.tickCmd())
	case resultMsg:
		m.fetching = false
		m.err = msg.Err
		if msg.Err == nil {
			m.usage = msg.Usage
			m.plan = msg.Plan
		}
		m.lastUpdate = m.cfg.Now()
	}
	return m, nil
}

// Styles. Under a non-colour terminal profile (as in tests) these render as
// plain text, so the View output remains assertable.
var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	dimStyle   = lipgloss.NewStyle().Faint(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
)

func colorForPct(pct float64) lipgloss.Color {
	switch {
	case pct < 50:
		return lipgloss.Color("10")
	case pct < 85:
		return lipgloss.Color("11")
	default:
		return lipgloss.Color("9")
	}
}

// View renders the dashboard.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder
	title := titleStyle.Render("Claude usage")
	if m.plan != "" {
		title += "  " + dimStyle.Render(m.plan)
	}
	b.WriteString(title + "\n\n")

	switch {
	case m.err != nil:
		b.WriteString(errStyle.Render("error: "+m.err.Error()) + "\n\n")
		b.WriteString(dimStyle.Render("Showing the last successful snapshot, if any.") + "\n\n")
		m.writeMeters(&b)
	case m.usage == nil:
		b.WriteString(dimStyle.Render("loading…") + "\n")
	default:
		m.writeMeters(&b)
	}

	b.WriteString("\n" + m.footer())
	return b.String()
}

// writeMeters appends the meter lines (or a placeholder) to b.
func (m Model) writeMeters(b *strings.Builder) {
	if m.usage == nil {
		return
	}
	meters := m.usage.Meters(usage.MeterOptions{IncludeZeroModels: m.cfg.ShowZeroModels})
	if len(meters) == 0 {
		b.WriteString(dimStyle.Render("(no usage windows reported)") + "\n")
		return
	}
	for _, mt := range meters {
		b.WriteString(m.meterLine(mt) + "\n")
	}
}

// meterLine renders one meter as "Label  ▕bar▏ 42%  resets in 3h 44m".
func (m Model) meterLine(mt usage.Meter) string {
	label := mt.Label
	if mt.Kind == usage.KindWeeklyModel {
		label = "  " + strings.ToLower(mt.Label)
	}
	pct := fmt.Sprintf("%3d%%", int(mt.Percent+0.5))
	colored := lipgloss.NewStyle().Foreground(colorForPct(mt.Percent)).Render(pct)

	line := fmt.Sprintf("  %-9s %s %s", label, m.bar(mt.Percent), colored)

	tail := resetTail(mt, m.cfg.Now())
	if mt.Kind == usage.KindExtra && mt.Detail != "" {
		if tail != "" {
			tail += "  "
		}
		tail += mt.Detail
	}
	if tail != "" {
		line += "   " + dimStyle.Render(tail)
	}
	return line
}

// bar builds a coloured usage bar of fixed width.
func (m Model) bar(pct float64) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	// pct is clamped to [0,100] above, so filled is always in [0,barWidth].
	filled := int(pct/100*float64(barWidth) + 0.5)
	fill := lipgloss.NewStyle().Foreground(colorForPct(pct)).Render(strings.Repeat("█", filled))
	empty := dimStyle.Render(strings.Repeat("░", barWidth-filled))
	return fill + empty
}

// resetTail returns the "resets …" suffix for a meter, or an empty string.
func resetTail(mt usage.Meter, now time.Time) string {
	if !mt.HasReset {
		return ""
	}
	d := mt.ResetsAt.Sub(now)
	if d <= 0 {
		return "resets now"
	}
	switch mt.Kind {
	case usage.KindWeekly, usage.KindWeeklyModel:
		if d >= 24*time.Hour {
			return "resets " + mt.ResetsAt.Local().Format("Mon 15:04")
		}
	}
	h := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("resets in %dh %02dm", h, mins)
	}
	return fmt.Sprintf("resets in %dm", mins)
}

// footer renders the status line with the last update time, refresh interval,
// and key hints.
func (m Model) footer() string {
	updated := "—"
	if !m.lastUpdate.IsZero() {
		updated = m.lastUpdate.Format("15:04:05")
	}
	status := fmt.Sprintf("updated %s · every %s · r refresh · q quit", updated, m.cfg.Interval)
	if m.fetching {
		status += " · refreshing…"
	}
	return dimStyle.Render(status)
}
