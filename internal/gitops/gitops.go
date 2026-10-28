// Package gitops wraps the git command line for the operations repoman needs:
// discovering local clones, inspecting their state, fetching and performing
// safe fast-forward updates.
//
// Shelling out to git (rather than embedding a library) keeps behaviour
// identical to what the user would get on the command line, including
// worktrees, submodules, hooks and credential helpers.
package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// commandTimeout bounds a single git invocation.
const commandTimeout = 60 * time.Second

// Snapshot is a point-in-time view of a repository's state.
type Snapshot struct {
	Path          string    `json:"path"`
	Name          string    `json:"name"`
	Branch        string    `json:"branch"`
	Detached      bool      `json:"detached"`
	SHA           string    `json:"sha"`
	DefaultBranch string    `json:"defaultBranch"`
	Remote        string    `json:"remote"`
	Upstream      string    `json:"upstream"`
	HasUpstream   bool      `json:"hasUpstream"`
	UpstreamGone  bool      `json:"upstreamGone"`
	Ahead         int       `json:"ahead"`
	Behind        int       `json:"behind"`
	Dirty         bool      `json:"dirty"`
	DirtyCount    int       `json:"dirtyCount"`
	LastCommit    string    `json:"lastCommit"`
	LastCommitAt  time.Time `json:"lastCommitAt"`
	Err           string    `json:"err,omitempty"`
}

// OnDefaultBranch reports whether HEAD is the repository's default branch.
func (s *Snapshot) OnDefaultBranch() bool {
	return !s.Detached && s.DefaultBranch != "" && s.Branch == s.DefaultBranch
}

// run executes git in dir and returns trimmed stdout. Errors include stderr.
func run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Never prompt: a background fetch shouldn't hang on credentials.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		return out, runError(ctx, err, args, out, stderr.String())
	}
	return out, nil
}

// runError turns a failed git invocation into a concise error message.
func runError(ctx context.Context, err error, args []string, out, stderr string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("git %s: timed out", strings.Join(args, " "))
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = out
	}
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(firstLine(msg))
}

// ok runs git and reports whether it exited successfully.
func ok(ctx context.Context, dir string, args ...string) bool {
	_, err := run(ctx, dir, args...)
	return err == nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// IsRepo reports whether dir is the root of a git working tree.
func IsRepo(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}

// Discover walks each root and returns the paths of every git working tree
// found, limited to maxDepth levels below each root. Duplicates and nested
// repositories inside an already-found repository are returned as-is; callers
// can de-duplicate if needed.
func Discover(roots []string, maxDepth int) ([]string, []error) {
	if maxDepth <= 0 {
		maxDepth = 1
	}
	var (
		mu    sync.Mutex
		found []string
		errs  []error
		wg    sync.WaitGroup
	)

	for _, root := range roots {
		wg.Add(1)
		go func(root string) {
			defer wg.Done()
			paths, err := discoverOne(root, maxDepth)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			found = append(found, paths...)
		}(root)
	}
	wg.Wait()

	seen := make(map[string]bool, len(found))
	unique := found[:0]
	for _, p := range found {
		if seen[p] {
			continue
		}
		seen[p] = true
		unique = append(unique, p)
	}
	return unique, errs
}

func discoverOne(root string, maxDepth int) ([]string, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("%s: %w", root, err)
	}

	var paths []string
	rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory: skip it rather than aborting the scan.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
		if depth > maxDepth {
			return fs.SkipDir
		}
		name := d.Name()
		if path != root && (name == "node_modules" || name == "vendor" || name == ".cache") {
			return fs.SkipDir
		}
		if IsRepo(path) {
			paths = append(paths, path)
			// Do not descend into nested clones / submodules.
			return fs.SkipDir
		}
		return nil
	})
	return paths, err
}

// Inspect gathers the state of the repository at path.
func Inspect(ctx context.Context, path string) *Snapshot {
	snap := &Snapshot{
		Path: path,
		Name: filepath.Base(path),
	}

	branch, err := run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		snap.Err = err.Error()
		return snap
	}
	if branch == "HEAD" {
		snap.Detached = true
		snap.Branch = "(detached)"
		if sha, err := run(ctx, path, "rev-parse", "--short", "HEAD"); err == nil {
			snap.SHA = sha
		}
	} else {
		snap.Branch = branch
	}

	snap.Remote = remoteFor(ctx, path, snap.Branch)
	snap.DefaultBranch = defaultBranch(ctx, path, snap.Remote)
	if snap.DefaultBranch == "" && !snap.Detached {
		snap.DefaultBranch = snap.Branch
	}

	if !snap.Detached {
		inspectUpstream(ctx, path, snap)
	}

	if status, err := run(ctx, path, "status", "--porcelain"); err == nil {
		lines := strings.Split(status, "\n")
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				snap.DirtyCount++
			}
		}
		snap.Dirty = snap.DirtyCount > 0
	}

	// Note: exec rejects NUL bytes in arguments, so the format string uses a
	// tab as separator instead.
	if meta, err := run(ctx, path, "log", "-1", "--format=%ct\t%s"); err == nil {
		if parts := strings.SplitN(meta, "\t", 2); len(parts) == 2 {
			if secs, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				snap.LastCommitAt = time.Unix(secs, 0)
			}
			snap.LastCommit = parts[1]
		}
	}

	return snap
}

