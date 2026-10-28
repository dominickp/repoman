// Package tui implements the repoman terminal interface.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dominickp/repoman/internal/config"
	"github.com/dominickp/repoman/internal/gitops"
)

type statusKind int

const (
	statusInfo statusKind = iota
	statusOK
	statusWarn
	statusErr
)

// sortMode is the ordering applied to the repository list; `s` cycles it.
type sortMode int

const (
	sortName sortMode = iota
	sortState
	sortSync
	sortRecent
)

var sortModeNames = [...]string{"name", "state", "sync", "recent commit"}

// Model is the Bubble Tea model for the whole application.
type Model struct {
	cfg     *config.Config
	cfgPath string
	st      styles

	repos    []*gitops.Snapshot
	cursor   int
	offset   int
	sortMode sortMode

	width  int
	height int

	busy     bool
	spin     spinner.Model
	progress int
	total    int
	busyText string
	runLabel string
	runStats stats

	status string
	kind   statusKind
	// statusTicks is how many animation frames the current status still
	// has left before it fades. A negative value means it is persistent
	// (the busy "fetching…" style messages are replaced on completion).
	statusTicks int

	// Per-repository activity for the operation that is running right now,
	// plus short-lived flashes of the outcome once a repository finishes.
	activity  map[string]repoActivity
	flashes   map[string]flash
	animFrame int

	filter      string
	filtering   bool
	filterInput textinput.Model

	settings *settingsState
	help     bool
	// helpOffset scrolls the help overlay on short terminals.
	helpOffset int

	events chan workMsg
}

// Run starts the interface and blocks until the user quits.
func Run(cfg *config.Config, cfgPath string) error {
	p := tea.NewProgram(New(cfg, cfgPath), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// New builds the initial model.
func New(cfg *config.Config, cfgPath string) *Model {
	sp := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color(cfg.Theme.Accent))),
	)
	fi := textinput.New()
	fi.Placeholder = "filter repositories"
	fi.CharLimit = 64

	m := &Model{
		cfg:         cfg,
		cfgPath:     cfgPath,
		st:          newStyles(cfg.Theme),
		spin:        sp,
		filterInput: fi,
		busy:        true,
		busyText:    "scanning for repositories",
	}
	return m
}

// --- messages ---------------------------------------------------------------

type scannedMsg struct {
	repos []*gitops.Snapshot
	errs  []error
}

type workMsg struct {
	label   string
	path    string
	started bool
	err     error
	snap    *gitops.Snapshot
	result  *gitops.UpdateResult
}

type streamClosedMsg struct{}

type savedMsg struct{ err error }

// openVSCodeMsg reports the outcome of launching VS Code on a repository.
type openVSCodeMsg struct{ err error }

// stats tallies the outcomes of a bulk operation.
type stats struct {
	updated  int
	upToDate int
	switched int
	skipped  int
	failed   int
}

// activityKind is what a repository is doing in a running bulk operation.
type activityKind int

const (
	actQueued activityKind = iota
	actFetching
	actUpdating
)

// repoActivity tracks one repository inside a running bulk operation.
type repoActivity struct {
	kind    activityKind
	running bool
}

// flash briefly highlights the outcome of a repository after it finishes.
type flash struct {
	text      string
	kind      statusKind
	remaining int
}

// flashTicks is how long an outcome stays visible, in animation frames. The
// spinner ticks ten times a second, so this is roughly 1.5 seconds.
const flashTicks = 15

// statusTicks is how long a transient status message stays visible, in
// animation frames — about 3 seconds.
const statusTicks = 30

// animFrames are the per-repository spinner glyphs. Every frame is one cell
// wide so rows and cards keep their alignment.
var animFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// flashForOutcome picks the short text shown after an update finishes.
func flashForOutcome(o gitops.Outcome) flash {
	switch o {
	case gitops.OutcomeUpdated:
		return flash{text: "updated", kind: statusOK}
	case gitops.OutcomeSwitched:
		return flash{text: "switched", kind: statusOK}
	case gitops.OutcomeUpToDate:
		return flash{text: "up to date", kind: statusInfo}
	case gitops.OutcomeSkipped:
		return flash{text: "skipped", kind: statusWarn}
	case gitops.OutcomeFailed:
		return flash{text: "failed", kind: statusErr}
	}
	return flash{}
}

