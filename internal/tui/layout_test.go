package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dominickp/repoman/internal/config"
	"github.com/dominickp/repoman/internal/gitops"
)

func testModel(width, height int) *Model {
	cfg := config.Default()
	cfg.Dirs = []string{"/tmp/repos"}
	m := New(cfg, "")
	m.width, m.height = width, height
	m.busy = false
	m.repos = []*gitops.Snapshot{
		{
			Path: "/tmp/repos/alpha", Name: "alpha", Branch: "feature",
			DefaultBranch: "main", Remote: "origin", Upstream: "origin/feature",
			HasUpstream: true, Behind: 2, Ahead: 1,
			LastCommit: "add feature", LastCommitAt: time.Now().Add(-3 * time.Hour),
		},
		{
			Path: "/tmp/repos/beta", Name: "beta", Branch: "main",
			DefaultBranch: "main", Remote: "origin", Upstream: "origin/main",
			HasUpstream: true, Dirty: true, DirtyCount: 3,
		},
		{
			Path: "/tmp/repos/gamma", Name: "gamma-with-a-really-long-name",
			Branch: "main", DefaultBranch: "main", Detached: false,
		},
	}
	m.cfg.Ignored = []string{"/tmp/repos/gamma"}
	return m
}

// TestViewLinesFit checks that no rendered line is wider than the terminal,
// which is what causes wrapping artifacts in real terminals.
func TestViewLinesFit(t *testing.T) {
	for _, size := range [][2]int{{110, 30}, {80, 24}, {60, 20}, {40, 15}} {
		for _, view := range []config.ViewMode{config.ViewList, config.ViewGrid} {
			m := testModel(size[0], size[1])
			m.cfg.View = view
			out := m.View()
			lines := strings.Split(out, "\n")
			for i, line := range lines {
				if w := lipgloss.Width(line); w > size[0] {
					t.Errorf("%v %s line %d width %d > %d: %q",
						size, view, i, w, size[0], line)
				}
			}
			if got := len(lines); got > size[1] {
				t.Errorf("%v %s rendered %d lines, terminal height %d", size, view, got, size[1])
			}
		}
	}
}

func TestListViewContainsData(t *testing.T) {
	m := testModel(100, 25)
	m.cfg.View = config.ViewList
	out := m.View()
	for _, want := range []string{"alpha", "feature", "beta", "gamma-with-a-really-long-name", "↑1", "↓2"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in view:\n%s", want, out)
		}
	}
}

func TestGridViewRendersCompactColumns(t *testing.T) {
	m := testModel(100, 25)
	m.cfg.View = config.ViewGrid
	out := m.View()
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in grid view:\n%s", want, out)
		}
	}
	// Cells are a single line each, so several repos must share a row.
	lines := strings.Split(out, "\n")
	var row = -1
	for i, l := range lines {
		if strings.Contains(l, "alpha") {
			row = i
		}
	}
	if row == -1 || !strings.Contains(lines[row], "beta") {
		t.Errorf("expected alpha and beta on the same grid row:\n%s", out)
	}
}

// TestGridColumnsFitTerminal checks the column count for a terminal width.
func TestGridColumnsFitTerminal(t *testing.T) {
	m := testModel(120, 30)
	if got := m.gridColumns(); got != 3 {
		t.Errorf("gridColumns = %d, want 3", got)
	}
	m = testModel(40, 20)
	if got := m.gridColumns(); got != 1 {
		t.Errorf("gridColumns at 40 cols = %d, want 1", got)
	}
}

// TestSortModes checks that `s` reorders the repositories and cycles back.
func TestSortModes(t *testing.T) {
	m := testModel(100, 25)
	m.cfg.View = config.ViewGrid
	for i := 0; i < len(sortModeNames); i++ {
		m.cycleSort()
	}
	if m.sortMode != sortName {
		t.Errorf("sortMode = %d, want %d after a full cycle", m.sortMode, sortName)
	}
	// state sort puts the dirty repo first
	m.sortMode = sortState
	m.sortRepos()
	if m.repos[0].Name != "beta" {
		t.Errorf("state sort should list dirty beta first, got %q", m.repos[0].Name)
	}
}

