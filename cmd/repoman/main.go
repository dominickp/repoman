// Command repoman is a terminal UI for managing locally cloned git
// repositories: it lists them, shows their sync state, and fetches or
// safe-updates them one at a time or in bulk.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dominickp/repoman/internal/config"
	"github.com/dominickp/repoman/internal/gitops"
	"github.com/dominickp/repoman/internal/tui"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)"
var version = "dev"

func main() {
	var (
		cfgPath  = flag.String("config", "", "path to config.json (default: XDG config dir)")
		listOnly = flag.Bool("list", false, "print repositories as a table and exit")
		addDir   = flag.String("add-dir", "", "add a scan directory to the config and exit")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("repoman", version)
		return
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintln(os.Stderr, "repoman requires git on PATH:", err)
		os.Exit(1)
	}

	var (
		cfg  *config.Config
		path string
		err  error
	)
	if *cfgPath != "" {
		path = config.ExpandPath(*cfgPath)
		cfg, err = config.LoadFile(path)
	} else {
		cfg, path, err = config.Load()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err)
	}

	if *addDir != "" {
		dir := config.ExpandPath(*addDir)
		if !cfg.AddDir(dir) {
			fmt.Printf("already scanning %s\n", dir)
			return
		}
		if err := cfg.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, "saving config:", err)
			os.Exit(1)
		}
		fmt.Printf("added scan directory %s\n", dir)
		return
	}

	if *listOnly {
		printTable(cfg)
		return
	}

	if err := tui.Run(cfg, path); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// printTable writes a plain-text overview, useful for scripting and for
// verifying behaviour without a TTY.
func printTable(cfg *config.Config) {
	paths, errs := gitops.Discover(cfg.Dirs, cfg.MaxDepth)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	if len(paths) == 0 {
		fmt.Println("no repositories found; add a directory with -add-dir")
		return
	}

	snaps := gitops.MapInspect(context.Background(), paths, 8)
	fmt.Printf("%-28s %-22s %-12s %s\n", "REPOSITORY", "BRANCH", "SYNC", "STATE")
	for _, s := range snaps {
		sync := "-"
		switch {
		case s.Detached:
			sync = "detached"
		case s.UpstreamGone:
			sync = "deleted"
		case s.HasUpstream:
			sync = fmt.Sprintf("+%d -%d", s.Ahead, s.Behind)
		}
		var state []string
		if s.Dirty {
			state = append(state, fmt.Sprintf("%d changed", s.DirtyCount))
		}
		if cfg.IsIgnored(s.Path) {
			state = append(state, "excluded")
		}
		if s.OnDefaultBranch() {
			state = append(state, "default")
		}
		if s.Err != "" {
			state = append(state, "error: "+s.Err)
		}
		fmt.Printf("%-28s %-22s %-12s %s\n",
			truncatePlain(s.Name, 28),
			truncatePlain(s.Branch, 22),
			sync,
			strings.Join(state, ", "),
		)
	}
}

func truncatePlain(s string, w int) string {
	if len(s) <= w {
		return s
	}
	if w <= 1 {
		return s[:w]
	}
	return s[:w-1] + "…"
}