// inspectUpstream fills in the upstream tracking details for the snapshot.
func inspectUpstream(ctx context.Context, path string, snap *Snapshot) {
	up, err := run(ctx, path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err == nil && up != "" && up != "@{u}" {
		snap.Upstream = up
		snap.HasUpstream = true
		counts, err := run(ctx, path, "rev-list", "--left-right", "--count", "@{u}...HEAD")
		if err != nil {
			return
		}
		fields := strings.Fields(counts)
		if len(fields) != 2 {
			return
		}
		if n, err := strconv.Atoi(fields[0]); err == nil {
			snap.Behind = n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			snap.Ahead = n
		}
		return
	}

	remote := snap.Remote
	if remote == "" {
		return
	}
	// A configured upstream whose remote ref disappeared shows up as
	// "[gone]" in the branch's tracking info.
	track, err := run(ctx, path, "for-each-ref", "--format=%(upstream:track)", "refs/heads/"+snap.Branch)
	if err == nil && strings.Contains(track, "gone") {
		snap.UpstreamGone = true
		snap.Upstream = remote + "/" + snap.Branch
	}
}

// remoteFor returns the remote tracking the given branch, defaulting to origin.
func remoteFor(ctx context.Context, path, branch string) string {
	if branch != "" && branch != "(detached)" {
		if r, err := run(ctx, path, "config", "--get", "branch."+branch+".remote"); err == nil && r != "" && r != "." {
			return r
		}
	}
	if ok(ctx, path, "remote", "get-url", "origin") {
		return "origin"
	}
	if remotes, err := run(ctx, path, "remote"); err == nil && remotes != "" {
		return strings.Fields(remotes)[0]
	}
	return ""
}

// defaultBranch resolves the repository's default branch, preferring the
// remote HEAD pointer and falling back to conventional names.
func defaultBranch(ctx context.Context, path, remote string) string {
	if remote != "" {
		ref := "refs/remotes/" + remote + "/HEAD"
		if out, err := run(ctx, path, "symbolic-ref", "--quiet", "--short", ref); err == nil && out != "" {
			return strings.TrimPrefix(out, remote+"/")
		}
		for _, candidate := range []string{"main", "master", "trunk", "develop"} {
			if ok(ctx, path, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+candidate) {
				return candidate
			}
		}
	}
	for _, candidate := range []string{"main", "master", "trunk"} {
		if ok(ctx, path, "show-ref", "--verify", "--quiet", "refs/heads/"+candidate) {
			return candidate
		}
	}
	if head, err := run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && head != "HEAD" {
		return head
	}
	return ""
}

// Fetch pulls down remote refs for every remote of the repository.
func Fetch(ctx context.Context, path string, prune bool) error {
	args := []string{"fetch", "--all", "--quiet"}
	if prune {
		args = append(args, "--prune")
	}
	_, err := run(ctx, path, args...)
	return err
}

// FetchEvent reports progress of a bulk fetch for one repository. Started is
// true when the fetch begins; when false the fetch has finished and Err holds
// the outcome.
//
// Emitting a start event lets the UI animate each repository individually
// instead of showing a single global spinner.
//
// Fetching only updates remote-tracking refs (`refs/remotes/*`): it never
// moves local branches or touches the working tree, so it is safe to run over
// every repository at once.
type FetchEvent struct {
	Path    string
	Started bool
	Err     error
}

// FetchMany runs Fetch concurrently over paths using at most workers
// goroutines, streaming a start event and a completion event for each
// repository to the returned channel.
func FetchMany(ctx context.Context, paths []string, prune bool, workers int) <-chan FetchEvent {
	if workers <= 0 {
		workers = 4
	}
	out := make(chan FetchEvent)
	go func() {
		defer close(out)
		jobs := make(chan string)
		var wg sync.WaitGroup
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range jobs {
					select {
					case out <- FetchEvent{Path: p, Started: true}:
					case <-ctx.Done():
						return
					}
					err := Fetch(ctx, p, prune)
					select {
					case out <- FetchEvent{Path: p, Err: err}:
					case <-ctx.Done():
						return
					}
				}
			}()
		}
		for _, p := range paths {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return
			case jobs <- p:
			}
		}
		close(jobs)
		wg.Wait()
	}()
	return out
}
