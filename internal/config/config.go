// Package config handles locally managed preferences for repoman.
//
// Preferences live in the user's config directory (typically
// `~/.config/repoman/config.json`) and are editable both from the in-app
// settings screen (`,` in the TUI) and by hand.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ViewMode selects how repositories are laid out.
type ViewMode string

const (
	ViewList ViewMode = "list"
	ViewGrid ViewMode = "grid"
)

// Theme holds the colour palette used by the TUI.
type Theme struct {
	Preset  string `json:"preset"`
	Accent  string `json:"accent"`
	Border  string `json:"border"`
	Muted   string `json:"muted"`
	Text    string `json:"text"`
	Success string `json:"success"`
	Warn    string `json:"warn"`
	Danger  string `json:"danger"`
}

// Config is the persisted preference set.
type Config struct {
	// Dirs are the root directories scanned for local clones.
	Dirs []string `json:"dirs"`
	// Ignored holds absolute repository paths excluded from bulk updates.
	Ignored []string `json:"ignored"`
	// View is the last used layout ("list" or "grid").
	View ViewMode `json:"view"`
	// PruneOnFetch passes `--prune` to `git fetch` so deleted remote
	// branches disappear from tracking refs.
	PruneOnFetch bool `json:"pruneOnFetch"`
	// MaxDepth limits how deep each root directory is scanned.
	MaxDepth int `json:"maxDepth"`
	// Theme is the active colour palette.
	Theme Theme `json:"theme"`
}

// Presets is the ordered list of available themes.
var Presets = []string{"tokyo-night", "dracula", "gruvbox", "nord", "mono"}

var presetThemes = map[string]Theme{
	"tokyo-night": {
		Accent: "#7aa2f7", Border: "#3b4261", Muted: "#565f89", Text: "#c0caf5",
		Success: "#9ece6a", Warn: "#e0af68", Danger: "#f7768e",
	},
	"dracula": {
		Accent: "#bd93f9", Border: "#44475a", Muted: "#6272a4", Text: "#f8f8f2",
		Success: "#50fa7b", Warn: "#ffb86c", Danger: "#ff5555",
	},
	"gruvbox": {
		Accent: "#fabd2f", Border: "#504945", Muted: "#928374", Text: "#ebdbb2",
		Success: "#b8bb26", Warn: "#fe8019", Danger: "#fb4934",
	},
	"nord": {
		Accent: "#88c0d0", Border: "#434c5e", Muted: "#616e88", Text: "#e5e9f0",
		Success: "#a3be8c", Warn: "#ebcb8b", Danger: "#bf616a",
	},
	"mono": {
		Accent: "#ffffff", Border: "#666666", Muted: "#888888", Text: "#dddddd",
		Success: "#ffffff", Warn: "#bbbbbb", Danger: "#ff8888",
	},
}

// ThemeByName returns the named preset, falling back to the first preset.
func ThemeByName(name string) Theme {
	t, ok := presetThemes[name]
	if !ok {
		name = Presets[0]
		t = presetThemes[name]
	}
	t.Preset = name
	return t
}

// NextPreset returns the preset following the given one, wrapping around.
func NextPreset(current string) Theme {
	i := slices.Index(Presets, current)
	if i < 0 {
		i = 0
	}
	return ThemeByName(Presets[(i+1)%len(Presets)])
}

// Default returns the configuration used on first run. Roots are seeded with
// conventional clone locations that actually exist on disk.
func Default() *Config {
	home, err := os.UserHomeDir()
	candidates := []string{"Documents/GitHub", "GitHub", "git", "src", "dev", "code"}
	var dirs []string
	if err == nil {
		for _, c := range candidates {
			p := filepath.Join(home, c)
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				dirs = append(dirs, p)
			}
		}
	}
	return &Config{
		Dirs:         dirs,
		View:         ViewList,
		PruneOnFetch: true,
		MaxDepth:     1,
		Theme:        ThemeByName("tokyo-night"),
	}
}

// Dir returns the directory holding repoman's configuration.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "repoman"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "repoman"), nil
}