func waitForWork(ch chan workMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return streamClosedMsg{}
		}
		return msg
	}
}

// --- lifecycle --------------------------------------------------------------

// Init kicks off the first repository scan.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), m.spin.Tick)
}

func (m *Model) scanCmd() tea.Cmd {
	dirs := append([]string(nil), m.cfg.Dirs...)
	depth := m.cfg.MaxDepth
	return func() tea.Msg {
		paths, errs := gitops.Discover(dirs, depth)
		snaps := gitops.MapInspect(context.Background(), paths, 8)
		return scannedMsg{repos: snaps, errs: errs}
	}
}

// --- update ----------------------------------------------------------------

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Keep one column spare: writing a line that reaches the final column
		// leaves some terminals in a pending-wrap state.
		m.width = max(1, msg.Width-1)
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		// Every spinner tick doubles as an animation frame for the
		// per-repository spinners in the list and grid.
		m.animFrame++
		flashing := m.ageFlashes()
		m.ageStatus()
		if !m.busy && !flashing && m.statusTicks == 0 {
			return m, nil
		}
		return m, cmd

	case scannedMsg:
		m.busy = false
		m.busyText = ""
		m.progress, m.total = 0, 0
		m.repos = msg.repos
		m.sortRepos()
		m.clampCursor()
		if len(msg.errs) > 0 {
			m.setStatus(statusWarn, "scan: "+msg.errs[0].Error())
		}
		return m, nil

	case workMsg:
		m.handleWork(msg)
		if m.events == nil {
			return m, nil
		}
		return m, waitForWork(m.events)

	case streamClosedMsg:
		m.busy = false
		m.activity = nil
		m.busyText = ""
		// Fetches show no summary when everything succeeded: the sync
		// columns already tell the story. Update summaries and failures
		// are still reported.
		summary := m.runSummary()
		m.setStatusTransient(m.runStats.kind(), summary)
		return m, nil

	case savedMsg:
		if msg.err != nil {
			m.setStatus(statusErr, "saving config: "+msg.err.Error())
		}
		return m, nil

	case openVSCodeMsg:
		if msg.err != nil {
			m.setStatus(statusErr, "opening vscode: "+cleanErr(msg.err))
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleWork(msg workMsg) {
	// A start event only flips the repository from queued to running, so the
	// per-repository spinner shows which ones are actually in flight.
	if msg.started {
		if a, ok := m.activity[msg.path]; ok {
			a.running = true
			m.activity[msg.path] = a
		}
		return
	}

	m.progress++
	if msg.snap != nil {
		m.upsert(msg.snap)
		m.sortRepos()
		m.clampCursor()
	}
	if msg.result != nil {
		m.finishActivity(msg.path, flashForOutcome(msg.result.Outcome))
		switch msg.result.Outcome {
		case gitops.OutcomeUpdated:
			m.runStats.updated++
			m.setStatusTransient(statusOK, msg.result.Message())
		case gitops.OutcomeSwitched:
			m.runStats.switched++
			m.setStatusTransient(statusOK, msg.result.Message())
		case gitops.OutcomeUpToDate:
			m.runStats.upToDate++
			if m.progress == m.total {
				m.setStatusTransient(statusInfo, msg.result.Message())
			}
		case gitops.OutcomeSkipped:
			m.runStats.skipped++
			m.setStatusTransient(statusWarn, msg.result.Message())
		case gitops.OutcomeFailed:
			m.runStats.failed++
			m.setStatusTransient(statusErr, msg.result.Message())
		}
		return
	}
	if msg.err != nil {
		m.runStats.failed++
		m.finishActivity(msg.path, flash{text: "fetch failed", kind: statusErr})
		m.setStatusTransient(statusErr, fmt.Sprintf("%s: %s", baseName(msg.path), cleanErr(msg.err)))
		return
	}
	m.finishActivity(msg.path, flash{text: "fetched", kind: statusOK})
}

// finishActivity clears a repository's running state and starts the flash
// that briefly shows its outcome.
func (m *Model) finishActivity(path string, f flash) {
	delete(m.activity, path)
	if f.text == "" {
		return
	}
	f.remaining = flashTicks
	if m.flashes == nil {
		m.flashes = make(map[string]flash, 4)
	}
	m.flashes[path] = f
}

// ageFlashes advances every flash by one animation frame and reports whether
// any are still visible, so ticking can continue after the run finishes.
func (m *Model) ageFlashes() bool {
	if len(m.flashes) == 0 {
		return false
	}
	for path, f := range m.flashes {
		f.remaining--
		if f.remaining <= 0 {
			delete(m.flashes, path)
			continue
		}
		m.flashes[path] = f
	}
	return len(m.flashes) > 0
}

// runSummary describes the outcome of the last bulk operation.
func (m *Model) runSummary() string {
	s := m.runStats
	if m.runLabel == "fetch" {
		if s.failed > 0 {
			return fmt.Sprintf("fetched %d/%d repositories (%d failed)", m.total-s.failed, m.total, s.failed)
		}
		// Everything fetched fine: the sync columns already show it.
		return ""
	}

	var parts []string
	if s.updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", s.updated))
	}
	if s.switched > 0 {
		parts = append(parts, fmt.Sprintf("%d switched", s.switched))
	}
	if s.upToDate > 0 {
		parts = append(parts, fmt.Sprintf("%d up to date", s.upToDate))
	}
	if s.skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", s.skipped))
	}
	if s.failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", s.failed))
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return "update: " + strings.Join(parts, " · ")
}

