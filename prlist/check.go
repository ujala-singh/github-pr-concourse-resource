package prlist

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// checkConcurrency bounds how many PRs are inspected in parallel per check
// (path filtering, comment scanning). List mode does one GitHub API call
// per PR per pass — with many open PRs on the repo, running these
// sequentially can make a single check take tens of seconds and eat into
// the GitHub App's rate limit. This trades some of that rate-limit budget
// for lower per-check latency; it's a constant rather than a source field
// since raising it further mostly just shifts where the bottleneck is
// (GitHub's own per-token concurrency/secondary rate limits).
const checkConcurrency = 8

// Check performs the check operation for PR list mode
// Returns a list of versions representing the current set of PRs
func Check(request CheckRequest, github *models.GithubClient) ([]models.Version, error) {
	ctx := context.Background()

	prs, err := github.GetPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull requests: %w", err)
	}

	filteredPRs, err := filterPRsByPath(ctx, github, prs)
	if err != nil {
		return nil, err
	}

	// Convert to versions
	var versions []models.Version
	for _, pr := range filteredPRs {
		versions = append(versions, models.Version{
			PR:                  strconv.Itoa(pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}

	// If we have a previous version, only return new versions
	if request.Version != nil {
		versions = filterNewVersions(versions, *request.Version)
	}

	// If no new versions, return the current version to indicate no changes
	if len(versions) == 0 && request.Version != nil {
		versions = []models.Version{*request.Version}
	}

	if len(request.Source.TriggerComments) > 0 {
		var err error
		versions, err = applyCommentTriggers(ctx, request, github, filteredPRs, versions)
		if err != nil {
			return nil, err
		}
	}

	return versions, nil
}

// runBounded runs fn(i) for i in [0, n) with at most checkConcurrency
// goroutines in flight at once, and waits for all of them to finish.
func runBounded(n int, fn func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, checkConcurrency)

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
// (bounded by checkConcurrency) and returns the matching PRs in the same
// order GetPullRequests returned them, so downstream cursor-based version
// diffing stays deterministic regardless of which goroutine finishes first.
func filterPRsByPath(ctx context.Context, github *models.GithubClient, prs []*models.PullRequest) ([]*models.PullRequest, error) {
	matches := make([]bool, len(prs))
	errs := make([]error, len(prs))

	runBounded(len(prs), func(i int) {
		m, err := github.MatchesPathFilters(ctx, prs[i])
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
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, filteredPRs []*models.PullRequest, versions []models.Version) ([]models.Version, error) {
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
	runBounded(len(filteredPRs), func(i int) {
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

// filterNewVersions returns only the versions that are newer than the given version
func filterNewVersions(versions []models.Version, lastVersion models.Version) []models.Version {
	var newVersions []models.Version
	foundLast := false

	for _, v := range versions {
		if v.PR == lastVersion.PR {
			foundLast = true
			continue
		}
		if foundLast {
			newVersions = append(newVersions, v)
		}
	}

	// If we didn't find the last version in the list, return all versions
	if !foundLast {
		return versions
	}

	return newVersions
}
