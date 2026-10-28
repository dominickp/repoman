package gitops

import (
	"context"
	"sync"
)

// UpdateJob pairs a repository path with the options used to update it.
type UpdateJob struct {
	Path string
	Opts UpdateOptions
}

// UpdateEvent reports progress for one repository in a bulk update. Started is
// true when the update begins; otherwise Result is set and the repository is
// done.
//
// The start event lets the UI animate the repository that is currently being
// worked on rather than only updating a global counter.
type UpdateEvent struct {
	Path    string
	Started bool
	Result  *UpdateResult
}

// StreamUpdates processes jobs sequentially (updates are deliberately not
// parallel: git index locks and merge output are easier to reason about one
// repository at a time) and streams a start event and a result for each job.
// The channel is closed when all jobs are done or ctx is cancelled.
func StreamUpdates(ctx context.Context, jobs []UpdateJob) <-chan UpdateEvent {
	out := make(chan UpdateEvent)
	go func() {
		defer close(out)
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case out <- UpdateEvent{Path: job.Path, Started: true}:
			case <-ctx.Done():
				return
			}
			res := SafeUpdate(ctx, job.Path, job.Opts)
			select {
			case out <- UpdateEvent{Path: job.Path, Result: &res}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// MapInspect inspects paths concurrently and returns snapshots in input order.
func MapInspect(ctx context.Context, paths []string, workers int) []*Snapshot {
	if workers <= 0 {
		workers = 8
	}
	snaps := make([]*Snapshot, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			snaps[i] = Inspect(ctx, p)
		}(i, p)
	}
	wg.Wait()
	return snaps
}