// kind picks the status colour for a bulk operation summary.
func (s stats) kind() statusKind {
	switch {
	case s.failed > 0:
		return statusErr
	case s.skipped > 0:
		return statusWarn
	case s.updated > 0 || s.switched > 0:
		return statusOK
	default:
		return statusInfo
	}
}

func cleanErr(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return msg
}

// handleHelpKey processes input while the help overlay is open: j/k (and the
// arrow keys) scroll the bindings on short terminals, any other key closes it.
func (m *Model) handleHelpKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "j", "down":
		m.helpOffset++
	case "k", "up":
		m.helpOffset--
	case "home", "g":
		m.helpOffset = 0
	case "end", "G":
		m.helpOffset = 1 << 20 // clamped against the content in helpView
	default:
		m.help = false
		m.helpOffset = 0
	}
	return m, nil
}

// ageStatus fades a transient status message once nothing is running.
// Persistent statuses (progress lines) keep their negative tick count and
// are left alone until something replaces them.
func (m *Model) ageStatus() {
	if m.busy || m.statusTicks <= 0 {
		return
	}
	m.statusTicks--
	if m.statusTicks == 0 {
		m.status, m.kind = "", statusInfo
	}
}

func (m *Model) setStatus(kind statusKind, text string) {
	m.kind = kind
	m.status = text
	m.statusTicks = -1 // persistent until replaced
}

// setStatusTransient shows an outcome message that fades away on its own
// once nothing is running, so the status line does not linger forever.
func (m *Model) setStatusTransient(kind statusKind, text string) {
	m.kind = kind
	m.status = text
	m.statusTicks = statusTicks
}

