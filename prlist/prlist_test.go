package prlist

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"
	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

func TestSource_Validate(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		wantErr bool
	}{
		{
			name: "valid source",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token123",
				},
			},
			wantErr: false,
		},
		{
			name: "missing repository",
			source: Source{
				CommonConfig: models.CommonConfig{
					AccessToken: "token123",
				},
			},
			wantErr: true,
		},
		{
			name: "missing access token",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository: "owner/repo",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Source.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckRequest_Validate(t *testing.T) {
	validSource := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
	}

	tests := []struct {
		name    string
		request CheckRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: CheckRequest{
				Source: validSource,
			},
			wantErr: false,
		},
		{
			name: "valid request with version",
			request: CheckRequest{
				Source:  validSource,
				Version: &models.Version{PR: "1"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckRequest validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInRequest_Validate(t *testing.T) {
	validSource := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
	}

	tests := []struct {
		name    string
		request InRequest
		wantErr bool
	}{
		{
			name: "valid request",
			request: InRequest{
				Source:  validSource,
				Version: models.Version{PR: "1"},
			},
			wantErr: false,
		},
		{
			name: "valid request with skip download",
			request: InRequest{
				Source:  validSource,
				Version: models.Version{PR: "1"},
				Params:  InParams{SkipDownload: true},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("InRequest validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestIn_BookkeepingVersion_WritesMarkerWithoutGithubCalls verifies In
// recognizes the comment-trigger bookkeeping version (PR == sentinelPR)
// and short-circuits to a safe no-op instead of trying to look up or clone
// a PR that doesn't exist — the regression this guards against is In
// calling github.GetPullRequest(ctx, 0), which would fail outright.
func TestIn_BookkeepingVersion_WritesMarkerWithoutGithubCalls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no GitHub API call should be made for the bookkeeping version, got request for %s", r.URL.Path)
	})
	gc := newTestGithubClient(t, mux)

	dest := t.TempDir()
	version := models.Version{
		PR:                sentinelPR,
		CommentBaseline:   true,
		CommentWatermarks: map[string]int64{"5": 42},
	}
	request := InRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: version,
	}

	response, err := In(request, gc, dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	markerPath := filepath.Join(dest, BookkeepingMarkerFile)
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("expected marker file %s to exist: %v", markerPath, err)
	}

	if response.Version.PR != sentinelPR {
		t.Errorf("response version PR = %s, want %s", response.Version.PR, sentinelPR)
	}

	var prMeta *models.Metadata
	for i := range response.Metadata {
		if response.Metadata[i].Name == "pr" {
			prMeta = &response.Metadata[i]
		}
	}
	if prMeta == nil || prMeta.Value != sentinelPR {
		t.Errorf("expected metadata pr=%s, got %+v", sentinelPR, response.Metadata)
	}
}

// newTestGithubClient wires a models.GithubClient's V3 REST client to an
// httptest server so CheckTriggerComments can be exercised without hitting
// the real GitHub API.
func newTestGithubClient(t *testing.T, mux *http.ServeMux) *models.GithubClient {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	// A dedicated *http.Transport, not github.NewClient(nil) — see
	// models/github_paths_test.go's newTestGithubClientForPaths for why
	// nil (which falls back to the shared, process-wide
	// http.DefaultTransport) can hang a test under heavy repeated runs by
	// reusing a stale connection to an already-closed httptest.Server.
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	v3.BaseURL = baseURL

	return &models.GithubClient{
		V3: v3,
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			TriggerComments: []string{"concourse plan"},
		},
	}
}

// TestRunBounded_CallsEveryIndexExactlyOnce verifies the worker pool covers
// every index in [0, n) exactly once, including n larger than
// DefaultCheckConcurrency (forcing multiple batches) and n == 0 (no-op).
func TestRunBounded_CallsEveryIndexExactlyOnce(t *testing.T) {
	for _, n := range []int{0, 1, DefaultCheckConcurrency, DefaultCheckConcurrency*3 + 1} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			seen := make([]int32, n)
			runBounded(n, DefaultCheckConcurrency, func(i int) {
				atomic.AddInt32(&seen[i], 1)
			})
			for i, count := range seen {
				if count != 1 {
					t.Errorf("index %d called %d times, want exactly 1", i, count)
				}
			}
		})
	}
}

// TestRunBounded_RespectsConcurrencyLimit verifies no more than
// DefaultCheckConcurrency goroutines run fn at the same time.
func TestRunBounded_RespectsConcurrencyLimit(t *testing.T) {
	var (
		mu        sync.Mutex
		current   int
		maxSeen   int
		callCount int
	)

	n := DefaultCheckConcurrency * 4
	runBounded(n, DefaultCheckConcurrency, func(i int) {
		mu.Lock()
		current++
		callCount++
		if current > maxSeen {
			maxSeen = current
		}
		mu.Unlock()

		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		current--
		mu.Unlock()
	})

	if callCount != n {
		t.Fatalf("callCount = %d, want %d", callCount, n)
	}
	if maxSeen > DefaultCheckConcurrency {
		t.Errorf("observed %d concurrent calls, want at most %d", maxSeen, DefaultCheckConcurrency)
	}
}

