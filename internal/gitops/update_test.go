package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- test helpers ----------------------------------------------------------

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture builds a bare origin with a `main` branch plus two clones: work and
// peer. Commits pushed from peer are what work has to catch up with.
type fixture struct {
	root     string
	origin   string
	work     string
	peer     string
	workRepo string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		root:   root,
		origin: filepath.Join(root, "origin.git"),
		work:   filepath.Join(root, "work"),
		peer:   filepath.Join(root, "peer"),
	}

	gitT(t, root, "init", "--bare", "--initial-branch=main", f.origin)
	gitT(t, root, "init", "--initial-branch=main", f.work)
	gitT(t, f.work, "remote", "add", "origin", f.origin)
	writeFile(t, f.work, "README.md", "hello\n")
	gitT(t, f.work, "add", ".")
	gitT(t, f.work, "commit", "-m", "initial")
	gitT(t, f.work, "push", "-u", "origin", "main")

	gitT(t, root, "clone", f.origin, f.peer)
	gitT(t, f.peer, "config", "user.name", "test")
	gitT(t, f.peer, "config", "user.email", "test@example.com")

	f.workRepo = f.work
	return f
}

// peerCommit creates a commit on peer's branch and pushes it.
func (f *fixture) peerCommit(t *testing.T, branch, msg string) {
	t.Helper()
	gitT(t, f.peer, "checkout", branch)
	writeFile(t, f.peer, msg+".txt", msg+"\n")
	gitT(t, f.peer, "add", ".")
	gitT(t, f.peer, "commit", "-m", msg)
	gitT(t, f.peer, "push", "origin", branch)
}

// --- tests -----------------------------------------------------------------

func TestInspectDetectsBehind(t *testing.T) {
	f := newFixture(t)
	f.peerCommit(t, "main", "second")

	ctx := context.Background()
	if err := Fetch(ctx, f.workRepo, true); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	snap := Inspect(ctx, f.workRepo)
	if snap.Behind != 1 || snap.Ahead != 0 {
		t.Fatalf("expected 0 ahead / 1 behind, got %d/%d", snap.Ahead, snap.Behind)
	}
	if snap.DefaultBranch != "main" {
		t.Fatalf("expected default branch main, got %q", snap.DefaultBranch)
	}
	if !snap.OnDefaultBranch() {
		t.Fatal("expected to be on the default branch")
	}
}

func TestSafeUpdateFastForwards(t *testing.T) {
	f := newFixture(t)
	f.peerCommit(t, "main", "second")

	ctx := context.Background()
	if err := Fetch(ctx, f.workRepo, true); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeUpdated {
		t.Fatalf("expected updated, got %v (%s)", res.Outcome, res.Reason)
	}
	if got := gitT(t, f.workRepo, "log", "-1", "--format=%s"); trim(got) != "second" {
		t.Fatalf("expected HEAD at second, got %q", got)
	}

	// A second run is a no-op.
	res = SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeUpToDate {
		t.Fatalf("expected up-to-date, got %v (%s)", res.Outcome, res.Reason)
	}
}

// TestSafeUpdateFetchesFirst checks that an update with Fetch set does not
// rely on stale tracking refs.
func TestSafeUpdateFetchesFirst(t *testing.T) {
	f := newFixture(t)
	f.peerCommit(t, "main", "second")

	ctx := context.Background()
	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{Fetch: true, Prune: true})
	if res.Outcome != OutcomeUpdated {
		t.Fatalf("expected updated, got %v (%s)", res.Outcome, res.Reason)
	}
	if got := trim(gitT(t, f.workRepo, "log", "-1", "--format=%s")); got != "second" {
		t.Fatalf("expected HEAD at second, got %q", got)
	}
}

func TestSafeUpdateSkipsDirty(t *testing.T) {
	f := newFixture(t)
	f.peerCommit(t, "main", "second")
	ctx := context.Background()
	_ = Fetch(ctx, f.workRepo, true)

	writeFile(t, f.workRepo, "README.md", "local edit\n")
	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("expected skipped, got %v", res.Outcome)
	}
	if trim(gitT(t, f.workRepo, "log", "-1", "--format=%s")) != "initial" {
		t.Fatal("repository moved despite dirty state")
	}
}

func TestSafeUpdateSkipsAhead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	writeFile(t, f.workRepo, "local.txt", "x\n")
	gitT(t, f.workRepo, "add", ".")
	gitT(t, f.workRepo, "commit", "-m", "local work")

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("expected skipped, got %v (%s)", res.Outcome, res.Reason)
	}
	if !contains(res.Reason, "local commit") {
		t.Fatalf("unexpected reason %q", res.Reason)
	}
}

