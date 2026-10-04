package prlist

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
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

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected no triggered version on first observation of a PR, got %d: %+v", len(versions), versions)
	}
}

func TestApplyCommentTriggers_NewCommentAfterBaseline_Fires(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 100, "body": "concourse plan"}, {"id": 555, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr := &models.PullRequest{Number: 42, HeadRefOID: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	prevVersion := &models.Version{PR: "42", Commit: "abc000", CommentID: 100, CommentBaseline: true}
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: prevVersion,
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly one triggered version, got %d: %+v", len(versions), versions)
	}
	if versions[0].CommentID != 555 {
		t.Errorf("CommentID = %d, want 555", versions[0].CommentID)
	}
	if versions[0].PR != "42" || versions[0].Commit != "abc123" {
		t.Errorf("triggered version = %+v, want PR 42 at current HEAD abc123", versions[0])
	}
}

func TestApplyCommentTriggers_CursorOnDifferentPR_TreatsAsNoBaseline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `[{"id": 555, "body": "concourse plan"}]`)
	})
	gc := newTestGithubClient(t, mux)

	pr := &models.PullRequest{Number: 42, HeadRefOID: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	// The cursor currently points at a different PR (#7), so PR #42's own
	// comment watermark is unknown — this is the documented best-effort
	// limitation of list mode.
	prevVersion := &models.Version{PR: "7", Commit: "def456", CommentID: 900, CommentBaseline: true}
	request := CheckRequest{
		Source:  Source{CommonConfig: gc.Config},
		Version: prevVersion,
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, []*models.PullRequest{pr}, nil, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected no triggered version when the cursor is on a different PR, got %d: %+v", len(versions), versions)
	}
}

// TestApplyCommentTriggers_ManyPRsConcurrently scans more PRs than
// DefaultCheckConcurrency at once (forcing multiple bounded worker batches) and
// verifies two things despite goroutines completing out of order:
//  1. No pre-existing version gets perturbed by the scan — the watermark
//     table only needs a carrier when nothing already makes a version new
//     this cycle, and here cursorPR's trigger already provides one (see
//     TestApplyCommentTriggers_MergedPROnlyPerturbsOneOpenPR for the case
//     where there's no triggered entry to piggyback on).
//  2. Exactly one triggered version is appended — for the single PR whose
//     number matches the cursor (request.Version.PR) — since list mode can
//     only track one PR's baseline at a time (see applyCommentTriggers'
//     doc comment). A PR with a genuinely new comment but no established
//     baseline must NOT trigger, concurrency or not.
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
		Source:  Source{CommonConfig: gc.Config},
		Version: &models.Version{PR: strconv.Itoa(cursorPR), CommentID: 100, CommentBaseline: true},
	}

	versions, err := applyCommentTriggers(context.Background(), request, gc, prs, prs, preExisting, DefaultCheckConcurrency)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// None of the pre-existing (base) versions should be perturbed: cursorPR
	// already triggers this cycle, giving the watermark table a carrier
	// among the appended triggered versions below, so there's no need (and
	// — see the regression this guards against — no business) stamping the
	// table onto every other unrelated, unchanged PR's version too.
	for i := range n {
		number := i + 1
		v := versions[i]
		if v.PR != strconv.Itoa(number) {
			t.Fatalf("versions[%d].PR = %s, want %d (order not preserved)", i, v.PR, number)
		}
		if v.CommentBaseline {
			t.Errorf("PR #%d: CommentBaseline = true, want false (base version must not be stamped when a trigger already carries the table)", number)
		}
		if v.CommentID != 0 {
			t.Errorf("PR #%d: CommentID = %d, want 0 (base version must not be stamped)", number, v.CommentID)
		}
	}

	// Exactly one triggered version appended, for cursorPR only.
	triggeredVersions := versions[n:]
	if len(triggeredVersions) != 1 {
		t.Fatalf("got %d triggered versions, want exactly 1: %+v", len(triggeredVersions), triggeredVersions)
	}
	if triggeredVersions[0].PR != strconv.Itoa(cursorPR) {
		t.Errorf("triggered version PR = %s, want %d", triggeredVersions[0].PR, cursorPR)
	}
	if triggeredVersions[0].CommentID != int64(1000+cursorPR) {
		t.Errorf("triggered version CommentID = %d, want %d", triggeredVersions[0].CommentID, 1000+cursorPR)
	}
}