// newTestPathFilterGithubClient wires a models.GithubClient's V3 REST
// client to an httptest server that serves PR-changed-files responses for
// MatchesPathFilters, keyed by PR number.
func newTestPathFilterGithubClient(t *testing.T, paths []string, filesByPR map[int]string, errPRs map[int]bool) *models.GithubClient {
	t.Helper()
	mux := http.NewServeMux()
	for number, files := range filesByPR {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", number), func(w http.ResponseWriter, r *http.Request) {
			if errPRs[number] {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = fmt.Fprint(w, `{"message": "boom"}`)
				return
			}
			_, _ = fmt.Fprint(w, files)
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	v3.BaseURL = baseURL

	return &models.GithubClient{
		V3: v3,
		Config: models.CommonConfig{
			Repository: "owner/repo",
			Paths:      paths,
		},
	}
}

// TestFilterPRsByPath_PreservesInputOrder runs many PRs concurrently through
// filterPRsByPath and verifies the matching subset comes back in the exact
// same relative order as the input, even though goroutines finish out of
// order. This matters because Check's output order should be a stable,
// deterministic function of GetPullRequests' order, not of goroutine
// scheduling.
func TestFilterPRsByPath_PreservesInputOrder(t *testing.T) {
	filesByPR := map[int]string{
		1: `[{"filename": "terraform/a.tf"}]`,
		2: `[{"filename": "docs/readme.md"}]`,
		3: `[{"filename": "terraform/b.tf"}]`,
		4: `[{"filename": "docs/other.md"}]`,
		5: `[{"filename": "terraform/c.tf"}]`,
	}
	gc := newTestPathFilterGithubClient(t, []string{"terraform/**"}, filesByPR, nil)

	prs := []*models.PullRequest{
		{Number: 1}, {Number: 2}, {Number: 3}, {Number: 4}, {Number: 5},
	}

	filtered, err := filterPRsByPath(context.Background(), gc, prs, DefaultCheckConcurrency, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantNumbers := []int{1, 3, 5}
	if len(filtered) != len(wantNumbers) {
		t.Fatalf("got %d matching PRs, want %d: %+v", len(filtered), len(wantNumbers), filtered)
	}
	for i, pr := range filtered {
		if pr.Number != wantNumbers[i] {
			t.Errorf("filtered[%d].Number = %d, want %d (order not preserved)", i, pr.Number, wantNumbers[i])
		}
	}
}

// TestFilterPRsByPath_PropagatesErrorFromAnyWorker verifies that an error
// from any single concurrent path-filter call fails the whole check, with
// the originating PR number in the error message.
func TestFilterPRsByPath_PropagatesErrorFromAnyWorker(t *testing.T) {
	filesByPR := map[int]string{
		1: `[{"filename": "terraform/a.tf"}]`,
		2: `[{"filename": "terraform/b.tf"}]`,
		3: `[{"filename": "terraform/c.tf"}]`,
	}
	gc := newTestPathFilterGithubClient(t, []string{"terraform/**"}, filesByPR, map[int]bool{2: true})

	prs := []*models.PullRequest{{Number: 1}, {Number: 2}, {Number: 3}}

	_, err := filterPRsByPath(context.Background(), gc, prs, DefaultCheckConcurrency, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "PR #2") {
		t.Errorf("error = %v, want it to mention PR #2", err)
	}
}

func TestApplyCommentTriggers_FirstObservation_EstablishesBaselineWithoutFiring(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 555, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr := &models.PullRequest{Number: 42, HeadRefOID: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: nil, // no prior version at all — this PR has never been observed
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, v := range versions {
		if v.PR == "42" {
			t.Fatalf("expected no triggered version on first observation of a PR, got one: %+v", versions)
		}
	}
	// The bookkeeping version is still appended, establishing PR #42's
	// baseline for a future check to compare against. This is safe here
	// specifically because the base versions argument passed in is nil —
	// PR #42 isn't present in the base snapshot (e.g. it doesn't currently
	// path-match), so there's no real entry for the sentinel to be
	// appended after; see TestApplyCommentTriggers_ColdStart_NeverAppendsSentinelAfterARealPR
	// for the case where one IS present on a cold start.
	if len(versions) != 1 || versions[0].PR != sentinelPR {
		t.Fatalf("expected exactly the bookkeeping version, got %+v", versions)
	}
	if versions[0].CommentWatermarks["42"] != 555 {
		t.Errorf("bookkeeping CommentWatermarks[\"42\"] = %d, want 555 (establishing baseline)", versions[0].CommentWatermarks["42"])
	}
}

// TestApplyCommentTriggers_ColdStart_NeverAppendsSentinelAfterARealPR is a
// regression test for the exact bug confirmed live: a resource with zero
// prior PR history (request.Version == nil — this is its very first-ever
// check) that discovers a brand-new PR with no comment activity involved.
//
// Confirmed directly against a running Concourse instance: on a resource's
// first-ever check, Concourse starts from whatever the check returns as
// its LAST array element, even with version: every configured — the same
// deliberate design that stops a freshly added git-resource with 1000
// historical commits from queuing 1000 builds. Appending the standalone
// bookkeeping version after PR #8's own first-ever entry made Concourse
// treat PR #8 as pre-existing history to skip, not build: its version was
// correctly recorded, but `in` was never run for it (confirmed via the
// live resource-versions API — it had no in-time metadata at all), while
// the bookkeeping version — now "latest" — got built instead.
func TestApplyCommentTriggers_ColdStart_NeverAppendsSentinelAfterARealPR(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/8/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[]`) // brand new PR, no comments at all yet
	})
	gc := newTestGithubClient(t, mux)

	pr8 := &models.PullRequest{Number: 8, HeadRefOID: "a6e76106", CommittedDate: "2026-10-04T10:48:34Z"}
	// PR #8 already matched path filters and is present in the base
	// snapshot — exactly what Check's commit-based loop produces for a
	// brand-new, path-matching PR appearing for the first time.
	baseVersions := []models.Version{
		{PR: "8", Commit: "a6e76106", CommittedDate: "2026-10-04T10:48:34Z"},
	}

	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: nil, // this resource has never returned anything before
	}

	versions, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr8}, baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, v := range versions {
		if v.PR == sentinelPR {
			t.Fatalf("a standalone bookkeeping version was appended after PR #8's own first-ever entry on a cold start — Concourse would treat PR #8 as pre-existing history and never build it: %+v", versions)
		}
	}
	if len(versions) != 1 || versions[0].PR != "8" || versions[0].Commit != "a6e76106" {
		t.Fatalf("expected exactly PR #8's own entry, nothing else, got %+v", versions)
	}
}

// TestApplyCommentTriggers_SimultaneousCommentsOnTwoPRs_BothFire is the
// definitive end-to-end regression test for the reported symptom that
// motivated the bookkeeping version: commenting "concourse plan" on two
// different PRs within the same check interval used to silently drop
// whichever PR wasn't the single implicit cursor (request.Version.PR).
// With watermarks read from the dedicated bookkeeping version instead,
// both PRs' own prior baselines are reliably recoverable in the same
// check, regardless of which real PR's version was last "latest" — so
// both comments fire.
func TestApplyCommentTriggers_SimultaneousCommentsOnTwoPRs_BothFire(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/39/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 100, "body": "concourse plan"}, {"id": 201, "body": "concourse plan"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/41/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 200, "body": "concourse plan"}, {"id": 202, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr39 := &models.PullRequest{Number: 39, HeadRefOID: "sha-39", CommittedDate: "2026-10-03T14:48:11Z"}
	pr41 := &models.PullRequest{Number: 41, HeadRefOID: "sha-41", CommittedDate: "2026-10-04T07:52:31Z"}
	baseVersions := []models.Version{
		{PR: "39", Commit: "sha-39", CommittedDate: "2026-10-03T14:48:11Z"},
		{PR: "41", Commit: "sha-41", CommittedDate: "2026-10-04T07:52:31Z"},
	}

	// Both PRs already have an established baseline from a prior check,
	// carried on the bookkeeping version — not tied to either PR being
	// "the cursor."
	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: map[string]int64{"39": 100, "41": 200},
		},
	}

	versions, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr39, pr41}, baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pr39Fired, pr41Fired bool
	for _, v := range versions {
		switch {
		case v.PR == "39" && v.CommentID == 201:
			pr39Fired = true
		case v.PR == "41" && v.CommentID == 202:
			pr41Fired = true
		}
	}
	if !pr39Fired {
		t.Errorf("PR #39's comment trigger did not fire: %+v", versions)
	}
	if !pr41Fired {
		t.Errorf("PR #41's comment trigger did not fire: %+v", versions)
	}
}

// TestApplyCommentTriggers_PlainNewCommit_NeverTouchedByBookkeeping is the
// definitive regression test for the bug confirmed live against a running
// pipeline: a genuine new commit, pushed with no comment activity
// involved, stopped triggering builds entirely. The cause was an earlier
// design that unconditionally appended a dedicated bookkeeping version
// LAST on every single check — Concourse treats a check's last returned
// element as "current" and re-ranks it ahead of everything else whenever
// it reappears, so an always-last bookkeeping entry permanently buried
// any real commit landing in the same or a later check: the commit was
// correctly recorded in history but never actually built.
//
// Here nothing about comments changes (the watermark table is already
// exactly up to date), so applyCommentTriggers must not add or touch
// anything beyond the plain commit-based entry already in versions.
func TestApplyCommentTriggers_PlainNewCommit_NeverTouchedByBookkeeping(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/41/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 200, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr41 := &models.PullRequest{Number: 41, HeadRefOID: "new-sha", CommittedDate: "2026-10-04T09:48:26Z"}
	// The base snapshot already reflects the new commit — this is exactly
	// what Check's commit-based loop produces for a freshly pushed,
	// path-matching PR, before applyCommentTriggers runs.
	baseVersions := []models.Version{
		{PR: "41", Commit: "new-sha", CommittedDate: "2026-10-04T09:48:26Z"},
	}

	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: map[string]int64{"41": 200}, // already up to date
		},
	}

	versions, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr41}, baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(versions) != 1 {
		t.Fatalf("expected exactly the plain commit-based version, nothing added, got %d: %+v", len(versions), versions)
	}
	v := versions[0]
	if v.PR != "41" || v.Commit != "new-sha" {
		t.Fatalf("version = %+v, want PR 41 at the new commit", v)
	}
	if v.CommentWatermarks != nil {
		t.Errorf("CommentWatermarks = %v, want nil — a plain new commit must never carry the shared table, or it risks being buried by a later, unrelated bookkeeping update", v.CommentWatermarks)
	}
}

// TestApplyCommentTriggers_UnrecoverableTable_FallsBackToSentinel_NeverPiggybacks
// is a regression test for the exact bug confirmed live immediately after
// the previous fix: once a plain new commit becomes the resource's
// "latest" version (carrying no CommentWatermarks, as it should), the very
// next check has tracked == nil even though nothing about ANY PR's own
// comments has changed. An earlier version of this function treated every
// key absent from a nil/empty tracked map as "a brand-new PR, safe to
// piggyback the table on" — but tracked being empty here doesn't mean any
// PR is actually new; it only means the table wasn't recoverable from
// whatever became "latest" last time. That version incorrectly attached
// the table to an arbitrary, completely stable, already-built PR's
// version, spuriously rebuilding it. Neither PR here has a new comment, so
// neither of their entries may be touched; the table must only be
// recorded via a standalone bookkeeping version instead.
func TestApplyCommentTriggers_UnrecoverableTable_FallsBackToSentinel_NeverPiggybacks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/39/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 100, "body": "concourse plan"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/41/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 200, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr39 := &models.PullRequest{Number: 39, HeadRefOID: "sha-39", CommittedDate: "2026-10-03T14:48:11Z"}
	pr41 := &models.PullRequest{Number: 41, HeadRefOID: "sha-41", CommittedDate: "2026-10-04T10:17:25Z"}
	baseVersions := []models.Version{
		{PR: "39", Commit: "sha-39", CommittedDate: "2026-10-03T14:48:11Z"},
		{PR: "41", Commit: "sha-41", CommittedDate: "2026-10-04T10:17:25Z"},
	}

	// request.Version is a plain commit-based entry (PR 41's own, from the
	// immediately preceding check) — it carries no CommentWatermarks at
	// all, exactly as a plain new commit should. Both PRs' comment ids
	// (100, 200) are already fully up to date; nothing has changed.
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: &models.Version{PR: "41", Commit: "sha-41", CommentID: 200, CommentBaseline: true},
	}

	versions, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr39, pr41}, baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, v := range versions {
		if (v.PR == "39" || v.PR == "41") && v.CommentWatermarks != nil {
			t.Errorf("PR #%s was perturbed by the unrecoverable table — got CommentWatermarks %v, want nil: %+v", v.PR, v.CommentWatermarks, v)
		}
	}

	var sentinel *models.Version
	for i := range versions {
		if versions[i].PR == sentinelPR {
			sentinel = &versions[i]
		}
	}
	if sentinel == nil {
		t.Fatalf("expected a standalone bookkeeping version to carry the table, got %+v", versions)
	}
	want := map[string]int64{"39": 100, "41": 200}
	if !maps.Equal(sentinel.CommentWatermarks, want) {
		t.Errorf("bookkeeping CommentWatermarks = %v, want %v", sentinel.CommentWatermarks, want)
	}
}

func TestApplyCommentTriggers_NewCommentAfterBaseline_Fires(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 100, "body": "concourse plan"}, {"id": 555, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr := &models.PullRequest{Number: 42, HeadRefOID: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	// The baseline lives on the bookkeeping version (sentinelPR), not on
	// any real PR's own version.
	prevVersion := &models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: map[string]int64{"42": 100}}
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: prevVersion,
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No separate bookkeeping version: the triggered entry is already
	// genuinely new this cycle, so the table rides on it directly instead
	// of a second, standalone entry — this matters because a separate
	// trailing bookkeeping entry would bury a plain new commit landing in
	// the same or a later check (confirmed live; see applyCommentTriggers'
	// doc comment).
	if len(versions) != 1 {
		t.Fatalf("expected exactly the triggered version (table piggybacked on it), got %d: %+v", len(versions), versions)
	}
	triggered := versions[0]
	if triggered.CommentID != 555 {
		t.Errorf("CommentID = %d, want 555", triggered.CommentID)
	}
	if triggered.PR != "42" || triggered.Commit != "abc123" {
		t.Errorf("triggered version = %+v, want PR 42 at current HEAD abc123", triggered)
	}
	if triggered.CommentWatermarks["42"] != 555 {
		t.Errorf("triggered version CommentWatermarks[\"42\"] = %d, want 555", triggered.CommentWatermarks["42"])
	}
}

// TestApplyCommentTriggers_NonSentinelVersion_TreatsAsNoBaseline covers the
// one-time migration case: right after upgrading from a version of this
// resource that predates the comment-trigger bookkeeping version,
// request.Version is still some real PR's own version, not the sentinel.
// Since watermarks are only ever read from the sentinel, no PR has an
// established baseline yet — not even the PR request.Version happens to
// be about — until the bookkeeping version this check appends establishes
// one for the future.
func TestApplyCommentTriggers_NonSentinelVersion_TreatsAsNoBaseline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 555, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr := &models.PullRequest{Number: 42, HeadRefOID: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	prevVersion := &models.Version{PR: "42", Commit: "abc000", CommentID: 900, CommentBaseline: true}
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: prevVersion,
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, v := range versions {
		if v.PR == "42" {
			t.Fatalf("PR #42 triggered even though request.Version wasn't the bookkeeping version: %+v", versions)
		}
	}
	if len(versions) != 1 || versions[0].PR != sentinelPR {
		t.Fatalf("expected exactly the bookkeeping version, got %+v", versions)
	}
	if versions[0].CommentWatermarks["42"] != 555 {
		t.Errorf("bookkeeping CommentWatermarks[\"42\"] = %d, want 555 (establishing baseline)", versions[0].CommentWatermarks["42"])
	}
}

// TestApplyCommentTriggers_ClosingOnePRNeverPerturbsAnother is a regression
// test for the exact bug confirmed live against a running pipeline: PR #41
// getting a real new trigger comment (or closing — either way, PR #41's
// own state changing) must never alter PR #39's version in any way. There
// is no shared data structure between PRs any more, so this holds by
// construction, but it's asserted explicitly since it's the precise,
// previously-reported symptom.
func TestApplyCommentTriggers_ClosingOnePRNeverPerturbsAnother(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/39/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 5977983085, "body": "concourse plan"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/41/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 5978113103, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr39 := &models.PullRequest{Number: 39, HeadRefOID: "abdabec5fd", CommittedDate: "2026-10-03T14:48:11Z"}
	pr41 := &models.PullRequest{Number: 41, HeadRefOID: "3f1d91c719", CommittedDate: "2026-10-04T07:52:31Z"}
	baseVersions := []models.Version{
		{PR: "39", Commit: "abdabec5fd", CommittedDate: "2026-10-03T14:48:11Z"},
		{PR: "41", Commit: "3f1d91c719", CommittedDate: "2026-10-04T07:52:31Z"},
	}

	// Both PR #39 and PR #41 already have an established baseline, carried
	// on the bookkeeping version — not on either PR's own version.
	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: map[string]int64{"39": 5977983085, "41": 5978113103},
		},
	}

	versionsBeforePR41Closes, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr39, pr41}, baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pr39Before models.Version
	for _, v := range versionsBeforePR41Closes {
		if v.PR == "39" {
			pr39Before = v
		}
	}

	// Now PR #41 closes/merges — it's simply absent from allPRs on the
	// next check. Re-run with a fresh copy of the base versions (as a real
	// check would start from a fresh snapshot) and confirm PR #39's
	// resulting version is identical to before.
	baseVersionsAfter := []models.Version{
		{PR: "39", Commit: "abdabec5fd", CommittedDate: "2026-10-03T14:48:11Z"},
	}
	versionsAfterPR41Closes, err := applyCommentTriggers(
		context.Background(), request, gc, []*models.PullRequest{pr39}, baseVersionsAfter, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pr39After models.Version
	for _, v := range versionsAfterPR41Closes {
		if v.PR == "39" {
			pr39After = v
		}
	}

	if !reflect.DeepEqual(pr39Before, pr39After) {
		t.Errorf("PR #39's version changed after PR #41 closed:\nbefore: %+v\nafter:  %+v", pr39Before, pr39After)
	}
}

// TestApplyCommentTriggers_ManyPRsConcurrently scans more PRs than
// DefaultCheckConcurrency at once (forcing multiple bounded worker batches) and
// verifies two things despite goroutines completing out of order:
//  1. Every PR's own already-present version gets stamped with that PR's
//     own freshly observed comment id, independent of every other PR —
//     each one's own slot is written correctly regardless of which
//     goroutine finishes first.
//  2. Only cursorPR (the one PR whose baseline was already established via
//     request.Version) is actually treated as "triggered": every other PR
//     also gets its own current comment id recorded (establishing ITS OWN
//     baseline for a future check), but since none of them had a prior
//     baseline to compare against this cycle, none of them count as firing
//     a build. Since cursorPR is already present in the base versions (not
//     missing due to a path mismatch), its trigger is reflected by
//     updating its own entry in place — no extra version is appended.
func TestApplyCommentTriggers_ManyPRsConcurrently(t *testing.T) {
	const n = DefaultCheckConcurrency*2 + 3 // force multiple bounded batches
	const cursorPR = 5                      // arbitrary PR whose baseline is established

	mux := http.NewServeMux()
	prs := make([]*models.PullRequest, n)
	preExisting := make([]models.Version, n)
	for i := range n {
		number := i + 1
		prs[i] = &models.PullRequest{
			Number:        number,
			HeadRefOID:    fmt.Sprintf("sha-%d", number),
			CommittedDate: "2026-01-01T00:00:00Z",
		}
		preExisting[i] = models.Version{PR: strconv.Itoa(number), Commit: fmt.Sprintf("sha-%d", number)}

		// Every PR has a comment newer than id 100 sitting on it, but only
		// cursorPR has an established baseline (see request.Version below),
		// so only cursorPR should actually fire.
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", number), func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"id": 100, "body": "concourse plan"}, {"id": %d, "body": "concourse plan"}]`, 1000+number)
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3,
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			TriggerComments: []string{"concourse plan"},
		},
	}

	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: map[string]int64{strconv.Itoa(cursorPR): 100},
		},
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, prs, preExisting, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No extra versions are appended at all: cursorPR was already present
	// among the base versions, so its trigger is reflected in place, and
	// since it's already genuinely new this cycle, the table piggybacks
	// directly on its entry instead of a separate bookkeeping version —
	// moving it last in the process (see applyCommentTriggers' doc
	// comment for why a separate, always-last bookkeeping entry is unsafe).
	if len(versions) != n {
		t.Fatalf("got %d versions, want exactly %d (no extra appends expected): %+v", len(versions), n, versions)
	}

	// Every PR's own entry must carry its own freshly observed comment id
	// and an established baseline — regardless of whether it counted as
	// "triggered" this cycle. Order is no longer guaranteed to match input
	// order: the triggered PR (cursorPR) gets moved to the end to carry
	// the table, so look each one up by PR number instead of by index.
	byPR := make(map[string]models.Version, len(versions))
	for _, v := range versions {
		byPR[v.PR] = v
	}
	for i := range n {
		number := i + 1
		v, ok := byPR[strconv.Itoa(number)]
		if !ok {
			t.Fatalf("no version found for PR #%d: %+v", number, versions)
		}
		if !v.CommentBaseline {
			t.Errorf("PR #%d: CommentBaseline = false, want true", number)
		}
		wantCommentID := int64(1000 + number)
		if v.CommentID != wantCommentID {
			t.Errorf("PR #%d: CommentID = %d, want %d", number, v.CommentID, wantCommentID)
		}
	}

	// cursorPR's entry — now the last one — carries the full watermark
	// table for every PR, not just its own, so each one's baseline is
	// recoverable on a future check regardless of which PR cursorPR ends
	// up being next.
	sentinel := versions[n-1]
	if sentinel.PR != strconv.Itoa(cursorPR) {
		t.Fatalf("versions[%d].PR = %s, want cursorPR %d to carry the table as the last entry", n-1, sentinel.PR, cursorPR)
	}
	for i := range n {
		number := i + 1
		wantCommentID := int64(1000 + number)
		if got := sentinel.CommentWatermarks[strconv.Itoa(number)]; got != wantCommentID {
			t.Errorf("bookkeeping CommentWatermarks[%q] = %d, want %d", strconv.Itoa(number), got, wantCommentID)
		}
	}
}

