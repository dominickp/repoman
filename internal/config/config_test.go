package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := Default()
	cfg.Dirs = []string{"/tmp/one", "/tmp/two"}
	cfg.View = ViewGrid
	cfg.PruneOnFetch = false
	cfg.MaxDepth = 7
	cfg.Theme = ThemeByName("gruvbox")
	cfg.SetIgnored("/tmp/one/repo", true)

	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Dirs) != 2 || got.Dirs[1] != "/tmp/two" {
		t.Fatalf("dirs not preserved: %v", got.Dirs)
	}
	if got.View != ViewGrid {
		t.Fatalf("view not preserved: %v", got.View)
	}
	if got.PruneOnFetch {
		t.Fatal("pruneOnFetch not preserved")
	}
	if got.MaxDepth != 7 {
		t.Fatalf("maxDepth not preserved: %d", got.MaxDepth)
	}
	if got.Theme.Preset != "gruvbox" || got.Theme.Accent != ThemeByName("gruvbox").Accent {
		t.Fatalf("theme not preserved: %+v", got.Theme)
	}
	if !got.IsIgnored("/tmp/one/repo") {
		t.Fatal("ignored repository not preserved")
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.View != ViewList {
		t.Fatalf("expected default list view, got %v", cfg.View)
	}
	if cfg.MaxDepth != 1 {
		t.Fatalf("expected default depth 1, got %d", cfg.MaxDepth)
	}
	if cfg.Theme.Accent == "" {
		t.Fatal("expected a populated theme")
	}
}

func TestNormalizeRepairsBadValues(t *testing.T) {
	cfg := &Config{
		Dirs:     []string{"", "  ", "/tmp/ok"},
		Ignored:  []string{"", "/tmp/x"},
		View:     "nonsense",
		MaxDepth: 99,
		Theme:    Theme{Preset: "not-a-preset"},
	}
	cfg.Normalize()

	if len(cfg.Dirs) != 1 || cfg.Dirs[0] != "/tmp/ok" {
		t.Fatalf("dirs not cleaned: %v", cfg.Dirs)
	}
	if len(cfg.Ignored) != 1 || cfg.Ignored[0] != "/tmp/x" {
		t.Fatalf("ignored not cleaned: %v", cfg.Ignored)
	}
	if cfg.View != ViewList {
		t.Fatalf("expected list view fallback, got %v", cfg.View)
	}
	if cfg.MaxDepth != 1 {
		t.Fatalf("expected depth fallback, got %d", cfg.MaxDepth)
	}
	if cfg.Theme.Preset != Presets[0] || cfg.Theme.Accent == "" {
		t.Fatalf("theme not repaired: %+v", cfg.Theme)
	}
}

func TestIgnoredToggle(t *testing.T) {
	cfg := Default()
	path := "/tmp/repo"

	if cfg.IsIgnored(path) {
		t.Fatal("path should not start ignored")
	}
	if !cfg.ToggleIgnored(path) {
		t.Fatal("toggle should report the new ignored state")
	}
	if !cfg.IsIgnored(path) {
		t.Fatal("path should be ignored")
	}
	if cfg.ToggleIgnored(path) {
		t.Fatal("second toggle should report not ignored")
	}
	if cfg.IsIgnored(path) {
		t.Fatal("path should no longer be ignored")
	}
}

func TestAddRemoveDir(t *testing.T) {
	cfg := Default()
	cfg.Dirs = nil

	if !cfg.AddDir("/tmp/a") {
		t.Fatal("expected the directory to be added")
	}
	if cfg.AddDir("/tmp/a") {
		t.Fatal("duplicate directory should not be added")
	}
	if !cfg.RemoveDir("/tmp/a") {
		t.Fatal("expected the directory to be removed")
	}
	if cfg.RemoveDir("/tmp/a") {
		t.Fatal("removing a missing directory should report false")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := ExpandPath("~/repos"), filepath.Join(home, "repos"); got != want {
		t.Fatalf("ExpandPath(~/repos) = %q, want %q", got, want)
	}
	if got := ExpandPath("relative/dir"); !filepath.IsAbs(got) {
		t.Fatalf("expected an absolute path, got %q", got)
	}
}

func TestNextPresetWraps(t *testing.T) {
	seen := map[string]bool{}
	name := Presets[0]
	for range Presets {
		if seen[name] {
			t.Fatalf("cycle repeated %q too early", name)
		}
		seen[name] = true
		name = NextPreset(name).Preset
	}
	if name != Presets[0] {
		t.Fatalf("expected the cycle to wrap to %q, got %q", Presets[0], name)
	}
}