// --- key handling ----------------------------------------------------------

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		return m, tea.Quit
	}

	if m.settings != nil {
		return m.handleSettingsKey(msg)
	}
	if m.help {
		return m.handleHelpKey(key)
	}
	if m.filtering {
		return m.handleFilterKey(msg)
	}

	// Backspace (or escape) backs out of the current filter.
	if isBackKey(key) {
		if m.filter != "" {
			m.clearFilter()
		}
		return m, nil
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
	case ",":
		m.settings = newSettingsState(m.cfg)
	case "tab":
		m.toggleView()
	case "/":
		m.filtering = true
		m.filterInput.SetValue(m.filter)
		m.filterInput.CursorEnd()
		return m, m.filterInput.Focus()
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "l", "right":
		m.moveCursor(m.gridStep())
	case "h", "left":
		m.moveCursor(-m.gridStep())
	case "G", "end":
		m.cursor = len(m.visible()) - 1
		m.clampCursor()
	case "home":
		m.cursor = 0
		m.clampCursor()
	case "i":
		m.toggleIgnore()
	case "o":
		return m, m.openVSCode()
	case "r":
		return m, m.fetchAll()
	case "s":
		m.cycleSort()
	case "u":
		return m, m.updateSelected()
	case "U":
		return m, m.updateAll()
	}
	return m, nil
}

// isBackKey reports whether a key means "go back / cancel". Backspace is the
// primary back key because it is where the fingers already are; escape stays
// as an alias for muscle memory. Some terminals send backspace as ctrl+h.
func isBackKey(key string) bool {
	return key == "backspace" || key == "ctrl+h" || key == "esc"
}

// clearFilter drops the active filter and resets the cursor.
func (m *Model) clearFilter() {
	m.filter = ""
	m.filterInput.SetValue("")
	m.cursor = 0
	m.clampCursor()
}

func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Escape always leaves the filter. Backspace only leaves once the input is
	// empty, so it keeps deleting characters while you type.
	if key == "esc" || (isBackKey(key) && m.filterInput.Value() == "") {
		m.filtering = false
		m.filterInput.Blur()
		m.clearFilter()
		return m, nil
	}

	switch key {
	case "enter":
		m.filtering = false
		m.filterInput.Blur()
		m.filter = strings.TrimSpace(m.filterInput.Value())
		m.cursor = 0
		m.clampCursor()
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.filter = strings.TrimSpace(m.filterInput.Value())
	m.clampCursor()
	return m, cmd
}

// --- actions ---------------------------------------------------------------

func (m *Model) toggleView() {
	if m.cfg.View == config.ViewGrid {
		m.cfg.View = config.ViewList
	} else {
		m.cfg.View = config.ViewGrid
	}
	m.saveConfig()
}

func (m *Model) toggleIgnore() {
	snap := m.selected()
	if snap == nil {
		return
	}
	if m.cfg.ToggleIgnored(snap.Path) {
		m.setStatus(statusInfo, baseName(snap.Path)+" excluded from bulk updates")
	} else {
		m.setStatus(statusInfo, baseName(snap.Path)+" included in bulk updates")
	}
	m.saveConfig()
}

// openVSCode launches the selected repository in VS Code (`code`). The
// editor is started detached so the interface keeps running underneath it.
func (m *Model) openVSCode() tea.Cmd {
	snap := m.selected()
	if snap == nil {
		return nil
	}
	path := snap.Path
	return func() tea.Msg {
		cmd := exec.Command("code", path)
		cmd.Stdout = nil
		cmd.Stderr = nil
		return openVSCodeMsg{err: cmd.Start()}
	}
}

func (m *Model) saveConfig() {
	if err := m.cfg.Save(m.cfgPath); err != nil {
		m.setStatus(statusErr, "saving config: "+err.Error())
	}
}

func (m *Model) saveConfigCmd() tea.Cmd {
	path := m.cfgPath
	cfg := m.cfg
	return func() tea.Msg { return savedMsg{err: cfg.Save(path)} }
}

// fetchAll refreshes every repository. Fetching only updates remote-tracking
// refs and never touches local branches or the working tree, so there is no
// reason to fetch a single repository and risk leaving the rest stale.
func (m *Model) fetchAll() tea.Cmd {
	paths := make([]string, 0, len(m.repos))
	for _, r := range m.repos {
		paths = append(paths, r.Path)
	}
	if len(paths) == 0 {
		return nil
	}
	return m.startFetch(paths)
}

func (m *Model) updateSelected() tea.Cmd {
	snap := m.selected()
	if snap == nil {
		return nil
	}
	return m.startUpdate([]gitops.UpdateJob{{Path: snap.Path, Opts: m.updateOptions()}})
}