// pullRequestsGraphQLResponse builds a minimal GraphQL response body for
// GetPullRequests' query, containing a single open PR with the given
// number/commit, no next page.
func pullRequestsGraphQLResponse(number int, headSHA string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequests":{"edges":[{"node":{
		"number": %d,
		"title": "test PR",
		"url": "https://github.com/owner/repo/pull/%d",
		"state": "OPEN",
		"isDraft": false,
		"baseRefName": "main",
		"headRefName": "feature",
		"headRefOid": %q,
		"repository": {"url": "https://github.com/owner/repo"},
		"headRepository": {"url": "https://github.com/owner/repo"},
		"author": {"login": "someone", "avatarUrl": ""},
		"labels": {"nodes": []},
		"commits": {"nodes": [{"commit": {"oid": %q, "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
		"reviews": {"nodes": []}
	}}], "pageInfo": {"endCursor": "", "hasNextPage": false}}}}}`, number, number, headSHA, headSHA)
}

// TestCheck_NewCommitOnAlreadyTrackedPR_IsDetected is a regression test for
// the bug where an already-tracked PR's new commit was silently dropped.
// The removed filterNewVersions helper matched purely on PR number: once a
// PR had been seen once (became the resource's "last known version"), any
// later commit to that SAME PR was found at "the cursor" and skipped
// forever, no matter how much its Commit/CommittedDate changed. Check must
// now return the PR's current version unconditionally so Concourse's own
// version-history dedup — not our own flawed position heuristic — decides
// what's actually new.
func TestCheck_NewCommitOnAlreadyTrackedPR_IsDetected(t *testing.T) {
	const prNumber = 32
	const oldSHA = "aaa111"
	const newSHA = "bbb222"

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(prNumber, newSHA))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	gc := &models.GithubClient{
		V4:     githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo"},
	}

	// Simulate: this PR was already the resource's last-known version, at
	// its OLD commit — exactly the state after a prior check first
	// discovered it.
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: &models.Version{PR: strconv.Itoa(prNumber), Commit: oldSHA},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(versions) != 1 {
		t.Fatalf("got %d versions, want exactly 1 (the PR's new commit): %+v", len(versions), versions)
	}
	if versions[0].PR != strconv.Itoa(prNumber) {
		t.Errorf("versions[0].PR = %s, want %d", versions[0].PR, prNumber)
	}
	if versions[0].Commit != newSHA {
		t.Errorf("versions[0].Commit = %s, want %s (new commit was dropped — the bug is back)", versions[0].Commit, newSHA)
	}
}

// TestCheck_PathFilter_OnlyConsidersFilesChangedSinceLastCheck is an
// end-to-end regression test for the reported bug: a PR whose cumulative
// base...HEAD diff includes a path-matching file (from an earlier,
// already-built commit) kept matching forever, even once its latest push
// touched only unrelated files. For the PR this resource already has a
// last-known commit for (the cursor), Check must now diff from that
// commit instead of using the PR's full history — so this PR, despite its
// earlier terraform change, correctly stops matching once the latest push
// is unrelated. It also asserts pulls/{number}/files (the cumulative-diff
// endpoint) is never hit in this case — if it were, the old bug's root
// cause would still be reachable regardless of what the compare endpoint
// returns.
func TestCheck_PathFilter_OnlyConsidersFilesChangedSinceLastCheck(t *testing.T) {
	const prNumber = 40
	const oldSHA = "f17cb0bc598907938993ec4ff7e4691333b93123"
	const newSHA = "8228e7b92bafa2009649cbebf613a42d475febf0"

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(prNumber, newSHA))
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prNumber), func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("pulls/%d/files (cumulative diff) must not be called for an already-tracked PR", prNumber)
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/%s...%s", oldSHA, newSHA), func(w http.ResponseWriter, r *http.Request) {
		// Only the latest push's own change — nothing under
		// concourse-demo-setup/terraform/**, unlike the PR's full history.
		_, _ = fmt.Fprint(w, `{"files": [
			{"filename": "concourse-demo-setup/pipelines/GCP/teleport-ssh-agent.yaml"}
		]}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3.BaseURL = baseURL

	gc := &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository: "owner/repo",
			Paths:      []string{"concourse-demo-setup/terraform/**"},
		},
	}

	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: &models.Version{PR: strconv.Itoa(prNumber), Commit: oldSHA},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With no currently-matching PRs, Check echoes back the last known
	// version rather than returning an empty list — but critically, that
	// echoed version must be byte-identical to request.Version (same old
	// commit), not the new commit. Concourse only triggers a build on a
	// version it hasn't already recorded, so this proves no retrigger
	// fires for a push that didn't touch the configured path.
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want exactly 1 (the echoed-back last known version): %+v", len(versions), versions)
	}
	if versions[0].Commit != oldSHA {
		t.Errorf("versions[0].Commit = %s, want %s (old commit) — a version for the new commit would retrigger the build even though it didn't touch the configured path", versions[0].Commit, oldSHA)
	}
}

