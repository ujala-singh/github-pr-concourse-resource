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

// sentinelPR is a reserved version PR value — never a real GitHub PR
// number, which are always >= 1 — used for the dedicated bookkeeping
// version applyCommentTriggers appends to carry the comment-trigger
// watermark table forward across checks. prlist.In and the pipeline's own
// task script both need to recognize this value and skip real work for
// it; see applyCommentTriggers and In.
const sentinelPR = "0"

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
	// appear to lose its place. Never echo the comment-trigger bookkeeping
	// version this way — applyCommentTriggers below always appends a
	// fresh one of its own when trigger_comments is set, so echoing the
	// old one here would just add a second, stale "PR" == sentinelPR
	// entry alongside it.
	if len(versions) == 0 && request.Version != nil && request.Version.PR != sentinelPR {
		versions = []models.Version{*request.Version}
	}

	if len(request.Source.TriggerComments) > 0 {
		var err error
		versions, err = applyCommentTriggers(ctx, request, github, prs, versions, concurrency)
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

// applyCommentTriggers scans every currently open PR for a comment
// matching source.trigger_comments, stamping the latest match onto that
// PR's own already-present version (if any) and appending an extra
// version for any PR that genuinely triggered but isn't otherwise part of
// this check's output (its latest push doesn't match source.paths).
//
// A real PR's own CommentID/CommentBaseline still describes only that one
// PR — nothing about PR B's version ever depends on PR A's data — so
// those fields are never perturbed by another PR's activity, including
// that PR closing. Earlier designs got this wrong in different ways: one
// embedded a shared, multi-PR table directly on every real PR's version
// (any PR's watermark changing made every other one look new and
// rebuilt its already-built commit); another dropped the shared table
// entirely in favor of a single implicit cursor (request.Version.PR),
// which meant commenting on two different PRs within the same check
// window silently dropped whichever one wasn't already the cursor; a
// third always appended a dedicated bookkeeping version (PR ==
// sentinelPR) LAST on every single check, which broke plain
// commit-triggering: confirmed live, Concourse treats a check's last
// returned element as "current," and re-ranks even an unchanged entry
// ahead of others whenever it reappears, so an always-last bookkeeping
// version permanently buried any real commit that landed in the same or
// a later check cycle — the commit was correctly recorded in history but
// never actually built.
//
// This version only touches the shared table on a check where it
// genuinely changed, and even then only piggybacks it onto a PR's entry
// when that PR is ALREADY, unambiguously new this cycle for its own
// reason: it has a genuine trigger. Everything else — a PR closing, a
// brand-new PR appearing without yet triggering, or simply the previous
// "latest" version happening to be a plain commit that carries no table
// at all (in which case tracked looks empty even though nothing is
// actually new) — falls back to a standalone bookkeeping version, never
// to piggybacking on an arbitrary existing entry. An intermediate design
// tried to also piggyback whenever a key was absent from tracked,
// treating that as "a brand-new PR, safe to attach to" — but an absent
// key is also exactly what a plain-commit "latest" version produces for
// every PR, so that version — confirmed live — attached the table to a
// stable, already-built PR's version essentially at random, spuriously
// rebuilding it. There's no way to tell "genuinely new" apart from
// "merely unrecoverable right now" from inside this function, so only
// the unambiguous signal (a real trigger) is trusted to piggyback; this
// costs one extra, harmless bookkeeping build the first time the table
// needs re-establishing after a plain commit, which is a small, bounded
// price for never again risking an unrelated PR's version. A plain new
// commit, with no comment activity involved, is never touched by any of
// this: it flows through untouched and is free to be the check's last
// element, so Concourse builds it normally. request.Version.CommentWatermarks
// is read regardless of which PR that version happens to be about — it's
// never restricted to only the bookkeeping version — since a piggybacked
// real PR version carries it just as well.
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, allPRs []*models.PullRequest, versions []models.Version, concurrency int) ([]models.Version, error) {
	var tracked map[string]int64
	if request.Version != nil {
		tracked = request.Version.CommentWatermarks
	}

	type result struct {
		pr            *models.PullRequest
		latestMatchID int64
		triggered     bool
		err           error
	}
	results := make([]result, len(allPRs))

	// Every currently open PR is scanned, regardless of path match, so a
	// comment trigger works even when the latest push doesn't touch a
	// matching path. The GitHub calls run concurrently; the
	// version-mutation pass below stays sequential over allPRs' original
	// order, so the output is deterministic regardless of which goroutine
	// finishes first.
	runBounded(len(allPRs), concurrency, func(i int) {
		pr := allPRs[i]
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
		results[i] = result{pr: pr, latestMatchID: latestMatchID, triggered: triggered}
	})

	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
	}

	byPR := make(map[string]result, len(results))
	newTable := make(map[string]int64, len(results))
	for _, r := range results {
		prKey := strconv.Itoa(r.pr.Number)
		byPR[prKey] = r
		newTable[prKey] = r.latestMatchID
	}

	// Stamp each PR's own, freshly observed comment state onto its own
	// already-present version. This never looks at any other PR's data,
	// so a stable PR's entry stays byte-identical check to check as long
	// as its own comments haven't changed — nothing happening on a
	// different PR can ever perturb it.
	present := make(map[string]bool, len(versions))
	for j := range versions {
		present[versions[j].PR] = true
		r, ok := byPR[versions[j].PR]
		if !ok {
			continue
		}
		versions[j].CommentID = r.latestMatchID
		versions[j].CommentBaseline = true
		if r.triggered {
			// The entry being updated might be the "echo back the last
			// known version" fallback rather than a fresh snapshot (e.g.
			// this PR's latest push doesn't currently match
			// source.paths), which can carry a stale commit. Refresh it
			// to the PR's actual current HEAD so a genuine trigger always
			// reflects what's really there to build, not a leftover
			// value from whenever this PR last path-matched.
			versions[j].Commit = r.pr.HeadRefOID
			versions[j].CommittedDate = r.pr.CommittedDate
			versions[j].ApprovedReviewCount = r.pr.ApprovedReviewCount
		}
	}

	anyTriggered := false
	for _, r := range results {
		if r.triggered {
			anyTriggered = true
			break
		}
	}

	triggeredPRs := make(map[string]bool)
	for _, r := range results {
		if !r.triggered {
			continue
		}
		prKey := strconv.Itoa(r.pr.Number)
		triggeredPRs[prKey] = true
		if present[prKey] {
			// Already stamped in place above — don't duplicate the entry.
			continue
		}
		versions = append(versions, models.Version{
			PR:                  prKey,
			Commit:              r.pr.HeadRefOID,
			CommittedDate:       r.pr.CommittedDate,
			ApprovedReviewCount: r.pr.ApprovedReviewCount,
			CommentID:           r.latestMatchID,
			CommentBaseline:     true,
		})
	}

	// newTable (this cycle's complete, freshly pruned-to-currently-open-PRs
	// table) only needs to be recorded somewhere when it actually changed.
	// Recomputing and re-returning an unchanged table costs nothing in
	// theory, but re-touches whatever carries it every single check — see
	// the doc comment above for why that broke real commit-triggering.
	if !maps.Equal(tracked, newTable) {
		attached := false
		if anyTriggered {
			// A triggered PR's entry is already genuinely new this cycle
			// — piggyback the table there. It isn't necessarily the LAST
			// entry already (e.g. a triggered PR that was already present
			// among the base versions is stamped in place, not appended),
			// so find it and move it last explicitly rather than assuming
			// position — attaching the table to whatever happens to be
			// last would risk landing on an unrelated, unchanged PR.
			for j := range versions {
				if !triggeredPRs[versions[j].PR] {
					continue
				}
				versions[j].CommentWatermarks = newTable
				last := len(versions) - 1
				versions[j], versions[last] = versions[last], versions[j]
				attached = true
				break
			}
		}
		if !attached {
			// Nothing else is independently new this cycle to safely
			// piggyback on. This covers several cases that all share the
			// same shape — a PR closed/merged, a brand-new PR entered
			// scope without yet triggering, or simply the previous
			// version happened to be a plain commit that didn't carry the
			// table (so tracked looks "empty" even though nothing is
			// actually new) — and in every one of them, there's no way to
			// tell "genuinely new" apart from "merely unrecoverable right
			// now" from inside this function. Treating every key absent
			// from tracked as "new" and piggybacking on it was tried and
			// is unsafe: confirmed live, it attached the table to a
			// completely stable, unrelated PR's version whenever the
			// table simply hadn't been recoverable, spuriously rebuilding
			// its already-built commit. A standalone bookkeeping version
			// is the only way to persist the table without ever risking
			// that — it's the sole new thing this cycle, so it's safe for
			// it to be last.
			//
			// Except on this resource's very first-ever check
			// (request.Version == nil) when real PR entries are ALSO
			// present: Concourse always starts a brand-new resource from
			// whatever check returns as its LAST array element, even with
			// version: every — confirmed live — so appending the sentinel
			// after a real PR's first-ever entry would make Concourse
			// treat THAT entry as pre-existing history to skip, not build,
			// exactly as it deliberately does for a freshly added
			// git-resource with 1000 existing commits. Skipping the
			// sentinel here costs nothing: there's no prior table to lose
			// on a cold start, and the very next check — no longer a cold
			// start — can safely record it via the normal paths above.
			if request.Version == nil && len(versions) > 0 {
				return versions, nil
			}
			versions = append(versions, models.Version{
				PR:                sentinelPR,
				CommentBaseline:   true,
				CommentWatermarks: newTable,
			})
		}
	}

	return versions, nil
}
