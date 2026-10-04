package prlist

import (
	"context"
	"fmt"
	"maps"
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
		commentScopePRs := commentTriggerScope(filteredPRs, prs, request.Version)
		var err error
		versions, err = applyCommentTriggers(ctx, request, github, commentScopePRs, prs, versions, concurrency)
		if err != nil {
			return nil, err
		}
	}

	return versions, nil
}

// commentTriggerScope returns every PR that should be scanned for a
// trigger-comment match this check: every path-matching PR (filteredPRs,
// which is scoped to "did the latest push touch a matching path" — the
// right question for commit-triggering, but the wrong one for
// comment-triggering, since a comment isn't a push), plus every PR this
// resource has an established comment-trigger watermark for (see
// commentWatermarks) that's still open. Without the second part, a PR
// would permanently lose comment-trigger eligibility the moment an
// unrelated push stopped it from path-matching, or the moment Concourse's
// single version cursor moved on to a different PR entirely — both are
// real incidents this was built to fix, not hypothetical.
func commentTriggerScope(filteredPRs, allPRs []*models.PullRequest, lastVersion *models.Version) []*models.PullRequest {
	tracked := commentWatermarks(lastVersion)
	if len(tracked) == 0 {
		return filteredPRs
	}

	scope := append([]*models.PullRequest(nil), filteredPRs...)
	seen := make(map[int]bool, len(scope))
	for _, pr := range scope {
		seen[pr.Number] = true
	}

	for _, pr := range allPRs {
		if seen[pr.Number] {
			continue
		}
		if _, ok := tracked[strconv.Itoa(pr.Number)]; ok {
			scope = append(scope, pr)
			seen[pr.Number] = true
		}
	}
	return scope
}

// commentWatermarks returns lastVersion's per-PR comment-trigger watermark
// table (PR number -> highest matching comment ID seen for it so far),
// falling back to the legacy single-PR CommentID/CommentBaseline fields
// when CommentWatermarks is empty — e.g. right after upgrading from a
// version of this resource that predates per-PR tracking, so the one PR
// that was the reliable cursor carries its watermark forward into the new
// table instead of silently resetting and re-establishing a baseline.
func commentWatermarks(lastVersion *models.Version) map[string]int64 {
	if lastVersion == nil {
		return nil
	}
	if len(lastVersion.CommentWatermarks) > 0 {
		return lastVersion.CommentWatermarks
	}
	if lastVersion.CommentBaseline {
		return map[string]int64{lastVersion.PR: lastVersion.CommentID}
	}
	return nil
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

// applyCommentTriggers scans every PR in scopePRs for a comment matching
// source.trigger_comments and appends an extra version for any PR whose
// latest matching comment is new since this resource last observed that
// specific PR.
//
// Per-PR watermarks (models.Version.CommentWatermarks) replace what used
// to be a single cursor-wide watermark: Concourse's check protocol only
// ever hands back the one version it considers "latest," not a per-PR
// history, so earlier versions of this function could only reliably track
// whichever single PR that version happened to be about. By folding every
// tracked PR's watermark into one map and stamping the complete, current
// map onto every version this function returns, the table survives
// regardless of which PR Concourse remembers as "latest" next — removing
// that limitation. allPRs (every currently open, Github-API-matching PR,
// not just path-filtered ones) is used only to prune entries for PRs that
// have closed/merged/dropped out, bounding the map to roughly the current
// open-PR count rather than growing forever.
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, scopePRs []*models.PullRequest, allPRs []*models.PullRequest, versions []models.Version, concurrency int) ([]models.Version, error) {
	tracked := commentWatermarks(request.Version)

	type result struct {
		latestMatchID int64
		triggered     bool
		err           error
	}
	results := make([]result, len(scopePRs))

	// The GitHub calls (one ListComments per PR) run concurrently; the
	// version-mutation pass below stays sequential over scopePRs in its
	// original order, so the output is deterministic regardless of which
	// goroutine finishes first.
	runBounded(len(scopePRs), concurrency, func(i int) {
		pr := scopePRs[i]
		prKey := strconv.Itoa(pr.Number)

		sinceID, baselineEstablished := tracked[prKey]

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

	// Build the updated watermark table: start from whatever was already
	// tracked, drop entries for PRs no longer open (the pruning that keeps
	// this bounded), then overlay fresh results for everything just
	// scanned.
	openPRs := make(map[string]bool, len(allPRs))
	for _, pr := range allPRs {
		openPRs[strconv.Itoa(pr.Number)] = true
	}
	newWatermarks := make(map[string]int64, len(scopePRs))
	for prKey, id := range tracked {
		if openPRs[prKey] {
			newWatermarks[prKey] = id
		}
	}
	for i, pr := range scopePRs {
		newWatermarks[strconv.Itoa(pr.Number)] = results[i].latestMatchID
	}

	// Only stamp the watermark table onto an EXISTING (already
	// about-to-be-returned) version when the table actually changed and
	// nothing else already makes a version new this cycle to carry it on.
	// Stamping it onto every version unconditionally — the previous
	// behavior — made an unrelated PR's merge (which prunes that PR's
	// entry out of the table above) change the comment_watermarks blob on
	// every OTHER open PR's version too, even though those PRs' own
	// commits and comments never changed. Concourse's ATC compares the
	// whole version map for equality, so that alone made every one of
	// them look like a brand-new version and spuriously rebuilt their
	// already-built commits. A PR that actually triggered this cycle
	// already gets the table attached below (it's a genuinely new version
	// regardless), so it only needs a carrier here when nothing already
	// triggered. The chosen carrier is restricted to a still-open PR —
	// never the fallback echo of an already-merged/closed PR's last known
	// version — so pruning a closed PR's entry can, at worst, cause one
	// still-open PR to rebuild its unchanged commit, instead of resurrecting
	// a dead PR's old commit or (the original bug) rebuilding every open PR.
	anyTriggered := false
	for _, r := range results {
		if r.triggered {
			anyTriggered = true
			break
		}
	}
	if !maps.Equal(tracked, newWatermarks) && !anyTriggered && len(versions) > 0 {
		last := len(versions) - 1
		if openPRs[versions[last].PR] {
			versions[last].CommentBaseline = true
			versions[last].CommentWatermarks = newWatermarks
			if id, ok := newWatermarks[versions[last].PR]; ok {
				versions[last].CommentID = id
			}
		}
	}

	for i, pr := range scopePRs {
		if !results[i].triggered {
			continue
		}
		prKey := strconv.Itoa(pr.Number)
		versions = append(versions, models.Version{
			PR:                  prKey,
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
			CommentID:           newWatermarks[prKey],
			CommentBaseline:     true,
			CommentWatermarks:   newWatermarks,
		})
	}

	return versions, nil
}