func TestResolveCheckConcurrency(t *testing.T) {
	tests := []struct {
		name   string
		source Source
		want   int
	}{
		{
			name:   "unset uses default",
			source: Source{},
			want:   DefaultCheckConcurrency,
		},
		{
			name:   "zero uses default",
			source: Source{CommonConfig: models.CommonConfig{CheckConcurrency: 0}},
			want:   DefaultCheckConcurrency,
		},
		{
			name:   "configured value is used as-is",
			source: Source{CommonConfig: models.CommonConfig{CheckConcurrency: 25}},
			want:   25,
		},
		{
			name:   "configured value of 1 is respected, not treated as unset",
			source: Source{CommonConfig: models.CommonConfig{CheckConcurrency: 1}},
			want:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCheckConcurrency(tt.source)
			if got != tt.want {
				t.Errorf("resolveCheckConcurrency() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestCheck_RespectsConfiguredCheckConcurrency proves source.check_concurrency
// actually reaches the worker pool end-to-end through Check, not just
// resolveCheckConcurrency in isolation: it sets a concurrency of 1 (fully
// sequential) and asserts no two path-filter calls ever overlap, then
// repeats with a higher value and asserts overlap does happen — ruling out
// a test that would pass even if Check silently ignored the config.
func TestCheck_RespectsConfiguredCheckConcurrency(t *testing.T) {
	const prCount = 6

	newServerAndClient := func(t *testing.T, trackConcurrency func(delta int)) *models.GithubClient {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
			var edges []string
			for i := 1; i <= prCount; i++ {
				edges = append(edges, fmt.Sprintf(`{"node":{
					"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
					"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
					"repository": {"url": "u"}, "headRepository": {"url": "u"},
					"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
					"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
					"reviews": {"nodes": []}
				}}`, i, i, i))
			}
			_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`,
				strings.Join(edges, ","))
		})
		for i := 1; i <= prCount; i++ {
			mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", i), func(w http.ResponseWriter, r *http.Request) {
				trackConcurrency(1)
				time.Sleep(20 * time.Millisecond)
				trackConcurrency(-1)
				_, _ = fmt.Fprint(w, `[{"filename": "terraform/a.tf"}]`)
			})
		}
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)

		v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
		baseURL, err := url.Parse(server.URL + "/")
		if err != nil {
			t.Fatalf("failed to parse test server URL: %v", err)
		}
		v3.BaseURL = baseURL

		return &models.GithubClient{
			V3:     v3,
			V4:     githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
			Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}},
		}
	}

	runWithConcurrency := func(t *testing.T, concurrency int) (maxObserved int) {
		var mu sync.Mutex
		var current int
		gc := newServerAndClient(t, func(delta int) {
			mu.Lock()
			current += delta
			if current > maxObserved {
				maxObserved = current
			}
			mu.Unlock()
		})
		gc.Config.CheckConcurrency = concurrency

		request := CheckRequest{Source: Source{CommonConfig: gc.Config}}
		if _, err := Check(request, gc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return maxObserved
	}

	t.Run("check_concurrency=1 is fully sequential", func(t *testing.T) {
		if got := runWithConcurrency(t, 1); got != 1 {
			t.Errorf("max concurrent path-filter calls = %d, want 1 (check_concurrency=1 was not respected)", got)
		}
	})

	t.Run("check_concurrency=6 allows overlap", func(t *testing.T) {
		if got := runWithConcurrency(t, prCount); got <= 1 {
			t.Errorf("max concurrent path-filter calls = %d, want > 1 (check_concurrency=%d had no effect)", got, prCount)
		}
	})
}

// TestCheck_CommentTrigger_FiresEvenWhenLatestPushDoesNotMatchPaths is a
// regression test for a real production incident: PR #5 on
// dbt-labs/dbt-concourse-config had an established baseline (it was the
// resource's tracked cursor), then got a commit touching only the pipeline
// bootstrap YAML — outside source.paths — followed by a genuine "concourse
// plan" comment. Three consecutive forced checks against the real pipeline
// never produced a new version.
//
// Root cause: filterPRsByPath correctly excludes a PR from the commit-
// triggering set when its latest push doesn't touch a matching path (that
// fix is intentional and correct — see
// TestCheck_PathFilter_OnlyConsidersFilesChangedSinceLastCheck). But
// applyCommentTriggers was only ever given that same filtered set, so once
// the cursor PR fell out of it, its comments were never scanned again —
// permanently, since nothing else ever advances the cursor away from it.
// A "concourse plan" comment isn't a push and has no reason to depend on
// what the latest commit happened to touch.
func TestCheck_CommentTrigger_FiresEvenWhenLatestPushDoesNotMatchPaths(t *testing.T) {
	const prNumber = 5
	const oldSHA = "9a1bfa3cfa2bfa18544879af4c637275c7d1292a" // last build (matched paths)
	const newSHA = "c2d150b4de4a56eef0866b3e5cfc3a27b551022f" // bootstrap-yaml-only push (doesn't)
	const oldCommentID = 0
	const newCommentID = 5971991589

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(prNumber, newSHA))
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/%s...%s", oldSHA, newSHA), func(w http.ResponseWriter, r *http.Request) {
		// The latest push only touched the bootstrap YAML, outside paths.
		_, _ = fmt.Fprint(w, `{"files": [{"filename": "concourse-config-bootstrap-staging.yaml"}]}`)
	})
	// request.Version is now the bookkeeping version (not PR #5's own), so
	// filterPRsByPath has no sinceSHA for PR #5 and falls back to the full
	// diff endpoint instead of the scoped compare above — mock it the same
	// way so the test exercises "doesn't match paths" either way.
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prNumber), func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"filename": "concourse-config-bootstrap-staging.yaml"}]`)
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", prNumber), func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan"}]`, newCommentID)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3.BaseURL = baseURL

	gc := &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			Paths:           []string{"environments/staging/**", "modules/**", "pipelines/staging/**"},
			TriggerComments: []string{"concourse plan"},
		},
	}

	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR:                sentinelPR,
			CommentBaseline:   true,
			CommentWatermarks: map[string]int64{strconv.Itoa(prNumber): oldCommentID},
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var triggered *models.Version
	for i := range versions {
		if versions[i].PR == strconv.Itoa(prNumber) && versions[i].CommentID == newCommentID {
			triggered = &versions[i]
		}
	}
	if triggered == nil {
		t.Fatalf("no version with the new comment's ID was emitted — the comment trigger did not fire: %+v", versions)
	}
	if triggered.Commit != newSHA {
		t.Errorf("triggered version Commit = %s, want %s (current HEAD)", triggered.Commit, newSHA)
	}
	if !triggered.CommentBaseline {
		t.Errorf("triggered version CommentBaseline = false, want true")
	}
}

// TestCheck_CommentTrigger_NonSentinelLastVersion_ReEstablishesBaseline
// covers the one-time migration edge case: request.Version is some real
// PR's own version (here, PR #20's), not the bookkeeping version. This can
// only happen right after upgrading from a version of this resource that
// predates the bookkeeping version, or on the very first-ever check — in
// steady-state operation, applyCommentTriggers always appends the
// bookkeeping version last, so it's always what Concourse remembers as
// "latest" from the second check onward, and every tracked PR's baseline
// (including PR #5's here) is always reliably recoverable from it
// regardless of which PR last got a new commit.
//
// Since watermarks are only ever read from the bookkeeping version,
// request.Version being PR #20's own version instead means NO PR has a
// recoverable baseline this check — including PR #5, even though PR #5 had
// one established previously. Its matching comment must NOT fire a build
// here; it only (re-)establishes PR #5's baseline on the freshly appended
// bookkeeping version, which fires on the comment after this one.
func TestCheck_CommentTrigger_NonSentinelLastVersion_ReEstablishesBaseline(t *testing.T) {
	const prA = 5
	const prASHA = "aaa111"
	const oldCommentID = 100
	const newCommentID = 200

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(prA, prASHA))
	})
	// PR #5's changed files deliberately don't match source.paths, so it's
	// excluded from the commit-triggering set (filteredPRs) — the only way
	// a PR #5 version could appear this check is via a genuine comment
	// trigger firing.
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prA), func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"filename": "unrelated/file.txt"}]`)
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", prA), func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan"}, {"id": %d, "body": "concourse plan"}]`, oldCommentID, newCommentID)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3.BaseURL = baseURL

	gc := &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			Paths:           []string{"relevant/**"},
			TriggerComments: []string{"concourse plan"},
		},
	}

	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR:              "20", // a different PR is the cursor
			Commit:          "zzz999",
			CommentBaseline: true,
			CommentID:       999, // PR #20's own baseline, irrelevant to PR #5
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, v := range versions {
		if v.PR == strconv.Itoa(prA) && v.CommentID == newCommentID {
			t.Fatalf("PR #%d's comment trigger fired even though the cursor was on a different PR — no shared table should exist to make this possible: %+v", prA, versions)
		}
	}
}

// TestCheck_MultipleCommitsAndNewPROpenedInSameCheck is an end-to-end
// regression test combining every scenario this feature has broken in
// some earlier form, all in a single check — the realistic situation
// where several things happen within one ~1-minute check interval:
//   - PR #10 and PR #20 (both already tracked) each get a genuine new
//     commit, with no comment activity involved.
//   - PR #30 opens for the first time in this very check, with no
//     comment activity either.
//
// None of these three events has anything to do with comments, so none
// of their versions may carry CommentWatermarks or otherwise differ from
// what a plain commit-based check would produce. The watermark table did
// change (PR #30 is now tracked), so it must still be recorded somewhere
// — via a standalone bookkeeping version, never by perturbing any of the
// three real entries above.
func TestCheck_MultipleCommitsAndNewPROpenedInSameCheck(t *testing.T) {
	type prFixture struct {
		number        int
		headSHA       string
		committedDate string
		commentID     int64 // highest matching comment currently on this PR
	}
	prs := []prFixture{
		{number: 10, headSHA: "new-sha-10", committedDate: "2026-10-04T10:00:00Z", commentID: 100},
		{number: 20, headSHA: "new-sha-20", committedDate: "2026-10-04T10:05:00Z", commentID: 200},
		{number: 30, headSHA: "sha-30", committedDate: "2026-10-04T10:10:00Z", commentID: 0}, // brand new, no comments at all yet
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, pr := range prs {
			edges = append(edges, fmt.Sprintf(`{"node":{
				"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": %q,
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": %q, "committedDate": %q, "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}}`, pr.number, pr.headSHA, pr.headSHA, pr.committedDate))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`,
			strings.Join(edges, ","))
	})
	for _, pr := range prs {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", pr.number), func(w http.ResponseWriter, r *http.Request) {
			if pr.commentID == 0 {
				_, _ = fmt.Fprint(w, `[]`)
				return
			}
			_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan"}]`, pr.commentID)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3.BaseURL = baseURL

	gc := &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			TriggerComments: []string{"concourse plan"},
			// No Paths configured: every PR matches unconditionally, so
			// this test exercises the commit/comment interaction alone.
		},
	}

	// PR #10 and #20 were already tracked (old commits, baselines
	// established); PR #30 isn't in the table at all yet.
	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: map[string]int64{"10": 100, "20": 200},
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byPR := make(map[string]models.Version, len(versions))
	for _, v := range versions {
		byPR[v.PR] = v
	}

	wantCommits := map[string]string{"10": "new-sha-10", "20": "new-sha-20", "30": "sha-30"}
	for prKey, wantCommit := range wantCommits {
		v, ok := byPR[prKey]
		if !ok {
			t.Fatalf("no version found for PR #%s: %+v", prKey, versions)
		}
		if v.Commit != wantCommit {
			t.Errorf("PR #%s: Commit = %s, want %s", prKey, v.Commit, wantCommit)
		}
		if v.CommentWatermarks != nil {
			t.Errorf("PR #%s was perturbed by the comment-trigger table — got CommentWatermarks %v, want nil: %+v", prKey, v.CommentWatermarks, v)
		}
	}

	sentinel, ok := byPR[sentinelPR]
	if !ok {
		t.Fatalf("expected a standalone bookkeeping version recording PR #30 joining the tracked table, got %+v", versions)
	}
	want := map[string]int64{"10": 100, "20": 200, "30": 0}
	if !maps.Equal(sentinel.CommentWatermarks, want) {
		t.Errorf("bookkeeping CommentWatermarks = %v, want %v", sentinel.CommentWatermarks, want)
	}

	if len(versions) != 4 {
		t.Fatalf("got %d versions, want exactly 4 (3 real PRs + 1 bookkeeping): %+v", len(versions), versions)
	}
}

