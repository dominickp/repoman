package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dominickp/repoman/internal/config"
)

type rowKind int

const (
	rowSection rowKind = iota
	rowDir
	rowAddDir
	rowIgnored
	rowView
	rowTheme
	rowPrune
	rowDepth
	rowInfo
)

type settingsRow struct {
	kind  rowKind
	label string
	value string
	index int // position within cfg.Dirs or cfg.Ignored
}

// settingsState is the in-app preferences screen opened with `,`.
type settingsState struct {
	cfg    *config.Config
	rows   []settingsRow
	cursor int
	adding bool
	input  textinput.Model
}

func newSettingsState(cfg *config.Config) *settingsState {
	in := textinput.New()
	in.Placeholder = "/path/to/repos"
	in.CharLimit = 512
	s := &settingsState{cfg: cfg, input: in}
	s.rebuild()
	return s
}

// rebuild regenerates the row list from the current configuration.
func (s *settingsState) rebuild() {
	current := s.cursor
	s.rows = nil

	s.rows = append(s.rows, settingsRow{kind: rowSection, label: "SCAN DIRECTORIES"})
	for i, d := range s.cfg.Dirs {
		s.rows = append(s.rows, settingsRow{kind: rowDir, label: shortPath(d), index: i})
	}
	s.rows = append(s.rows, settingsRow{kind: rowAddDir, label: "add directory…"})

	s.rows = append(s.rows, settingsRow{kind: rowSection, label: "EXCLUDED REPOSITORIES"})
	if len(s.cfg.Ignored) == 0 {
		s.rows = append(s.rows, settingsRow{kind: rowInfo, label: "none — press i on a repository to exclude it"})
	}
	for i, p := range s.cfg.Ignored {
		s.rows = append(s.rows, settingsRow{
			kind:  rowIgnored,
			label: filepath.Base(p) + "  " + shortPath(p),
			index: i,
		})
	}

	s.rows = append(s.rows, settingsRow{kind: rowSection, label: "APPEARANCE & BEHAVIOUR"})
	s.rows = append(s.rows, settingsRow{kind: rowView, label: "Default view", value: string(s.cfg.View)})
	s.rows = append(s.rows, settingsRow{kind: rowTheme, label: "Theme", value: s.cfg.Theme.Preset})
	s.rows = append(s.rows, settingsRow{
		kind:  rowPrune,
		label: "Prune deleted remote branches on fetch",
		value: onOff(s.cfg.PruneOnFetch),
	})
	s.rows = append(s.rows, settingsRow{
		kind:  rowDepth,
		label: "Max scan depth",
		value: fmt.Sprintf("%d", s.cfg.MaxDepth),
	})

	s.cursor = clamp(current, 0, max(0, len(s.rows)-1))
	s.skipSections(1)
}

// skipSections moves the cursor off non-selectable rows.
func (s *settingsState) skipSections(dir int) {
	for i := 0; i < len(s.rows); i++ {
		if s.cursor < 0 || s.cursor >= len(s.rows) {
			return
		}
		if s.rows[s.cursor].kind != rowSection {
			return
		}
		s.cursor += dir
	}
	s.cursor = clamp(s.cursor, 0, max(0, len(s.rows)-1))
}

func (s *settingsState) move(dir int) {
	for i := 0; i < len(s.rows); i++ {
		s.cursor += dir
		if s.cursor < 0 {
			s.cursor = len(s.rows) - 1
		}
		if s.cursor >= len(s.rows) {
			s.cursor = 0
		}
		if s.rows[s.cursor].kind != rowSection {
			return
		}
	}
}

