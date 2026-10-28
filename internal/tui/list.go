package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/dominickp/repoman/internal/gitops"
)

// listView renders the table layout.
func (m *Model) listView(height int) string {
	vis := m.visible()
	if len(vis) == 0 {
		return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center,
			m.st.muted.Render("no repositories match "+fmt.Sprintf("%q", m.filter)))
	}

	detail := m.detailLine()
	detailHeight := 0
	if detail != "" {
		detailHeight = 2 // blank separator + detail
	}

	rowsHeight := height - 1 - detailHeight // minus column header
	if rowsHeight < 1 {
		rowsHeight = 1
	}

	// Keep the cursor inside the scroll window.
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rowsHeight {
		m.offset = m.cursor - rowsHeight + 1
	}
	if m.offset > len(vis)-rowsHeight {
		m.offset = max(0, len(vis)-rowsHeight)
	}

	lines := []string{m.listHeader()}
	end := min(len(vis), m.offset+rowsHeight)
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.listRow(vis[i], i == m.cursor))
	}
	for len(lines)-1 < rowsHeight {
		lines = append(lines, "")
	}
	if detail != "" {
		lines = append(lines, "", detail)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// listColumns computes the widths of the table columns for the current width.
type listColumns struct{ name, branch, sync, state int }

func (m *Model) listColumns() listColumns {
	const marker = 2
	const gaps = 6 // two spaces before each of branch, sync and state

	syncWidth := 11
	branch := clamp(m.width*18/100, 10, 22)
	state := clamp(m.width*16/100, 8, 18)
	name := m.width - marker - syncWidth - branch - state - gaps

	// Narrow terminals: drop the state column first, then the sync column.
	if name < 16 {
		state = 0
		name = m.width - marker - syncWidth - branch - gaps + 2
	}
	if name < 14 {
		syncWidth = 0
		branch = clamp(m.width/3, 8, 22)
		name = m.width - marker - branch - gaps + 4
	}
	return listColumns{
		name:   max(8, name),
		branch: branch,
		sync:   syncWidth,
		state:  state,
	}
}

func (m *Model) listHeader() string {
	c := m.listColumns()
	line := "  " + fitStyled(m.st.column.Render("REPOSITORY"), c.name) + "  "
	if c.sync > 0 {
		sync := m.st.column.Render("SYNC")
		if c.state > 0 {
			sync = fitStyled(sync, c.sync)
		}
		line += fitStyled(m.st.column.Render("BRANCH"), c.branch) + "  " + sync
	} else {
		line += m.st.column.Render("BRANCH")
	}
	if c.state > 0 {
		line += "  " + m.st.column.Render("STATE")
	}
	return truncate(line, m.width)
}

func (m *Model) listRow(s *gitops.Snapshot, selected bool) string {
	c := m.listColumns()

	// The marker column doubles as a per-repository spinner: while a fetch or
	// update runs, each row animates only when that repository is in flight.
	marker := "  "
	if glyph, ok := m.activityGlyph(s.Path); ok {
		marker = glyph
	} else if selected {
		marker = m.st.accent.Render("▸ ")
	}

	name := s.Name
	if selected {
		name = m.st.selected.Render(name)
	} else {
		name = m.st.row.Render(name)
	}

	branch := s.Branch
	switch {
	case s.Detached:
		branch = m.st.danger.Render(branch)
	case !s.OnDefaultBranch():
		branch = m.st.warn.Render(branch)
	default:
		branch = m.st.ok.Render(branch)
	}
	if s.UpstreamGone {
		branch += m.st.danger.Render(" ✗")
	}

	row := marker + fitStyled(name, c.name) + "  " + fitStyled(branch, c.branch)
	if c.sync > 0 {
		row += "  " + fitStyled(m.syncCell(s), c.sync)
	}
	if c.state > 0 {
		row += "  " + m.stateCell(s, c.state)
	}
	return truncate(row, m.width)
}

// syncCell renders "↑ahead ↓behind" for a repository.
func (m *Model) syncCell(s *gitops.Snapshot) string {
	if s.Detached {
		return m.st.muted.Render("—")
	}
	if s.UpstreamGone {
		return m.st.danger.Render("deleted")
	}
	if !s.HasUpstream {
		return m.st.muted.Render("—")
	}
	if s.Ahead == 0 && s.Behind == 0 {
		return m.st.badge.Render("in sync")
	}
	var parts []string
	if s.Ahead > 0 {
		parts = append(parts, m.st.warn.Render(fmt.Sprintf("↑%d", s.Ahead)))
	}
	if s.Behind > 0 {
		parts = append(parts, m.st.accent.Render(fmt.Sprintf("↓%d", s.Behind)))
	}
	return strings.Join(parts, " ")
}

// stateCell renders the trailing status flags for a repository.
func (m *Model) stateCell(s *gitops.Snapshot, width int) string {
	// Activity and its short-lived outcome take precedence over the static
	// state so the column narrates what the operation is doing.
	if text, ok := m.activityText(s.Path); ok {
		return ellipsize(text, width)
	}

	var parts []string
	if s.Err != "" {
		parts = append(parts, m.st.danger.Render("!"))
	}
	if s.Dirty {
		parts = append(parts, m.st.danger.Render(fmt.Sprintf("●%d", s.DirtyCount)))
	}
	if m.cfg.IsIgnored(s.Path) {
		parts = append(parts, m.st.muted.Faint(true).Render("⊘"))
	}
	if s.UpstreamGone {
		parts = append(parts, m.st.warn.Render("⇡merged"))
	}
	if len(parts) == 0 && s.HasUpstream && s.Ahead == 0 && s.Behind == 0 {
		parts = append(parts, m.st.ok.Render("✓"))
	}
	return ellipsize(strings.Join(parts, " "), width)
}

// detailLine describes the selected repository.
func (m *Model) detailLine() string {
	s := m.selected()
	if s == nil || m.height < 14 {
		return ""
	}
	when := "unknown"
	if !s.LastCommitAt.IsZero() {
		when = humanize(time.Since(s.LastCommitAt))
		if when != "just now" {
			when += " ago"
		}
	}
	parts := []string{
		m.st.muted.Render(shortPath(s.Path)),
		m.st.badge.Render("·"),
		m.st.muted.Render(when),
	}
	return truncate(strings.Join(parts, " "), m.width)
}

// gridGutter is the number of spaces between grid columns.
const gridGutter = 2

// gridView renders the compact multi-column layout: each repository is one
// short line, packed several per row so a screenful shows many more
// repositories than the table view.
func (m *Model) gridView(height int) string {
	vis := m.visible()
	if len(vis) == 0 {
		return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center,
			m.st.muted.Render("no repositories match "+fmt.Sprintf("%q", m.filter)))
	}

	cols := m.gridColumns()
	rowsVisible := max(1, height)
	totalRows := (len(vis) + cols - 1) / cols

	// The offset counts rows here (not items), and scrolls to keep the
	// cursor's row inside the window.
	cursorRow := m.cursor / cols
	if m.offset > cursorRow {
		m.offset = cursorRow
	}
	if m.offset > max(0, totalRows-rowsVisible) {
		m.offset = max(0, totalRows-rowsVisible)
	}
	if cursorRow >= m.offset+rowsVisible {
		m.offset = cursorRow - rowsVisible + 1
	}

	var lines []string
	for r := range rowsVisible {
		start := (m.offset + r) * cols
		if start >= len(vis) {
			break
		}
		end := min(len(vis), start+cols)
		var cells []string
		for i := start; i < end; i++ {
			cells = append(cells, fitStyled(m.gridCell(vis[i], i == m.cursor), m.gridCellWidth()))
		}
		// Pad partial rows so the columns line up.
		for len(cells) < cols {
			cells = append(cells, strings.Repeat(" ", m.gridCellWidth()))
		}
		lines = append(lines, strings.Join(cells, strings.Repeat(" ", gridGutter)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// gridCellWidth is the width of one grid cell. Cells are short by design so
// several columns fit across the terminal.
func (m *Model) gridCellWidth() int {
	w := 30
	if m.width < 30 {
		w = max(16, m.width)
	}
	return w
}

// gridColumns reports how many cells fit across the terminal, accounting for
// the gutter between columns.
func (m *Model) gridColumns() int {
	return max(1, (m.width+gridGutter)/(m.gridCellWidth()+gridGutter))
}

// gridCell renders one repository as a single short line: name plus a
// compact sync/state summary.
func (m *Model) gridCell(s *gitops.Snapshot, selected bool) string {
	w := m.gridCellWidth()

	marker := ""
	if glyph, ok := m.activityGlyph(s.Path); ok {
		marker = glyph
	} else if selected {
		marker = m.st.accent.Render("▸ ")
	}

	name := s.Name
	switch {
	case selected:
		name = m.st.selected.Render(name)
	case m.cfg.IsIgnored(s.Path):
		name = m.st.muted.Faint(true).Render(name)
	default:
		name = m.st.row.Render(name)
	}

	state := m.gridStateCell(s)

	markerW := lipgloss.Width(marker)
	stateW := clamp(w*3/10, 6, 12)
	nameW := w - markerW - stateW - 1
	return marker + fitStyled(name, nameW) + " " + fitStyled(state, stateW)
}

// gridStateCell condenses the sync and state columns of the table view into
// a few characters: ahead/behind counts, or the running activity / outcome
// flash, plus a dirty marker.
func (m *Model) gridStateCell(s *gitops.Snapshot) string {
	if text, ok := m.activityText(s.Path); ok {
		return text
	}
	if s.Err != "" {
		return m.st.danger.Render("error")
	}
	if s.Detached {
		return m.st.danger.Render("detached")
	}
	parts := []string{}
	if s.Dirty {
		parts = append(parts, m.st.danger.Render("●"))
	}
	if s.UpstreamGone {
		parts = append(parts, m.st.warn.Render("gone"))
	} else if !s.HasUpstream {
		parts = append(parts, m.st.muted.Render("no remote"))
	}
	sync := m.syncCell(s)
	if s.HasUpstream && !s.UpstreamGone {
		parts = append(parts, sync)
	}
	if len(parts) == 0 {
		return m.st.badge.Render("in sync")
	}
	return strings.Join(parts, " ")
}

// activityGlyph returns the animated spinner that replaces the selection
// marker while a repository is part of a running operation. Repositories that
// are waiting for a worker show a dim dot instead.
func (m *Model) activityGlyph(path string) (string, bool) {
	a, ok := m.activity[path]
	if !ok {
		return "", false
	}
	if !a.running {
		return m.st.muted.Faint(true).Render("· "), true
	}
	return m.st.accent.Render(animFrames[m.animFrame%len(animFrames)] + " "), true
}

// activityText describes what a repository is doing right now, or the outcome
// flash just after it finished.
func (m *Model) activityText(path string) (string, bool) {
	if f, ok := m.flashes[path]; ok {
		return m.flashStyle(f.kind).Render(f.text), true
	}
	a, ok := m.activity[path]
	if !ok {
		return "", false
	}
	if !a.running {
		return m.st.muted.Faint(true).Render("queued"), true
	}
	verb := "fetching"
	if a.kind == actUpdating {
		verb = "updating"
	}
	return m.st.accent.Render(verb), true
}

// flashStyle maps a status kind to its colour style.
func (m *Model) flashStyle(kind statusKind) lipgloss.Style {
	switch kind {
	case statusOK:
		return m.st.ok
	case statusWarn:
		return m.st.warn
	case statusErr:
		return m.st.danger
	default:
		return m.st.muted
	}
}

// --- formatting helpers ----------------------------------------------------

// ellipsize truncates styled text to width, marking the cut with an
// ellipsis so a clipped cell is visibly shortened instead of ending
// mid-word at the window edge.
func ellipsize(s string, width int) string {
	if width < 3 || lipgloss.Width(s) <= width {
		return truncate(s, width)
	}
	return truncate(s, width-1) + "…"
}

// fitStyled truncates styled text to width and right-pads it with spaces so
// neighbouring columns line up. ANSI sequences are handled by lipgloss.Width.
func fitStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) > width {
		s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	}
	if pad := width - lipgloss.Width(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// humanize renders a coarse duration, e.g. "3d" or "2h".
func humanize(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/(24*365)))
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