// TestCheck_CommitsNewPRAndCommentTriggers_AllInOneCheck extends the
// scenario above with the remaining realistic case: two ALREADY-tracked
// PRs also get a genuine new "concourse plan" comment in the very same
// check, alongside two other PRs getting a plain new commit and a brand
// new PR opening — everything that can happen within one ~1-minute check
// interval, happening at once.
//
//   - PR #10, #20: new commit, no comment activity (same as above).
//   - PR #30: opens for the first time, no comment activity (same as above).
//   - PR #40, #50: NO new commit, but each gets a genuine new matching
//     comment — both must fire; neither may be silently dropped in favor
//     of the other (the exact bug a shared, single-cursor table caused).
//
// Since #40/#50 triggering already provides an independently-new entry to
// carry the table, PR #30 joining the tracked set rides along for free —
// no separate bookkeeping version should be needed at all this time.
func TestCheck_CommitsNewPRAndCommentTriggers_AllInOneCheck(t *testing.T) {
	type prFixture struct {
		number        int
		headSHA       string
		committedDate string
		oldCommentID  int64 // 0 means "not yet tracked"
		newCommentID  int64 // 0 means "no new comment this check"
	}
	prs := []prFixture{
		{number: 10, headSHA: "new-sha-10", committedDate: "2026-10-04T10:00:00Z", oldCommentID: 100, newCommentID: 100},
		{number: 20, headSHA: "new-sha-20", committedDate: "2026-10-04T10:05:00Z", oldCommentID: 200, newCommentID: 200},
		{number: 30, headSHA: "sha-30", committedDate: "2026-10-04T10:10:00Z", oldCommentID: 0, newCommentID: 0},
		{number: 40, headSHA: "sha-40", committedDate: "2026-09-01T00:00:00Z", oldCommentID: 400, newCommentID: 401},
		{number: 50, headSHA: "sha-50", committedDate: "2026-09-02T00:00:00Z", oldCommentID: 500, newCommentID: 501},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, pr := range prs {
			edges = append(edges, fmt.Sprintf(`{"node":{
				"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": %q,
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": %q, "committedDate": %q, "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}}`, pr.number, pr.headSHA, pr.headSHA, pr.committedDate))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`,
			strings.Join(edges, ","))
	})
	for _, pr := range prs {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", pr.number), func(w http.ResponseWriter, r *http.Request) {
			if pr.newCommentID == 0 {
				_, _ = fmt.Fprint(w, `[]`)
				return
			}
			_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan"}]`, pr.newCommentID)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}
	v3.BaseURL = baseURL

	gc := &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			TriggerComments: []string{"concourse plan"},
		},
	}

	tracked := map[string]int64{}
	for _, pr := range prs {
		if pr.oldCommentID != 0 {
			tracked[strconv.Itoa(pr.number)] = pr.oldCommentID
		}
	}
	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR: sentinelPR, CommentBaseline: true,
			CommentWatermarks: tracked,
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byPR := make(map[string]models.Version, len(versions))
	for _, v := range versions {
		byPR[v.PR] = v
	}

	// No separate bookkeeping version: #40/#50 triggering already
	// provides a carrier, so PR #30 joining the table rides along for free.
	if _, ok := byPR[sentinelPR]; ok {
		t.Errorf("unexpected standalone bookkeeping version — a trigger already provides a carrier this cycle: %+v", versions)
	}
	if len(versions) != len(prs) {
		t.Fatalf("got %d versions, want exactly %d (one per real PR, no extras): %+v", len(versions), len(prs), versions)
	}

	wantTable := map[string]int64{"10": 100, "20": 200, "30": 0, "40": 401, "50": 501}
	var carrierPR string
	for _, pr := range prs {
		prKey := strconv.Itoa(pr.number)
		v, ok := byPR[prKey]
		if !ok {
			t.Fatalf("no version found for PR #%d: %+v", pr.number, versions)
		}
		if v.Commit != pr.headSHA {
			t.Errorf("PR #%d: Commit = %s, want %s", pr.number, v.Commit, pr.headSHA)
		}
		// Every PR's own entry always carries its own current comment
		// state, deterministically, once trigger_comments is configured —
		// triggered or not (see TestApplyCommentTriggers_ManyPRsConcurrently).
		// For #10/#20/#30 that's their unchanged (or, for #30, baseline)
		// id; "perturbed" here specifically means picking up something
		// that isn't its own — e.g. the shared table, checked separately
		// below.
		if v.CommentID != pr.newCommentID {
			t.Errorf("PR #%d: CommentID = %d, want %d", pr.number, v.CommentID, pr.newCommentID)
		}
		if !v.CommentBaseline {
			t.Errorf("PR #%d: CommentBaseline = false, want true", pr.number)
		}
		if v.CommentWatermarks != nil {
			if carrierPR != "" {
				t.Fatalf("more than one version carries CommentWatermarks — PR #%s and PR #%d: %+v", carrierPR, pr.number, versions)
			}
			carrierPR = prKey
			if !maps.Equal(v.CommentWatermarks, wantTable) {
				t.Errorf("PR #%d (carrier) CommentWatermarks = %v, want %v", pr.number, v.CommentWatermarks, wantTable)
			}
		}
	}
	if carrierPR == "" {
		t.Error("expected exactly one triggered PR's version to carry the full watermark table, found none")
	}
}
