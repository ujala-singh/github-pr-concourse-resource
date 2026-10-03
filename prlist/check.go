package prlist

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// DefaultCheckConcurrency bounds how many PRs are inspected in parallel per
// check (path filtering, comment scanning) when source.check_concurrency is
// unset. List mode does one GitHub API call per PR per pass — with many
// open PRs on the repo, running these sequentially can make a single check
// take tens of seconds and eat into the GitHub App's rate limit. This
// trades some of that rate-limit budget for lower per-check latency;
// raising it further mostly just shifts where the bottleneck is (GitHub's
// own per-token concurrency/secondary rate limits) — see
// CommonConfig.CheckConcurrency for the configurable override.
const DefaultCheckConcurrency = 10

// resolveCheckConcurrency returns the effective concurrency bound for a
// check: the configured source.check_concurrency if set (validated to be
// in [1, 50] by CommonConfig.Validate), otherwise DefaultCheckConcurrency.
func resolveCheckConcurrency(source Source) int {
	if source.CheckConcurrency > 0 {
		return source.CheckConcurrency
	}
	return DefaultCheckConcurrency
}

// Check performs the check operation for PR list mode
// Returns a list of versions representing the current set of PRs
func Check(request CheckRequest, github *models.GithubClient) ([]models.Version, error) {
	ctx := context.Background()
	concurrency := resolveCheckConcurrency(request.Source)

	prs, err := github.GetPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull requests: %w", err)
	}

	filteredPRs, err := filterPRsByPath(ctx, github, prs, concurrency, request.Version)
	if err != nil {
		return nil, err
	}

	// Return the current version of every matching PR, every check — not
	// just whatever comes "after" the last known version's PR in this
	// list. An earlier version of this function tried to save bandwidth by
	// only returning entries positioned after the last-known PR's spot in
	// the freshly fetched list (filterNewVersions, since removed). That
	// matched purely on PR NUMBER, so once a PR had been seen once, any
	// later commit pushed to that SAME PR was silently dropped forever —
	// its entry was always found at "the cursor" and skipped, no matter
	// how much its Commit/CommittedDate had changed since. Concourse's ATC
	// already dedups by exact version equality against its own recorded
	// history, so returning the full snapshot is both simpler and
	// correct: an unchanged PR's version is a no-op, while a PR with a new
	// commit (or a brand-new PR) is picked up as new regardless of its
	// position in the list.
	var versions []models.Version
	for _, pr := range filteredPRs {
		versions = append(versions, models.Version{
			PR:                  strconv.Itoa(pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}

	// If nothing currently matches (e.g. no open PRs touch the configured
	// paths), echo back the last known version so the resource doesn't
	// appear to lose its place.
	if len(versions) == 0 && request.Version != nil {
		versions = []models.Version{*request.Version}
	}

	if len(request.Source.TriggerComments) > 0 {
		// filteredPRs is scoped to "did the latest push touch a matching
		// path" (see filterPRsByPath), which is the right question for
		// commit-triggering but the wrong one for comment-triggering: a
		// "concourse plan" comment isn't a push, and a human posting one
		// has no reason to expect it to depend on what the PR's most
		// recent commit happened to touch. Without this, the resource's
		// own cursor PR could fall out of filteredPRs (push touched an
		// unrelated path) and, since nothing else ever advances that
		// cursor, trigger_comments would go permanently dead for that PR
		// — exactly what was reported. So the cursor PR is always
		// eligible for the comment scan, even when path-filtered out of
		// the commit-triggering set.
		commentScopePRs := filteredPRs
		if request.Version != nil {
			alreadyIncluded := false
			for _, pr := range filteredPRs {
				if strconv.Itoa(pr.Number) == request.Version.PR {
					alreadyIncluded = true
					break
				}
			}
			if !alreadyIncluded {
				for _, pr := range prs {
					if strconv.Itoa(pr.Number) == request.Version.PR {
						commentScopePRs = append(append([]*models.PullRequest(nil), filteredPRs...), pr)
						break
					}
				}
			}
		}

		var err error
		versions, err = applyCommentTriggers(ctx, request, github, commentScopePRs, versions, concurrency)
		if err != nil {
			return nil, err
		}
	}

	return versions, nil
}

// runBounded runs fn(i) for i in [0, n) with at most concurrency goroutines
// in flight at once, and waits for all of them to finish. concurrency <= 0
// is treated as 1 (no parallelism).
func runBounded(n int, concurrency int, fn func(i int)) {
	if concurrency <= 0 {
		concurrency = 1
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}

	wg.Wait()
}

// filterPRsByPath applies source.paths/ignore_paths to each PR concurrently
// (bounded by concurrency) and returns the matching PRs in the same order
// GetPullRequests returned them, so downstream cursor-based version diffing
// stays deterministic regardless of which goroutine finishes first.
//
// lastVersion is the resource's last known version (nil on the very first
// check). For the one PR it refers to, its Commit is passed to
// MatchesPathFilters as sinceSHA, scoping the path check to files changed
// since that build rather than the PR's entire history — see
// MatchesPathFilters. Every other PR has no previously-known commit here
// (list mode only remembers this single cursor, not a per-PR history — the
// same limitation documented on applyCommentTriggers), so it falls back to
// the full-PR diff.
func filterPRsByPath(ctx context.Context, github *models.GithubClient, prs []*models.PullRequest, concurrency int, lastVersion *models.Version) ([]*models.PullRequest, error) {
	matches := make([]bool, len(prs))
	errs := make([]error, len(prs))

	runBounded(len(prs), concurrency, func(i int) {
		pr := prs[i]
		var sinceSHA string
		if lastVersion != nil && lastVersion.PR == strconv.Itoa(pr.Number) {
			sinceSHA = lastVersion.Commit
		}
		m, err := github.MatchesPathFilters(ctx, pr, sinceSHA)
		matches[i] = m
		errs[i] = err
	})

	var filtered []*models.PullRequest
	for i, pr := range prs {
		if errs[i] != nil {
			return nil, fmt.Errorf("failed to check path filters for PR #%d: %w", pr.Number, errs[i])
		}
		if matches[i] {
			filtered = append(filtered, pr)
		}
	}
	return filtered, nil
}

// applyCommentTriggers scans every currently path-matching PR for a comment
// matching source.trigger_comments and appends an extra version for any PR
// whose latest matching comment is new since this resource last observed
// that specific PR.
//
// Caveat: unlike single-PR mode, this resource's version stream only
// remembers a single cursor — the one version Concourse hands back as
// request.Version — not a per-PR history. Once the cursor advances past a
// given PR (e.g. because a different PR got a new commit), that PR's
// comment watermark is lost. A later comment on it is then treated as
// establishing a fresh baseline (no trigger) rather than firing
// immediately. This is best-effort: reliable when at most one PR is being
// actively worked on at a time, but can miss (or, rarely, double-fire) a
// trigger under heavy concurrent PR churn across many matching PRs.
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, filteredPRs []*models.PullRequest, versions []models.Version, concurrency int) ([]models.Version, error) {
	type result struct {
		latestMatchID int64
		triggered     bool
		err           error
	}
	results := make([]result, len(filteredPRs))

	// The GitHub calls (one ListComments per PR) run concurrently; the
	// version-mutation pass below stays sequential over filteredPRs in its
	// original order, so the output is deterministic regardless of which
	// goroutine finishes first.
	runBounded(len(filteredPRs), concurrency, func(i int) {
		pr := filteredPRs[i]
		prKey := strconv.Itoa(pr.Number)

		baselineEstablished := request.Version != nil && request.Version.PR == prKey && request.Version.CommentBaseline
		var sinceID int64
		if baselineEstablished {
			sinceID = request.Version.CommentID
		}

		latestMatchID, triggered, err := github.CheckTriggerComments(ctx, pr.Number, request.Source.TriggerComments, sinceID)
		if err != nil {
			results[i] = result{err: fmt.Errorf("failed to check trigger comments for PR #%d: %w", pr.Number, err)}
			return
		}
		if !baselineEstablished {
			triggered = false
		}
		results[i] = result{latestMatchID: latestMatchID, triggered: triggered}
	})

	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
	}

	for i, pr := range filteredPRs {
		prKey := strconv.Itoa(pr.Number)
		r := results[i]

		// Stamp the watermark on any version already in this batch for this
		// PR, so the cursor — if it lands here — carries the up-to-date
		// CommentID forward.
		for j := range versions {
			if versions[j].PR == prKey {
				versions[j].CommentID = r.latestMatchID
				versions[j].CommentBaseline = true
			}
		}

		if r.triggered {
			versions = append(versions, models.Version{
				PR:                  prKey,
				Commit:              pr.HeadRefOID,
				CommittedDate:       pr.CommittedDate,
				ApprovedReviewCount: pr.ApprovedReviewCount,
				CommentID:           r.latestMatchID,
				CommentBaseline:     true,
			})
		}
	}

	return versions, nil
}