func (m *Model) updateAll() tea.Cmd {
	var jobs []gitops.UpdateJob
	for _, r := range m.repos {
		if m.cfg.IsIgnored(r.Path) {
			continue
		}
		jobs = append(jobs, gitops.UpdateJob{Path: r.Path, Opts: m.updateOptions()})
	}
	if len(jobs) == 0 {
		m.setStatus(statusWarn, "no repositories to update")
		return nil
	}
	return m.startUpdate(jobs)
}

// updateOptions returns the safety settings used for update actions.
func (m *Model) updateOptions() gitops.UpdateOptions {
	return gitops.UpdateOptions{Fetch: true, Prune: m.cfg.PruneOnFetch}
}

// beginRun marks every repository in the batch as queued and sets up the
// message channel that reports their progress.
func (m *Model) beginRun(label string, kind activityKind, paths []string) chan workMsg {
	ch := make(chan workMsg, 64)
	m.events = ch
	m.busy = true
	m.busyText = label
	m.runLabel = label
	m.runStats = stats{}
	m.progress, m.total = 0, len(paths)
	m.activity = make(map[string]repoActivity, len(paths))
	for _, p := range paths {
		m.activity[p] = repoActivity{kind: kind}
	}
	m.flashes = nil
	return ch
}

func (m *Model) startFetch(paths []string) tea.Cmd {
	ch := m.beginRun("fetch", actFetching, paths)
	prune := m.cfg.PruneOnFetch
	m.setStatus(statusInfo, fmt.Sprintf("fetching %d repositor%s…", len(paths), pluralWord(len(paths))))

	go func() {
		defer close(ch)
		for ev := range gitops.FetchMany(context.Background(), paths, prune, 4) {
			if ev.Started {
				ch <- workMsg{label: "fetch", path: ev.Path, started: true}
				continue
			}
			snap := gitops.Inspect(context.Background(), ev.Path)
			ch <- workMsg{label: "fetch", path: ev.Path, err: ev.Err, snap: snap}
		}
	}()
	return tea.Batch(waitForWork(ch), m.spin.Tick)
}

func (m *Model) startUpdate(jobs []gitops.UpdateJob) tea.Cmd {
	paths := make([]string, len(jobs))
	for i, job := range jobs {
		paths[i] = job.Path
	}
	ch := m.beginRun("update", actUpdating, paths)
	m.setStatus(statusInfo, fmt.Sprintf("updating %d repositor%s…", len(jobs), pluralWord(len(jobs))))

	go func() {
		defer close(ch)
		for ev := range gitops.StreamUpdates(context.Background(), jobs) {
			if ev.Started || ev.Result == nil {
				ch <- workMsg{label: "update", path: ev.Path, started: true}
				continue
			}
			res := *ev.Result
			snap := gitops.Inspect(context.Background(), res.Path)
			ch <- workMsg{label: "update", path: res.Path, result: &res, snap: snap}
		}
	}()
	return tea.Batch(waitForWork(ch), m.spin.Tick)
}

// --- helpers ---------------------------------------------------------------

func (m *Model) upsert(snap *gitops.Snapshot) {
	for i, r := range m.repos {
		if r.Path == snap.Path {
			m.repos[i] = snap
			return
		}
	}
	m.repos = append(m.repos, snap)
}

