package prlist

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"sync"
	"time"

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

// sortVersionsByRecency orders versions by their latest event ascending —
// the commit, or the PR's newest trigger comment (commentedAt, keyed by
// PR) if that's later — breaking ties by PR number ascending for
// determinism. See Check's doc comment on why this matters: Concourse
// assigns check_order by array position, and version: every builds the
// versions after the last one built, so a version must never sort before
// one already built that's older. Ordering by commit date alone did that
// to comment triggers: one check after a comment triggered, its PR fell
// back behind any PR with a newer commit — including one already built —
// before the scheduler had built it, and it never was.
func sortVersionsByRecency(versions []models.Version, commentedAt map[string]time.Time) {
	// latest is a version's latest event, and for a trigger comment its ID:
	// GitHub assigns comment IDs in order across the repository, so two
	// comments in the same second still sort in the order they were posted
	// (a PR-number tie-break could put one that's still to be built before
	// one already built). Commit-only events tie on 0.
	latest := func(v models.Version) (time.Time, int64) {
		t, _ := time.Parse(time.RFC3339, v.CommittedDate)
		if c, ok := commentedAt[v.PR]; ok && !c.Before(t) {
			return c, v.CommentID
		}
		return t, 0
	}
	sort.SliceStable(versions, func(i, j int) bool {
		ti, ci := latest(versions[i])
		tj, cj := latest(versions[j])
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if ci != cj {
			return ci < cj
		}
		iNum, _ := strconv.Atoi(versions[i].PR)
		jNum, _ := strconv.Atoi(versions[j].PR)
		return iNum < jNum
	})
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
	// how much its Commit/CommittedDate had changed since. Returning the
	// full snapshot every check is both simpler and correct in terms of
	// WHAT it returns — an unchanged PR's version is a no-op, a PR with a
	// new commit (or a brand-new PR) is a new row regardless of its
	// position in the list.
	//
	// Position still matters, though — confirmed live, Concourse assigns
	// check_order to a check's returned array strictly by array position,
	// for every version it mentions, new row or not: whichever entry
	// lands last gets bumped to be the newest-ranked, even if it's an
	// unchanged row just reappearing, and even ahead of a different,
	// brand-new row that appeared earlier in the very same array. GitHub
	// GraphQL's pullRequests connection defaults to CREATED_AT ascending
	// with no orderBy override, so filteredPRs — and this loop's output —
	// is ordered by PR creation date, NOT by recency of change. Left
	// alone, that means whichever open, path-matching PR happens to have
	// been opened most recently always lands last and perpetually "wins"
	// the current slot, starving every other PR's genuinely new commits
	// from ever being recognized — confirmed live: a PR opened later kept
	// outranking an older PR's actual new commit even across repeated
	// forced re-checks. Sorting by CommittedDate
	// instead aligns the array's order with actual code recency, so the
	// PR that most recently pushed lands last and wins, regardless of
	// which PR happens to have the newer number.
	var versions []models.Version
	for _, pr := range filteredPRs {
		versions = append(versions, models.Version{
			PR:                  strconv.Itoa(pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}
	sortVersionsByRecency(versions, nil)

	// If nothing currently matches (e.g. no open PRs touch the configured
	// paths), echo back the last known version so the resource doesn't
	// appear to lose its place. Never echo the comment-trigger bookkeeping
	// version this way — applyCommentTriggers below always appends a
	// fresh one of its own when trigger_comments is set, so echoing the
	// old one here would just add a second, stale "PR" == sentinelPR
	// entry alongside it.
	// Only while that PR is still open, as the check protocol asks: a closed
	// PR's version is no longer valid. That also rules out the bookkeeping
	// version, which isn't a PR.
	if len(versions) == 0 && request.Version != nil && isOpen(prs, request.Version.PR) {
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

// watermark is the newest comment already handled on a PR, and whether
// that's known (only a newer comment then triggers; an unknown baseline is
// established without triggering). It's the highest of:
//
//   - the bookkeeping table's entry, when this check has the table;
//   - the PR's comment-trigger note, which a build's get writes and every
//     check can read (see models.RecordHandledTrigger).
//
// When the table is lost and the PR has no note, no build has recorded
// picking up any of its comments, so its newest trigger comment fires —
// but only if it was posted after the PR's head commit. Without this, a
// comment while a real PR's version was current was recorded as the
// baseline and never built (seen live on a real multi-PR pipeline). The bound keeps
// a missing note (deleted, or never written before this release) from
// re-running a comment some earlier push has already superseded.
func watermark(tracked map[string]int64, prKey string, st models.TriggerState, tableLost bool, headCommitted string) (int64, bool) {
	sinceID, known := tracked[prKey]
	if st.HasNote {
		sinceID, known = max(sinceID, st.HandledID), true
	}
	if !known && tableLost {
		// Strictly before: a comment in the same second as the push
		// (both are second-precision) fires rather than being lost.
		if committed, err := time.Parse(time.RFC3339, headCommitted); err == nil && st.LatestMatchAt.Before(committed) {
			return st.LatestMatchID, true // superseded by a push: baseline, no trigger
		}
		return 0, true
	}
	return sinceID, known
}

// commentIsLatest reports whether the PR's newest trigger comment (id,
// posted at) is its latest event: stamped on v and not older than v's
// commit. A comment version is the PR,
// its head and that comment, not its approval count: an approval alone
// doesn't make it a new version, and nor does losing its note marker,
// which fires the same comment again.
func commentIsLatest(id int64, at time.Time, v models.Version) bool {
	if id == 0 || v.CommentID != id {
		return false
	}
	committed, err := time.Parse(time.RFC3339, v.CommittedDate)
	return err != nil || !at.Before(committed)
}

// isOpen reports whether pr is the number of one of prs.
func isOpen(prs []*models.PullRequest, pr string) bool {
	for _, p := range prs {
		if strconv.Itoa(p.Number) == pr {
			return true
		}
	}
	return false
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
		// Diff from the last known version's commit only if the PR has
		// been pushed to since: diffing the head against itself finds no
		// files and would drop the PR, so an unchanged PR uses its full
		// diff instead.
		var sinceSHA string
		if lastVersion != nil && lastVersion.PR == strconv.Itoa(pr.Number) && lastVersion.Commit != pr.HeadRefOID {
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
// that PR closing. The shared comment-trigger watermark table
// (CommentWatermarks) is handled completely separately, and is NEVER
// attached to a real PR's version — only ever to a dedicated, standalone
// bookkeeping version (PR == sentinelPR). Earlier designs got this wrong
// in different ways: one embedded the shared table directly on every
// real PR's version (any PR's watermark changing made every other one
// look new and rebuilt its already-built commit); another dropped the
// shared table entirely in favor of a single implicit cursor
// (request.Version.PR), which meant commenting on two different PRs
// within the same check window silently dropped whichever one wasn't
// already the cursor; a third always appended the bookkeeping version
// LAST on every single check, which broke plain commit-triggering:
// confirmed live, Concourse treats a check's last returned element as
// "current," and re-ranks even an unchanged entry ahead of others
// whenever it reappears, so an always-last bookkeeping version
// permanently buried any real commit that landed in the same or a later
// check cycle — the commit was correctly recorded in history but never
// actually built. A fourth piggybacked the table onto whichever real
// PR's version was "already, unambiguously new this cycle for its own
// reason" (a genuine comment trigger), to avoid the extra bookkeeping
// build — this looked safe, since that PR's version genuinely was new
// regardless, but confirmed live it wasn't: that same PR, on a LATER
// check, reappeared with the identical commit and identical
// latest-comment-match id, just without CommentWatermarks attached
// (because some other PR carried the table that cycle instead, or
// nothing needed recording at all). Concourse diffs on the full version
// map, so "has CommentWatermarks" vs. "doesn't," alone, made an
// already-built, completely unchanged PR look like a brand-new version —
// and it was built again: a real, wasted rebuild, not a cosmetic one (a
// PR re-ran its terraform plan this way, set off by an unrelated commit
// to a different PR that merely made the table need re-recording).
//
// So: a real PR's version is built from ONLY that PR's own data, full
// stop — nothing here can ever make its shape depend on what any other
// PR, or the bookkeeping table, happens to be doing this cycle. Whenever
// the table changes, it's recorded on a standalone bookkeeping version,
// unconditionally — the bounded cost is that this bookkeeping version
// might also need building (harmless; see the COMMENT_TRIGGER_BOOKKEEPING
// skip in consuming pipelines), never a real PR's.
//
// That bookkeeping version is PREPENDED, not appended — confirmed live:
// the table needing re-recording doesn't require any comment to have
// actually changed; it also happens whenever request.Version (whatever
// this resource's last-known version was going into THIS check) turns
// out to be a plain real-PR version, which never carries
// CommentWatermarks, making `tracked` look empty/unrecoverable on its
// own. That can coincide, in
// the very same check, with a genuinely new, unrelated real commit —
// one Check's sortVersionsByRecency correctly placed last in `versions`
// specifically to win Concourse's "last element is current" ranking.
// Appending the bookkeeping version after it would instantly undo that:
// it's itself a new row whenever its content changes, so it would land
// last and outrank that real commit — the exact same "always-last
// bookkeeping version buries real commits" failure already described
// above for the third design, just reached by a different, non-comment
// trigger. Prepending it instead leaves every real entry's relative,
// recency-sorted order untouched, so whichever one is genuinely newest
// still wins — the bookkeeping version still gets its own harmless
// build, just never at a real one's expense. A plain new commit, with
// no comment activity involved, is never touched by any of this beyond
// its position relative to the (possibly prepended) bookkeeping
// version: it flows through untouched, free to be the check's last
// element, so Concourse builds it normally. request.Version.CommentWatermarks
// is read regardless of which PR that version happens to be about — in
// steady state it's now frequently a real PR's, not always the
// bookkeeping version's, precisely because prepending lets real commits
// legitimately win "latest" instead of it.
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, allPRs []*models.PullRequest, versions []models.Version, concurrency int) ([]models.Version, error) {
	var tracked map[string]int64
	if request.Version != nil {
		tracked = request.Version.CommentWatermarks
	}

	type result struct {
		pr            *models.PullRequest
		latestMatchID int64
		latestMatchAt time.Time
		triggered     bool
		// handled: the exact version a build fetched for this PR's newest
		// trigger comment, while its head hasn't moved. It keeps being
		// returned, so it keeps its place in the order (see the append
		// loop).
		handled *models.Version
		err     error
	}
	results := make([]result, len(allPRs))

	// Every currently open PR is scanned, regardless of path match, so a
	// comment trigger works even when the latest push doesn't touch a
	// matching path. The GitHub calls run concurrently; the
	// version-mutation pass below stays sequential over allPRs' original
	// order, so the output is deterministic regardless of which goroutine
	// finishes first.
	// The table is unrecoverable when Concourse hands this check a real
	// PR's version (which never carries it) rather than the bookkeeping
	// version — always the case right after a comment triggers.
	tableLost := request.Version != nil && tracked == nil

	runBounded(len(allPRs), concurrency, func(i int) {
		pr := allPRs[i]
		prKey := strconv.Itoa(pr.Number)

		st, err := github.TriggerState(ctx, pr.Number, request.Source.TriggerComments)
		if err != nil {
			results[i] = result{err: fmt.Errorf("failed to check trigger comments for PR #%d: %w", pr.Number, err)}
			return
		}
		sinceID, baselineEstablished := watermark(tracked, prKey, st, tableLost, pr.CommittedDate)
		triggered := baselineEstablished && st.LatestMatchID > sinceID
		// The exact version a build fetched for the PR's newest trigger
		// comment, kept only while the PR's head is still its commit: a
		// push needs a matching change or a fresh comment of its own, and
		// nothing else about the PR (an approval, a commit date) makes it
		// a new version.
		var handled *models.Version
		if v := st.HandledVersion; v != nil && st.LatestMatchID != 0 && st.HandledID == st.LatestMatchID &&
			v.PR == prKey && v.CommentID == st.LatestMatchID && v.Commit == pr.HeadRefOID {
			handled = v
		}
		results[i] = result{pr: pr, latestMatchID: st.LatestMatchID, latestMatchAt: st.LatestMatchAt, triggered: triggered, handled: handled}
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
		// While its trigger comment is the PR's latest event, its version
		// is the comment version, whether or not the PR matches paths this
		// check (which, for the current version's PR, depends on whether
		// it's diffed from its last commit or in full): the same shape the
		// append loop below gives it, so the two can't make two versions
		// of one trigger.
		if commentIsLatest(r.latestMatchID, r.latestMatchAt, versions[j]) {
			versions[j].ApprovedReviewCount = 0
		}
	}

	// A PR not otherwise returned (it doesn't match paths) gets its comment
	// version when the comment triggers, and keeps getting the same one
	// once a build has handled it, while the comment is still its latest
	// event. Dropping it from the output then would leave the other PRs'
	// versions, re-ranked by this check, ahead of it, and a version: every
	// job that hasn't built it yet would skip it. A comment only recorded
	// as a baseline (no build picked it up) never makes a version.
	for _, r := range results {
		if !r.triggered && r.handled == nil {
			continue
		}
		prKey := strconv.Itoa(r.pr.Number)
		if present[prKey] {
			// Already stamped in place above — don't duplicate the entry.
			continue
		}
		if !r.triggered {
			versions = append(versions, *r.handled) // exactly as it was built
			continue
		}
		// The same shape the in-place branch above gives a PR that matches
		// paths, so whichever branch a PR goes through (and, for the current
		// version's PR, that depends on how it's diffed), one trigger is one
		// version: the comment version (no approval count) while the comment
		// is the PR's latest event, else with its approval count. An
		// approval alone, or the comment firing again after its marker was
		// lost, then can't make a new version.
		v := models.Version{
			PR:                  prKey,
			Commit:              r.pr.HeadRefOID,
			CommittedDate:       r.pr.CommittedDate,
			ApprovedReviewCount: r.pr.ApprovedReviewCount,
			CommentID:           r.latestMatchID,
			CommentBaseline:     true,
		}
		if commentIsLatest(r.latestMatchID, r.latestMatchAt, v) {
			v.ApprovedReviewCount = 0
		}
		versions = append(versions, v)
	}

	// Order everything by its latest event, comments included (see
	// sortVersionsByRecency). A PR that triggered this check isn't moved
	// last on top of that: it would sit last only until the trigger is
	// recorded, then fall back to its event-time position next check,
	// and that reshuffle can put a version still waiting to build behind
	// one already built, so version: every never builds it. A fresh
	// comment is its PR's newest event, so it lands last anyway, the same
	// way on every check.
	commentedAt := make(map[string]time.Time, len(byPR))
	for k, r := range byPR {
		if r.latestMatchID != 0 {
			commentedAt[k] = r.latestMatchAt
		}
	}
	sortVersionsByRecency(versions, commentedAt)

	// newTable (this cycle's complete, freshly pruned-to-currently-open-PRs
	// table) only needs to be recorded somewhere when it actually changed.
	// Recomputing and re-returning an unchanged table costs nothing in
	// theory, but re-touches whatever carries it every single check — see
	// the doc comment above for why that broke real commit-triggering. It
	// is NEVER attached to a real PR's version (see the doc comment above
	// for why that's unsafe even when that PR is already, independently
	// new this cycle) — only ever to a standalone bookkeeping version.
	if !maps.Equal(tracked, newTable) {
		// Except on this resource's very first-ever check
		// (request.Version == nil) when real PR entries are ALSO present:
		// Concourse always starts a brand-new resource from whatever
		// check returns as its LAST array element, even with
		// version: every — confirmed live — so the sentinel landing
		// after a real PR's first-ever entry would make Concourse treat
		// THAT entry as pre-existing history to skip, not build, exactly
		// as it deliberately does for a freshly added git-resource with
		// 1000 existing commits. Skipping the sentinel here costs
		// nothing: there's no prior table to lose on a cold start, and
		// the very next check — no longer a cold start — can safely
		// record it below.
		if request.Version == nil && len(versions) > 0 {
			return versions, nil
		}

		sentinel := models.Version{
			PR:                sentinelPR,
			CommentBaseline:   true,
			CommentWatermarks: newTable,
		}

		// Confirmed live: the table needing to be re-recorded doesn't
		// require anything about comments to have actually changed — it
		// also happens whenever
		// request.Version (this resource's last-known version going into
		// THIS check) turns out to be a plain real-PR version, which
		// never carries CommentWatermarks by design, making `tracked`
		// look empty/unrecoverable even though nothing new is going on.
		// That can coincide, in the very same check, with a genuinely
		// new, unrelated real commit — one sortVersionsByRecency
		// correctly placed last in `versions`, specifically so it would
		// win Concourse's "last element is current" ranking. Appending
		// the sentinel after it would instantly undo that: the sentinel
		// is itself a brand-new row every time its content changes, so
		// it would land last and outrank that real commit, burying it —
		// the exact same "always-last bookkeeping version buries real
		// commits" failure already fixed for cold start, just via a
		// different trigger. Prepending it instead keeps every real
		// entry's relative, recency-sorted order intact, so whichever
		// one is genuinely newest still ends up last and wins — the
		// sentinel still gets its own harmless build (it's still a new
		// row), just never at the cost of a real one.
		versions = append([]models.Version{sentinel}, versions...)
	}

	return versions, nil
}
