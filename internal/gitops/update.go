package gitops

import (
	"context"
	"fmt"
	"strings"
)

// Outcome classifies the result of a safe update attempt.
type Outcome int

const (
	// OutcomeUpdated means the branch was fast-forwarded.
	OutcomeUpdated Outcome = iota
	// OutcomeUpToDate means there was nothing to pull.
	OutcomeUpToDate
	// OutcomeSwitched means the branch had been merged and deleted remotely,
	// so the repository was moved back to its default branch.
	OutcomeSwitched
	// OutcomeSkipped means the update was deliberately not attempted.
	OutcomeSkipped
	// OutcomeFailed means a git command failed.
	OutcomeFailed
)

// String renders a short label for the outcome.
func (o Outcome) String() string {
	switch o {
	case OutcomeUpdated:
		return "updated"
	case OutcomeUpToDate:
		return "up-to-date"
	case OutcomeSwitched:
		return "switched"
	case OutcomeSkipped:
		return "skipped"
	case OutcomeFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// UpdateResult describes what happened to one repository.
type UpdateResult struct {
	Path    string
	Name    string
	Outcome Outcome
	// Reason explains a skip or a failure.
	Reason string
	// Branch is the branch the repository ended up on.
	Branch  string
	Commits int
}

// Message renders a one-line human readable summary.
func (r UpdateResult) Message() string {
	switch r.Outcome {
	case OutcomeUpdated:
		return fmt.Sprintf("%s: fast-forwarded %s (%d commit%s)", r.Name, r.Branch, r.Commits, plural(r.Commits))
	case OutcomeSwitched:
		return fmt.Sprintf("%s: branch merged remotely, switched to %s", r.Name, r.Branch)
	case OutcomeUpToDate:
		return fmt.Sprintf("%s: already up to date", r.Name)
	case OutcomeSkipped:
		return fmt.Sprintf("%s: skipped (%s)", r.Name, r.Reason)
	case OutcomeFailed:
		return fmt.Sprintf("%s: failed (%s)", r.Name, r.Reason)
	default:
		return r.Name
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// UpdateOptions tunes the safety rules of SafeUpdate.
type UpdateOptions struct {
	// AllowDirty permits updating repositories with uncommitted changes.
	// Fast-forwards ignore local modifications that don't conflict, but the
	// default is to refuse so nothing surprising happens.
	AllowDirty bool
	// Fetch fetches the repository first so the update reflects what is
	// actually on the remote rather than the last known state.
	Fetch bool
	// Prune passes `--prune` to the fetch, so branches deleted remotely stop
	// being tracked (which is what triggers the switch-back-to-default rule).
	Prune bool
}

// SafeUpdate brings one repository up to date without risking local work.
//
// Rules, in order:
//   - detached HEAD: skip
//   - dirty working tree (modified or untracked files): skip
//   - no upstream branch: skip
//   - diverged (ahead and behind): skip
//   - local commits ahead: skip
//   - upstream deleted remotely, but merged into the default branch: switch
//     back to the default branch and fast-forward it
//   - otherwise: fast-forward to the upstream tracking branch
func SafeUpdate(ctx context.Context, path string, opts UpdateOptions) UpdateResult {
	if opts.Fetch {
		if err := Fetch(ctx, path, opts.Prune); err != nil {
			snap := Inspect(ctx, path)
			return UpdateResult{
				Path: path, Name: snap.Name, Branch: snap.Branch,
				Outcome: OutcomeFailed, Reason: "fetch: " + cleanGitError(err.Error()),
			}
		}
	}

	snap := Inspect(ctx, path)
	res := UpdateResult{Path: path, Name: snap.Name, Branch: snap.Branch}

	fail := func(format string, args ...any) UpdateResult {
		res.Outcome = OutcomeFailed
		res.Reason = fmt.Sprintf(format, args...)
		return res
	}
	skip := func(reason string) UpdateResult {
		res.Outcome = OutcomeSkipped
		res.Reason = reason
		return res
	}

	if snap.Err != "" {
		return fail("%s", snap.Err)
	}
	if snap.Detached {
		return skip("detached HEAD")
	}
	if snap.Dirty && !opts.AllowDirty {
		return skip(fmt.Sprintf("%d uncommitted change%s", snap.DirtyCount, plural(snap.DirtyCount)))
	}

	if snap.UpstreamGone {
		return switchToDefault(ctx, path, snap, res)
	}
	if !snap.HasUpstream {
		return skip("no upstream branch")
	}
	if snap.Ahead > 0 && snap.Behind > 0 {
		return skip(fmt.Sprintf("diverged (%d ahead, %d behind)", snap.Ahead, snap.Behind))
	}
	if snap.Ahead > 0 {
		return skip(fmt.Sprintf("%d local commit%s", snap.Ahead, plural(snap.Ahead)))
	}
	if snap.Behind == 0 {
		res.Outcome = OutcomeUpToDate
		return res
	}

	if err := fastForward(ctx, path, "@{u}"); err != nil {
		return fail("%s", err)
	}
	res.Outcome = OutcomeUpdated
	res.Commits = snap.Behind
	return res
}

// switchToDefault handles the "branch was merged and deleted on the remote"
// case by moving back to the default branch once it is safe to do so.
func switchToDefault(ctx context.Context, path string, snap *Snapshot, res UpdateResult) UpdateResult {
	def := snap.DefaultBranch
	if def == "" || snap.Remote == "" {
		res.Outcome = OutcomeSkipped
		res.Reason = "upstream deleted remotely"
		return res
	}

	remoteRef := "refs/remotes/" + snap.Remote + "/" + def
	if !ok(ctx, path, "show-ref", "--verify", "--quiet", remoteRef) {
		res.Outcome = OutcomeSkipped
		res.Reason = "upstream deleted remotely and " + def + " is not available"
		return res
	}
	// Only switch when the current branch's work is already contained in the
	// default branch; otherwise the commits would be lost.
	if !ok(ctx, path, "merge-base", "--is-ancestor", "HEAD", remoteRef) {
		res.Outcome = OutcomeSkipped
		res.Reason = "upstream deleted remotely and branch is not merged into " + def
		return res
	}

	if ok(ctx, path, "show-ref", "--verify", "--quiet", "refs/heads/"+def) {
		if _, err := run(ctx, path, "switch", def); err != nil {
			res.Outcome = OutcomeFailed
			res.Reason = err.Error()
			return res
		}
	} else {
		if _, err := run(ctx, path, "switch", "--create", def, "--track", snap.Remote+"/"+def); err != nil {
			res.Outcome = OutcomeFailed
			res.Reason = err.Error()
			return res
		}
	}

	if err := fastForward(ctx, path, snap.Remote+"/"+def); err != nil {
		// The switch itself succeeded; report the branch move but note the
		// failed fast-forward.
		res.Outcome = OutcomeSwitched
		res.Branch = def
		res.Reason = err.Error()
		return res
	}

	res.Outcome = OutcomeSwitched
	res.Branch = def
	return res
}

// fastForward merges target into HEAD, refusing anything but a fast-forward.
func fastForward(ctx context.Context, path, target string) error {
	if _, err := run(ctx, path, "merge", "--ff-only", "--quiet", target); err != nil {
		return fmt.Errorf("%s", cleanGitError(err.Error()))
	}
	return nil
}

// cleanGitError trims git's noisy prefixes from error messages.
func cleanGitError(msg string) string {
	msg = strings.TrimPrefix(msg, "error: ")
	msg = strings.TrimPrefix(msg, "fatal: ")
	return firstLine(msg)
}