func TestSafeUpdateSkipsDiverged(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	writeFile(t, f.workRepo, "local.txt", "x\n")
	gitT(t, f.workRepo, "add", ".")
	gitT(t, f.workRepo, "commit", "-m", "local work")
	f.peerCommit(t, "main", "remote work")
	_ = Fetch(ctx, f.workRepo, true)

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped || !contains(res.Reason, "diverged") {
		t.Fatalf("expected diverged skip, got %v (%s)", res.Outcome, res.Reason)
	}
}

func TestSafeUpdateSkipsDetached(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gitT(t, f.workRepo, "checkout", "--detach")

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped || !contains(res.Reason, "detached") {
		t.Fatalf("expected detached skip, got %v (%s)", res.Outcome, res.Reason)
	}
}

func TestSafeUpdateSkipsNoUpstream(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	gitT(t, f.workRepo, "checkout", "-b", "feature/no-upstream")

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped || !contains(res.Reason, "no upstream") {
		t.Fatalf("expected no-upstream skip, got %v (%s)", res.Outcome, res.Reason)
	}
}

// TestSafeUpdateSwitchesAfterRemoteBranchDeleted covers the requirement: when
// the checked-out branch was merged and deleted on the remote, repoman moves
// back to the default branch.
func TestSafeUpdateSwitchesAfterRemoteBranchDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Create a feature branch on both clones and let "peer" merge it to main.
	gitT(t, f.workRepo, "checkout", "-b", "feature")
	writeFile(t, f.workRepo, "feature.txt", "f\n")
	gitT(t, f.workRepo, "add", ".")
	gitT(t, f.workRepo, "commit", "-m", "feature work")
	gitT(t, f.workRepo, "push", "-u", "origin", "feature")

	gitT(t, f.peer, "fetch", "origin")
	gitT(t, f.peer, "checkout", "main")
	gitT(t, f.peer, "merge", "--no-ff", "-m", "merge feature", "origin/feature")
	gitT(t, f.peer, "push", "origin", "main")

	// Delete the merged branch on the remote.
	gitT(t, f.peer, "push", "origin", "--delete", "feature")

	if err := Fetch(ctx, f.workRepo, true); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	snap := Inspect(ctx, f.workRepo)
	if !snap.UpstreamGone {
		t.Fatalf("expected upstream gone, got %+v", snap)
	}

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSwitched {
		t.Fatalf("expected switched, got %v (%s)", res.Outcome, res.Reason)
	}
	if got := trim(gitT(t, f.workRepo, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Fatalf("expected to be on main, got %q", got)
	}
}

// TestSafeUpdateKeepsUnmergedDeletedBranch makes sure we do not switch away
// from a branch whose commits never made it into the default branch.
func TestSafeUpdateKeepsUnmergedDeletedBranch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	gitT(t, f.workRepo, "checkout", "-b", "unmerged")
	writeFile(t, f.workRepo, "wip.txt", "w\n")
	gitT(t, f.workRepo, "add", ".")
	gitT(t, f.workRepo, "commit", "-m", "wip")
	gitT(t, f.workRepo, "push", "-u", "origin", "unmerged")

	gitT(t, f.peer, "push", "origin", "--delete", "unmerged")
	_ = Fetch(ctx, f.workRepo, true)

	res := SafeUpdate(ctx, f.workRepo, UpdateOptions{})
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("expected skip, got %v (%s)", res.Outcome, res.Reason)
	}
	if got := trim(gitT(t, f.workRepo, "rev-parse", "--abbrev-ref", "HEAD")); got != "unmerged" {
		t.Fatalf("expected to stay on unmerged, got %q", got)
	}
}

func TestDiscoverFindsClones(t *testing.T) {
	f := newFixture(t)
	paths, errs := Discover([]string{f.root}, 3)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := map[string]bool{f.work: false, f.peer: false}
	for _, p := range paths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, found := range want {
		if !found {
			t.Fatalf("expected %s in discovered paths %v", p, paths)
		}
	}
}

func TestDiscoverHonoursDepth(t *testing.T) {
	f := newFixture(t)
	deep := filepath.Join(f.root, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, deep, "init", "--initial-branch=main")

	paths, _ := Discover([]string{f.root}, 2)
	for _, p := range paths {
		if p == deep {
			t.Fatal("depth limit was not honoured")
		}
	}
	paths, _ = Discover([]string{f.root}, 5)
	var found bool
	for _, p := range paths {
		if p == deep {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected deep repo at depth 4 to be found, got %v", paths)
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