func (m *Model) sortRepos() {
	sort.SliceStable(m.repos, func(i, j int) bool {
		a, b := m.repos[i], m.repos[j]
		switch m.sortMode {
		case sortState:
			if ra, rb := stateRank(a), stateRank(b); ra != rb {
				return ra > rb
			}
		case sortSync:
			if ka, kb := a.Ahead+a.Behind, b.Ahead+b.Behind; ka != kb {
				return ka > kb
			}
		case sortRecent:
			if !a.LastCommitAt.Equal(b.LastCommitAt) {
				return a.LastCommitAt.After(b.LastCommitAt)
			}
		}
		if a.Name != b.Name {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.Path < b.Path
	})
}

// stateRank scores how much a repository needs attention, so sortState lists
// problems first.
func stateRank(s *gitops.Snapshot) int {
	switch {
	case s.Err != "":
		return 4
	case s.Detached:
		return 3
	case s.Dirty:
		return 2
	case s.UpstreamGone:
		return 1
	}
	return 0
}

// cycleSort advances to the next sort order.
func (m *Model) cycleSort() {
	m.sortMode = (m.sortMode + 1) % sortMode(len(sortModeNames))
	m.sortRepos()
	m.clampCursor()
}

func (m *Model) visible() []*gitops.Snapshot {
	if m.filter == "" {
		return m.repos
	}
	needle := strings.ToLower(m.filter)
	out := make([]*gitops.Snapshot, 0, len(m.repos))
	for _, r := range m.repos {
		if strings.Contains(strings.ToLower(r.Name), needle) ||
			strings.Contains(strings.ToLower(r.Branch), needle) {
			out = append(out, r)
		}
	}
	return out
}

func (m *Model) selected() *gitops.Snapshot {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return nil
	}
	return vis[m.cursor]
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	if n == 0 {
		m.cursor, m.offset = 0, 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.offset > m.cursor {
		m.offset = m.cursor
	}
}

func (m *Model) moveCursor(delta int) {
	if delta == 0 {
		return
	}
	m.cursor += delta
	m.clampCursor()
}

// gridStep is how many items a left/right press moves: one in list view, a
// whole row in grid view.
func (m *Model) gridStep() int {
	if m.cfg.View != config.ViewGrid {
		return 1
	}
	return m.gridColumns()
}

// --- view ------------------------------------------------------------------

// View renders the whole interface.
func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}

	header := m.headerView()
	footer := m.footerView()
	bodyHeight := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	var body string
	switch {
	case m.settings != nil:
		body = m.settingsView(bodyHeight)
	case m.help:
		body = m.helpView(bodyHeight)
	default:
		if len(m.repos) == 0 {
			body = m.emptyView(bodyHeight)
		} else if m.cfg.View == config.ViewGrid {
			body = m.gridView(bodyHeight)
		} else {
			body = m.listView(bodyHeight)
		}
	}

	body = fitHeight(body, bodyHeight)
	return clampLines(lipgloss.JoinVertical(lipgloss.Left, header, body, footer), m.width)
}

