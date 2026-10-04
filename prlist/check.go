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
// tracked PR's watermark into one map and carrying the complete, current
// table forward on whichever version Concourse ends up remembering as
// "latest" next, the table survives regardless of which PR that happens
// to be about — removing that limitation.
//
// allPRs (every currently open, GitHub-API-matching PR, not just
// path-filtered ones) is used to prune entries for PRs that have
// closed/merged/dropped out, bounding the map to roughly the current
// open-PR count rather than growing forever — but that pruning is only
// ever applied on a cycle where something else already makes a version
// new (a genuine trigger, or a PR entering tracked scope for the first
// time), never on its own. See the comment above the usePruned
// computation below for why: eagerly changing the table's content purely
// because some OTHER PR closed, while every version carried the full
// table, used to make Concourse rebuild every open PR's already-built
// commit the moment any one of them closed.
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

	// Grow-only overlay of the tracked table with this cycle's scan
	// results. This alone never removes anything, so a value here only
	// differs from tracked[prKey] in two cases: a real trigger fired for
	// an already-tracked PR (handled below — that PR's own triggered
	// entry is a genuinely new version regardless), or prKey is entering
	// tracked scope for the first time. The second case always coincides
	// with that PR's OWN (pr, commit) version also being new — it's never
	// been returned before — so there's always an independently-new place
	// to carry it, never a need to perturb an unrelated, unchanged PR.
	overlaid := make(map[string]int64, len(tracked)+len(scopePRs))
	maps.Copy(overlaid, tracked)
	for i, pr := range scopePRs {
		overlaid[strconv.Itoa(pr.Number)] = results[i].latestMatchID
	}

	newKeys := make(map[string]bool)
	for prKey := range overlaid {
		if _, existed := tracked[prKey]; !existed {
			newKeys[prKey] = true
		}
	}

	anyTriggered := false
	for _, r := range results {
		if r.triggered {
			anyTriggered = true
			break
		}
	}

	// Dropping PRs that closed/merged out of the table is deferred until a
	// cycle where something else ALREADY makes a version new (a genuine
	// trigger, or a newKeys PR's own first-ever version) — never performed
	// on its own. Pruning eagerly, every check regardless, was the bug:
	// since it changes the table's content purely because some OTHER PR
	// closed, and the table used to get stamped onto every returned
	// version, Concourse's ATC (which compares the whole version map for
	// equality) saw every other open, completely unchanged PR as a new
	// version and rebuilt their already-built commits. Deferring costs
	// nothing but a few stale entries lingering in the table a bit longer
	// — bounded by how long it takes before any real activity happens
	// again, at which point that activity's own new version absorbs the
	// prune for free.
	usePruned := anyTriggered || len(newKeys) > 0
	newWatermarks := overlaid
	if usePruned {
		openPRs := make(map[string]bool, len(allPRs))
		for _, pr := range allPRs {
			openPRs[strconv.Itoa(pr.Number)] = true
		}
		pruned := make(map[string]int64, len(overlaid))
		for prKey, id := range overlaid {
			if openPRs[prKey] {
				pruned[prKey] = id
			}
		}
		newWatermarks = pruned
	}

	// When nothing triggered this cycle, a triggered entry (appended
	// below, always last) isn't available to carry the table forward —
	// fall back to one of the newKeys PRs' own base versions instead,
	// moving it last so it's the one Concourse remembers as "latest" next.
	// usePruned guarantees at least one exists whenever this runs: it's
	// only true here because len(newKeys) > 0 (anyTriggered is false in
	// this branch).
	if usePruned && !anyTriggered {
		for j := range versions {
			if !newKeys[versions[j].PR] {
				continue
			}
			versions[j].CommentBaseline = true
			versions[j].CommentWatermarks = newWatermarks
			if id, ok := newWatermarks[versions[j].PR]; ok {
				versions[j].CommentID = id
			}
			last := len(versions) - 1
			versions[j], versions[last] = versions[last], versions[j]
			break
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
