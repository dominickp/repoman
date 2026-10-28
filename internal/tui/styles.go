package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/dominickp/repoman/internal/config"
)

// styles holds every lipgloss style derived from the active theme.
type styles struct {
	theme config.Theme

	accent lipgloss.Style
	muted  lipgloss.Style
	text   lipgloss.Style
	ok     lipgloss.Style
	warn   lipgloss.Style
	danger lipgloss.Style

	title    lipgloss.Style
	subtitle lipgloss.Style
	column   lipgloss.Style
	row      lipgloss.Style
	selected lipgloss.Style
	badge    lipgloss.Style
	key      lipgloss.Style
	hint     lipgloss.Style

	card       lipgloss.Style
	cardActive lipgloss.Style
	cardMuted  lipgloss.Style

	overlay       lipgloss.Style
	overlayTitle  lipgloss.Style
	overlayCursor lipgloss.Style
	overlayValue  lipgloss.Style
}

func newStyles(theme config.Theme) styles {
	c := func(hex string) lipgloss.Color { return lipgloss.Color(hex) }

	return styles{
		theme: theme,

		accent: lipgloss.NewStyle().Foreground(c(theme.Accent)),
		muted:  lipgloss.NewStyle().Foreground(c(theme.Muted)),
		text:   lipgloss.NewStyle().Foreground(c(theme.Text)),
		ok:     lipgloss.NewStyle().Foreground(c(theme.Success)),
		warn:   lipgloss.NewStyle().Foreground(c(theme.Warn)),
		danger: lipgloss.NewStyle().Foreground(c(theme.Danger)),

		title:    lipgloss.NewStyle().Foreground(c(theme.Accent)).Bold(true),
		subtitle: lipgloss.NewStyle().Foreground(c(theme.Muted)),
		column:   lipgloss.NewStyle().Foreground(c(theme.Muted)).Bold(true),
		row:      lipgloss.NewStyle().Foreground(c(theme.Text)),
		selected: lipgloss.NewStyle().Foreground(c(theme.Accent)).Bold(true),
		badge:    lipgloss.NewStyle().Foreground(c(theme.Muted)).Faint(true),
		key:      lipgloss.NewStyle().Foreground(c(theme.Accent)),
		hint:     lipgloss.NewStyle().Foreground(c(theme.Muted)),

		card: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(theme.Border)).
			Padding(0, 1),
		cardActive: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(theme.Accent)).
			Padding(0, 1),
		cardMuted: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(theme.Border)).
			Padding(0, 1).
			Faint(true),

		overlay: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c(theme.Accent)).
			Padding(1, 2),
		overlayTitle:  lipgloss.NewStyle().Foreground(c(theme.Accent)).Bold(true),
		overlayCursor: lipgloss.NewStyle().Foreground(c(theme.Accent)).Bold(true),
		overlayValue:  lipgloss.NewStyle().Foreground(c(theme.Text)),
	}
}