// TestPerRepoActivityAnimation checks that individual repositories render
// their own spinner and activity text while a bulk operation runs.
func TestPerRepoActivityAnimation(t *testing.T) {
	m := testModel(100, 25)
	m.cfg.View = config.ViewList
	m.activity = map[string]repoActivity{
		"/tmp/repos/alpha": {kind: actFetching, running: true},
		"/tmp/repos/beta":  {kind: actFetching},
	}

	out := m.View()
	if !strings.Contains(out, "fetching") {
		t.Errorf("expected a running repository to show 'fetching':\n%s", out)
	}
	if !strings.Contains(out, "queued") {
		t.Errorf("expected an idle repository to show 'queued':\n%s", out)
	}

	// Advancing the animation frame must change the rendered spinner.
	before := out
	m.animFrame++
	if after := m.View(); after == before {
		t.Errorf("spinner did not animate between frames:\n%s", after)
	}
	for i, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 100 {
			t.Errorf("line %d too wide: %q", i, line)
		}
	}

	// Grid view must animate the card too.
	m.cfg.View = config.ViewGrid
	if out := m.View(); !strings.Contains(out, "fetching") && !strings.Contains(out, "queued") {
		t.Errorf("expected activity text in grid view:\n%s", out)
	}
}

// TestFlashesFadeOut checks that a finished repository keeps its outcome on
// screen for a few frames and then disappears.
func TestFlashesFadeOut(t *testing.T) {
	m := testModel(100, 25)
	m.cfg.View = config.ViewList
	m.activity = nil
	m.flashes = map[string]flash{
		"/tmp/repos/beta": {text: "fetched", kind: statusOK, remaining: 2},
	}

	if !strings.Contains(m.View(), "fetched") {
		t.Fatalf("expected the outcome flash in view:\n%s", m.View())
	}
	if !m.ageFlashes() {
		t.Error("ageFlashes should still report a visible flash")
	}
	if m.ageFlashes() {
		t.Error("ageFlashes should report no flashes once they expire")
	}
	if strings.Contains(m.View(), "fetched") {
		t.Error("expired flash should not be rendered")
	}
}

// TestBackspaceIsTheBackKey checks that backspace backs out of the filter and
// settings, while still deleting characters inside a text input.
func TestBackspaceIsTheBackKey(t *testing.T) {
	m := testModel(100, 25)

	// Main view: backspace with an active filter clears it.
	m.filter = "al"
	m.handleKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filter != "" {
		t.Errorf("backspace should clear the filter, got %q", m.filter)
	}

	// Filter input: backspace deletes text, then leaves when empty.
	m.filtering = true
	m.filterInput.Focus()
	m.filterInput.SetValue("alp")
	m.filterInput.CursorEnd()
	m.handleFilterKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if !m.filtering {
		t.Fatal("backspace should edit the input, not close it")
	}
	if got := m.filterInput.Value(); got != "al" {
		t.Errorf("input = %q, want %q", got, "al")
	}
	m.filterInput.SetValue("")
	m.handleFilterKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filtering {
		t.Error("backspace on an empty filter should leave filter mode")
	}

	// Settings: backspace closes and saves.
	m.settings = newSettingsState(m.cfg)
	m.handleSettingsKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.settings != nil {
		t.Error("backspace should close the settings screen")
	}
}

// TestHeaderHidesTheme checks the header badge no longer repeats the theme,
// which is already visible in settings.
func TestHeaderHidesTheme(t *testing.T) {
	m := testModel(100, 25)
	if out := m.headerView(); strings.Contains(out, m.cfg.Theme.Preset) {
		t.Errorf("header should not show the theme preset:\n%s", out)
	}
}

func TestSettingsViewFits(t *testing.T) {
	m := testModel(100, 30)
	m.settings = newSettingsState(m.cfg)
	out := m.View()
	for i, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > 100 {
			t.Errorf("settings line %d too wide: %q", i, line)
		}
	}
	if !strings.Contains(out, "SCAN DIRECTORIES") || !strings.Contains(out, "tokyo-night") {
		t.Errorf("settings screen missing content:\n%s", out)
	}
}

// TestOverlayViewsFitSmallTerminals checks the settings and help screens keep
// their bottom hint line visible on short terminals.
func TestOverlayViewsFitSmallTerminals(t *testing.T) {
	for _, height := range []int{10, 12, 16, 20} {
		m := testModel(80, height)

		m.settings = newSettingsState(m.cfg)
		out := m.View()
		if lines := strings.Split(out, "\n"); len(lines) > height {
			t.Errorf("settings at height %d rendered %d lines:\n%s", height, len(lines), out)
		}
		if !strings.Contains(out, "backspace close & save") {
			t.Errorf("settings hint missing at height %d:\n%s", height, out)
		}

		m.settings = nil
		m.help = true
		m.helpOffset = 100 // scrolled to the bottom
		out = m.View()
		if lines := strings.Split(out, "\n"); len(lines) > height {
			t.Errorf("help at height %d rendered %d lines:\n%s", height, len(lines), out)
		}
		// Scrolling past the end must clamp so the last line stays reachable.
		m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		if strings.Contains(m.View(), "Key bindings") && height < 25 {
			t.Errorf("expected scrolled help to hide the title at height %d", height)
		}
	}
}