// Path returns the full path to the configuration file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the configuration from the default location, creating defaults
// when the file is absent. It returns the config, the path it was loaded from,
// and any error.
func Load() (*Config, string, error) {
	path, err := Path()
	if err != nil {
		return Default(), "", err
	}
	cfg, err := LoadFile(path)
	return cfg, path, err
}

// LoadFile reads the configuration from an explicit path.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}

	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return Default(), fmt.Errorf("parsing %s: %w", path, err)
	}
	cfg.Normalize()
	return cfg, nil
}

// Normalize repairs out-of-range or missing values.
func (c *Config) Normalize() {
	if c.View != ViewGrid {
		c.View = ViewList
	}
	if c.MaxDepth <= 0 || c.MaxDepth > 12 {
		c.MaxDepth = 1
	}
	if c.Theme.Preset == "" {
		c.Theme = ThemeByName(c.Theme.Preset)
	}
	// Keep any custom colours, but fall back for empty fields.
	base := ThemeByName(c.Theme.Preset)
	colors := map[string]*string{
		"accent":  &c.Theme.Accent,
		"border":  &c.Theme.Border,
		"muted":   &c.Theme.Muted,
		"text":    &c.Theme.Text,
		"success": &c.Theme.Success,
		"warn":    &c.Theme.Warn,
		"danger":  &c.Theme.Danger,
	}
	fallbacks := map[string]string{
		"accent": base.Accent, "border": base.Border, "muted": base.Muted,
		"text": base.Text, "success": base.Success, "warn": base.Warn,
		"danger": base.Danger,
	}
	for k, v := range colors {
		if strings.TrimSpace(*v) == "" {
			*v = fallbacks[k]
		}
	}
	c.Theme.Preset = base.Preset

	dirs := c.Dirs[:0]
	for _, d := range c.Dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		dirs = append(dirs, ExpandPath(d))
	}
	c.Dirs = dirs

	ign := c.Ignored[:0]
	for _, p := range c.Ignored {
		if strings.TrimSpace(p) == "" {
			continue
		}
		ign = append(ign, filepath.Clean(ExpandPath(p)))
	}
	c.Ignored = ign
}

// Save writes the configuration atomically.
func (c *Config) Save(path string) error {
	if path == "" {
		var err error
		if path, err = Path(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	c.Normalize()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ExpandPath expands a leading `~` and cleans the result.
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return filepath.Clean(p)
}

// IsIgnored reports whether the repository at path is excluded from bulk updates.
func (c *Config) IsIgnored(path string) bool {
	p := filepath.Clean(path)
	return slices.Contains(c.Ignored, p)
}

// SetIgnored adds or removes path from the ignore list.
func (c *Config) SetIgnored(path string, ignored bool) {
	p := filepath.Clean(path)
	i := slices.Index(c.Ignored, p)
	switch {
	case ignored && i < 0:
		c.Ignored = append(c.Ignored, p)
	case !ignored && i >= 0:
		c.Ignored = slices.Delete(c.Ignored, i, i+1)
	}
}

// ToggleIgnored flips the ignore state for path and returns the new value.
func (c *Config) ToggleIgnored(path string) bool {
	next := !c.IsIgnored(path)
	c.SetIgnored(path, next)
	return next
}

// AddDir appends a directory root if it is not already present.
func (c *Config) AddDir(dir string) bool {
	d := ExpandPath(dir)
	if slices.Contains(c.Dirs, d) {
		return false
	}
	c.Dirs = append(c.Dirs, d)
	return true
}

// RemoveDir deletes a directory root. It reports whether anything was removed.
func (c *Config) RemoveDir(dir string) bool {
	i := slices.Index(c.Dirs, dir)
	if i < 0 {
		return false
	}
	c.Dirs = slices.Delete(c.Dirs, i, i+1)
	return true
}

// CycleTheme advances to the next preset and returns its name.
func (c *Config) CycleTheme() string {
	c.Theme = NextPreset(c.Theme.Preset)
	return c.Theme.Preset
}