// fitHeight pads or trims a block so it occupies exactly height lines. This
// keeps the footer pinned to the bottom of the terminal.
func fitHeight(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		return strings.Join(lines[:height], "\n")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// clampLines guarantees that no rendered line reaches the last column of the
// terminal. Writing a full-width line leaves the cursor in the "wrap pending"
// state, which makes some terminals scroll or double-wrap.
func clampLines(s string, width int) string {
	if width <= 1 {
		return s
	}
	maxW := width
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > maxW {
			lines[i] = lipgloss.NewStyle().MaxWidth(maxW).Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) headerView() string {
	title := m.st.title.Render(" repoman ")

	var left string
	if m.filtering {
		left = title + " " + m.st.key.Render("filter: ") + m.filterInput.View()
	} else {
		left = title + " " + m.st.subtitle.Render(m.counts())
		if m.filter != "" {
			left += " " + m.st.key.Render("filter:") + m.st.text.Render(m.filter)
		}
	}

	right := m.st.badge.Render(string(m.cfg.View)) + " " +
		m.st.muted.Render("sorted: "+sortModeNames[m.sortMode])

	// Drop the right-hand badge rather than overflowing the terminal: any
	// line wider than the screen would wrap and break the layout.
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > m.width {
		return truncate(left, m.width)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) counts() string {
	return fmt.Sprintf("%d repos", len(m.repos))
}

func (m *Model) footerView() string {
	var left string
	if m.busy {
		counter := ""
		if m.total > 0 {
			counter = fmt.Sprintf(" %d/%d", m.progress, m.total)
		}
		left = m.spin.View() + " " + m.st.muted.Render(m.busyText+counter)
	} else {
		left = m.statusStyle().Render(m.status)
	}

	// Try progressively shorter key hint sets until one fits next to the
	// status text.
	sets := [][][2]string{
		{
			{"r", "fetch all"}, {"u/U", "update"},
			{"o", "vscode"}, {"i", "ignore"},
			{"tab", "view"}, {",", "settings"}, {"?", "help"}, {"q", "quit"},
		},
		{
			{"r", "fetch"}, {"u/U", "update"}, {"tab", "view"}, {",", "settings"}, {"?", "help"},
		},
		{
			{",", "settings"}, {"?", "help"},
		},
	}

	gap := m.width - lipgloss.Width(left)
	for _, hints := range sets {
		right := m.renderHints(hints)
		if space := gap - lipgloss.Width(right); space >= 1 {
			return left + strings.Repeat(" ", space) + right
		}
	}
	// Nothing fits alongside the status text.
	return truncate(left, m.width)
}

// renderHints formats a key binding row.
func (m *Model) renderHints(hints [][2]string) string {
	var b strings.Builder
	for i, h := range hints {
		if i > 0 {
			b.WriteString(m.st.badge.Render("  "))
		}
		b.WriteString(m.st.key.Render(h[0]) + m.st.hint.Render(" "+h[1]))
	}
	return b.String()
}

func (m *Model) statusStyle() lipgloss.Style {
	switch m.kind {
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

func (m *Model) emptyView(height int) string {
	lines := []string{
		m.st.title.Render("No repositories found"),
		"",
		m.st.muted.Render("Add the directories where you clone repositories:"),
		"",
		m.st.accent.Render("  ,") + m.st.muted.Render("  open settings"),
		"",
	}
	if len(m.cfg.Dirs) > 0 {
		lines = append(lines, m.st.muted.Render("Scanning: "+strings.Join(m.cfg.Dirs, ", ")))
	}
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center,
		lipgloss.JoinVertical(lipgloss.Center, lines...))
}

func (m *Model) helpView(height int) string {
	bindings := [][2]string{
		{"j / ↓", "move down"},
		{"k / ↑", "move up"},
		{"h / l", "move left / right (grid)"},
		{"home / G", "jump to first / last"},
		{"r", "fetch every repository"},
		{"u", "safe-update selected repository"},
		{"U", "safe-update all repositories (skips ignored)"},
		{"i", "exclude / include selected repository from bulk updates"},
		{"o", "open selected repository in VS Code"},
		{"tab", "toggle list / grid view"},
		{"s", "cycle sort order (name / state / sync / recent commit)"},
		{"/", "filter by name or branch"},
		{"backspace", "back / clear filter"},
		{",", "settings"},
		{"?", "toggle this help"},
		{"q", "quit"},
	}
	var lines []string
	lines = append(lines, m.st.overlayTitle.Render("Key bindings"), "")
	for _, b := range bindings {
		lines = append(lines, m.st.key.Render(fmt.Sprintf("%-10s", b[0]))+m.st.text.Render(b[1]))
	}
	lines = append(lines, "", m.st.muted.Render("Safe updates only fast-forward: dirty, detached, diverged or"))
	lines = append(lines, m.st.muted.Render("ahead-of-remote repositories are left untouched."))
	lines = append(lines, "", m.st.muted.Render("State flags: ●N uncommitted changes, ! error, ⊘ excluded,"))
	lines = append(lines, m.st.muted.Render("⇡merged branch merged remotely, ✓ clean."))

	// On short terminals only a window of the bindings fits; scroll it so
	// every line stays reachable.
	if n := len(lines); n > height {
		m.helpOffset = clamp(m.helpOffset, 0, n-height)
		lines = lines[m.helpOffset : m.helpOffset+height]
	} else {
		m.helpOffset = 0
	}

	box := m.st.overlay.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box)
}

// --- small utilities -------------------------------------------------------

func mustHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return path
}

// pluralWord returns the suffix that turns "repositor" into a singular or
// plural noun.
func pluralWord(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}