// TestApplyCommentTriggers_MergedPROnlyPerturbsOneOpenPR is a regression
// test for the exact reported bug: merging PR #3 (dropping it out of the
// open-PR list) must not spuriously rebuild every OTHER open, unrelated PR
// at its already-built commit. PR #3's watermark entry is pruned out of
// the shared table this check, which changes the table's content — but
// with no PR actually triggering this cycle, the only version allowed to
// carry that change forward is the single one that ends up last, and every
// OTHER tracked-but-untouched PR must come back exactly as it went in.
func TestApplyCommentTriggers_MergedPROnlyPerturbsOneOpenPR(t *testing.T) {
	mux := http.NewServeMux()
	for _, number := range []int{1, 2} {
		// Same comment id as the existing watermark — nothing new, no trigger.
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", number), func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan"}]`, 100*number)
		})
	}
	gc := newTestGithubClient(t, mux)

	// PR #1 and #2 are still open and unchanged; PR #3 just merged, so it's
	// absent from both scopePRs and allPRs (list mode's GetPullRequests
	// would no longer return it once it drops out of the OPEN state filter).
	pr1 := &models.PullRequest{Number: 1, HeadRefOID: "sha-1", CommittedDate: "2026-01-01T00:00:00Z"}
	pr2 := &models.PullRequest{Number: 2, HeadRefOID: "sha-2", CommittedDate: "2026-01-01T00:00:00Z"}
	baseVersions := []models.Version{
		{PR: "1", Commit: "sha-1"},
		{PR: "2", Commit: "sha-2"},
	}

	request := CheckRequest{
		Source: Source{CommonConfig: gc.Config},
		Version: &models.Version{
			PR:                "2",
			Commit:            "sha-2",
			CommentWatermarks: map[string]int64{"1": 100, "2": 200, "3": 300},
		},
	}

	versions, err := applyCommentTriggers(
		context.Background(), request, gc,
		[]*models.PullRequest{pr1, pr2}, []*models.PullRequest{pr1, pr2},
		baseVersions, DefaultCheckConcurrency,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected no extra triggered versions, got %d: %+v", len(versions), versions)
	}

	// PR #1 is NOT the carrier (it's not last) and must come back untouched —
	// this is the crux of the bug: before the fix, PR #3 merging would have
	// stamped a changed comment_watermarks blob onto PR #1 too, making
	// Concourse rebuild its already-built "sha-1" commit for no reason.
	if versions[0].CommentBaseline || versions[0].CommentWatermarks != nil {
		t.Errorf("PR #1 version was perturbed by PR #3's merge: %+v", versions[0])
	}

	// PR #2 (last in the slice) is the one allowed to carry the updated
	// table forward, with PR #3 correctly pruned out of it.
	if !versions[1].CommentBaseline {
		t.Error("PR #2 (the carrier) should have CommentBaseline = true")
	}
	want := map[string]int64{"1": 100, "2": 200}
	if !maps.Equal(versions[1].CommentWatermarks, want) {
		t.Errorf("PR #2 CommentWatermarks = %v, want %v (PR #3 pruned)", versions[1].CommentWatermarks, want)
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
			PR:              strconv.Itoa(prNumber),
			Commit:          oldSHA,
			CommentID:       oldCommentID,
			CommentBaseline: true,
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

// TestCheck_CommentWatermarks_SurviveCursorMovingToADifferentPR is the core
// regression test for the single-cursor limitation itself: before
// CommentWatermarks, this resource could only remember a comment-trigger
// baseline for whichever PR happened to be request.Version.PR — the one
// version Concourse hands back as "latest." The moment a different PR
// became the cursor (e.g. it got a new commit), every other PR's
// watermark was gone for good; a later comment on it would look exactly
// like a PR that had never been checked before, re-establishing a fresh
// baseline instead of firing.
//
// Here, PR #20 is the cursor (request.Version.PR == "20"), but PR #5 still
// has an established watermark (id 100) in CommentWatermarks from an
// earlier check. PR #5 gets a new matching comment. It must fire — not
// get silently re-baselined — proving the watermark survived the cursor
// moving away from it.
func TestCheck_CommentWatermarks_SurviveCursorMovingToADifferentPR(t *testing.T) {
	const prA = 5
	const prASHA = "aaa111"
	const oldCommentID = 100
	const newCommentID = 200

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Only PR #5 is currently open/matching — PR #20 (the stale
		// cursor) is gone (closed/merged), so its watermark entry should
		// also be pruned from the output.
		_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(prA, prASHA))
	})
	// PR #5's changed files deliberately don't match source.paths, so it's
	// excluded from the commit-triggering set (filteredPRs). Without this,
	// an empty Paths config would make PR #5 trivially "match" on its own,
	// and the stamping that already happens for ordinary commit-matching
	// PRs would set CommentID regardless of whether the comment trigger
	// itself actually fired — masking exactly the bug this test exists to
	// catch. The only path left for a PR #5 version to appear is the
	// explicit "triggered" append inside applyCommentTriggers.
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
			CommentWatermarks: map[string]int64{
				"5":  oldCommentID, // PR A's watermark, NOT the cursor
				"20": 999,          // the stale cursor's own watermark
			},
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var triggered *models.Version
	for i := range versions {
		if versions[i].PR == strconv.Itoa(prA) && versions[i].CommentID == newCommentID {
			triggered = &versions[i]
		}
	}
	if triggered == nil {
		t.Fatalf("PR #%d's comment trigger did not fire even though its watermark was established — the single-cursor limitation is back: %+v", prA, versions)
	}
	if triggered.Commit != prASHA {
		t.Errorf("triggered version Commit = %s, want %s", triggered.Commit, prASHA)
	}

	// PR #20 is gone (not in the current open-PR list), so its entry must
	// be pruned rather than carried forward forever.
	if _, stillPresent := triggered.CommentWatermarks["20"]; stillPresent {
		t.Errorf("CommentWatermarks still has an entry for closed/gone PR #20, want it pruned: %+v", triggered.CommentWatermarks)
	}
	if got := triggered.CommentWatermarks["5"]; got != newCommentID {
		t.Errorf("CommentWatermarks[\"5\"] = %d, want %d (the fresh result, not the stale %d)", got, newCommentID, oldCommentID)
	}
}
