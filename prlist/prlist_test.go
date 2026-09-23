package prlist

import (
	"context"
	"fmt"
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

	v3 := github.NewClient(nil)
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
// checkConcurrency (forcing multiple batches) and n == 0 (no-op).
func TestRunBounded_CallsEveryIndexExactlyOnce(t *testing.T) {
	for _, n := range []int{0, 1, checkConcurrency, checkConcurrency*3 + 1} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			seen := make([]int32, n)
			runBounded(n, func(i int) {
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
// checkConcurrency goroutines run fn at the same time.
func TestRunBounded_RespectsConcurrencyLimit(t *testing.T) {
	var (
		mu        sync.Mutex
		current   int
		maxSeen   int
		callCount int
	)

	n := checkConcurrency * 4
	runBounded(n, func(i int) {
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
	if maxSeen > checkConcurrency {
		t.Errorf("observed %d concurrent calls, want at most %d", maxSeen, checkConcurrency)
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

	v3 := github.NewClient(nil)
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

	filtered, err := filterPRsByPath(context.Background(), gc, prs)
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

	_, err := filterPRsByPath(context.Background(), gc, prs)
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

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil)
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

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil)
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

	versions, err := applyCommentTriggers(context.Background(), request, gc, []*models.PullRequest{pr}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected no triggered version when the cursor is on a different PR, got %d: %+v", len(versions), versions)
	}
}

// TestApplyCommentTriggers_ManyPRsConcurrently scans more PRs than
// checkConcurrency at once (forcing multiple bounded worker batches) and
// verifies two things despite goroutines completing out of order:
//  1. Every PR's watermark gets stamped onto its pre-existing version
//     correctly (the concurrent GitHub calls all land in the right slot).
//  2. Exactly one triggered version is appended — for the single PR whose
//     number matches the cursor (request.Version.PR) — since list mode can
//     only track one PR's baseline at a time (see applyCommentTriggers'
//     doc comment). A PR with a genuinely new comment but no established
//     baseline must NOT trigger, concurrency or not.
func TestApplyCommentTriggers_ManyPRsConcurrently(t *testing.T) {
	const n = checkConcurrency*2 + 3 // force multiple bounded batches
	const cursorPR = 5               // arbitrary PR whose baseline is established

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
	v3 := github.NewClient(nil)
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

	versions, err := applyCommentTriggers(context.Background(), request, gc, prs, preExisting)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Every pre-existing version must be stamped with its own PR's latest
	// comment id, regardless of whether that PR could actually trigger.
	for i := range n {
		number := i + 1
		v := versions[i]
		if v.PR != strconv.Itoa(number) {
			t.Fatalf("versions[%d].PR = %s, want %d (order not preserved)", i, v.PR, number)
		}
		if !v.CommentBaseline {
			t.Errorf("PR #%d: CommentBaseline = false, want true after stamping", number)
		}
		if v.CommentID != int64(1000+number) {
			t.Errorf("PR #%d: CommentID = %d, want %d", number, v.CommentID, 1000+number)
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