// handleSettingsKey processes input while the settings screen is open.
func (m *Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings

	if s.adding {
		key := msg.String()
		// Escape always cancels; backspace cancels once the path is empty so
		// it keeps deleting characters while typing.
		if key == "esc" || (isBackKey(key) && s.input.Value() == "") {
			s.adding = false
			s.input.Blur()
			return m, nil
		}
		switch key {
		case "enter":
			value := strings.TrimSpace(s.input.Value())
			s.adding = false
			s.input.Blur()
			s.input.SetValue("")
			if value == "" {
				return m, nil
			}
			dir := config.ExpandPath(value)
			if !s.cfg.AddDir(dir) {
				m.setStatus(statusWarn, "already scanning "+dir)
				return m, nil
			}
			m.setStatus(statusInfo, "scanning "+dir)
			s.rebuild()
			return m, tea.Batch(m.saveConfigCmd(), m.rescanCmd())
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "esc", "backspace", "ctrl+h", "q", ",":
		m.settings = nil
		m.st = newStyles(m.cfg.Theme)
		return m, tea.Batch(m.saveConfigCmd(), m.rescanCmd())

	case "j", "down":
		s.move(1)
	case "k", "up":
		s.move(-1)
	case "g", "home":
		s.cursor = 0
		s.skipSections(1)
	case "G", "end":
		s.cursor = len(s.rows) - 1
		s.skipSections(-1)

	case "a":
		if s.cfg != nil {
			s.adding = true
			s.input.SetValue("")
			return m, s.input.Focus()
		}

	case "d", "delete":
		if s.cursor < len(s.rows) {
			row := s.rows[s.cursor]
			switch row.kind {
			case rowDir:
				if row.index < len(s.cfg.Dirs) {
					dir := s.cfg.Dirs[row.index]
					if s.cfg.RemoveDir(dir) {
						m.setStatus(statusInfo, "stopped scanning "+shortPath(dir))
						s.rebuild()
						return m, tea.Batch(m.saveConfigCmd(), m.rescanCmd())
					}
				}
			case rowIgnored:
				if row.index < len(s.cfg.Ignored) {
					path := s.cfg.Ignored[row.index]
					s.cfg.SetIgnored(path, false)
					m.setStatus(statusInfo, "included "+filepath.Base(path)+" again")
					s.rebuild()
					return m, tea.Batch(m.saveConfigCmd(), m.rescanCmd())
				}
			}
		}

	case "enter", " ", "right", "left", "l", "h":
		if s.cursor >= len(s.rows) {
			return m, nil
		}
		row := s.rows[s.cursor]
		switch row.kind {
		case rowAddDir:
			s.adding = true
			s.input.SetValue("")
			return m, s.input.Focus()
		case rowView:
			if s.cfg.View == config.ViewList {
				s.cfg.View = config.ViewGrid
			} else {
				s.cfg.View = config.ViewList
			}
			s.rebuild()
			return m, m.saveConfigCmd()
		case rowTheme:
			s.cfg.CycleTheme()
			m.st = newStyles(s.cfg.Theme)
			m.spin.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(s.cfg.Theme.Accent))
			s.rebuild()
			return m, m.saveConfigCmd()
		case rowPrune:
			s.cfg.PruneOnFetch = !s.cfg.PruneOnFetch
			s.rebuild()
			return m, m.saveConfigCmd()
		case rowDepth:
			delta := 1
			if msg.String() == "left" || msg.String() == "h" {
				delta = -1
			}
			s.cfg.MaxDepth = clamp(s.cfg.MaxDepth+delta, 1, 12)
			s.rebuild()
			return m, tea.Batch(m.saveConfigCmd(), m.rescanCmd())
		}
	}
	return m, nil
}

// rescanCmd re-runs discovery after a preference change.
func (m *Model) rescanCmd() tea.Cmd {
	m.busy = true
	m.busyText = "rescanning"
	return tea.Batch(m.scanCmd(), m.spin.Tick)
}

// settingsView renders the preferences screen.
func (m *Model) settingsView(height int) string {
	s := m.settings
	var lines []string
	lines = append(lines, m.st.overlayTitle.Render("Settings"), "")

	start := 0
	// Budget every fixed line (title, blank, hints, config path, section
	// gaps) so the bottom of the screen is always visible; the rest of the
	// body scrolls the rows.
	overhead := 8
	if s.adding {
		overhead += 2
	}
	maxRows := max(2, height-overhead)
	if len(s.rows) > maxRows {
		start = clamp(s.cursor-maxRows/2, 0, len(s.rows)-maxRows)
	}
	end := min(len(s.rows), start+maxRows)

	for i := start; i < end; i++ {
		row := s.rows[i]
		switch row.kind {
		case rowSection:
			if i > start {
				lines = append(lines, "")
			}
			lines = append(lines, m.st.column.Render(row.label))
		case rowInfo:
			lines = append(lines, m.st.muted.Render("  "+row.label))
		default:
			cursor := "  "
			if i == s.cursor {
				cursor = m.st.accent.Render("▸ ")
			}
			label := row.label
			if i == s.cursor {
				label = m.st.overlayCursor.Render(label)
			} else {
				label = m.st.text.Render(label)
			}
			line := cursor + label
			if row.value != "" {
				line += m.st.badge.Render("  ") + m.st.accent.Render(row.value)
			}
			if row.kind == rowDir || row.kind == rowIgnored {
				line += m.st.badge.Render("   d to remove")
			}
			lines = append(lines, line)
		}
	}

	if s.adding {
		lines = append(lines, "", m.st.key.Render("new directory: ")+s.input.View())
	}

	lines = append(lines, "")
	lines = append(lines, m.st.muted.Render("enter/space change · a add directory · d remove · backspace close & save"))
	if m.cfgPath != "" {
		lines = append(lines, m.st.badge.Render(shortPath(m.cfgPath)))
	}

	box := m.st.overlay.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box)
}

func shortPath(p string) string {
	home, err := filepath.Rel(mustHome(), p)
	if err == nil && !strings.HasPrefix(home, "..") {
		return "~/" + home
	}
	return p
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
