package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v60/github"
)

func newTestGithubClientForPaths(t *testing.T, mux *http.ServeMux, config CommonConfig) *GithubClient {
	t.Helper()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("failed to parse test server URL: %v", err)
	}

	v3 := github.NewClient(nil)
	v3.BaseURL = baseURL

	config.Repository = "owner/repo"
	return &GithubClient{V3: v3, Config: config}
}

func TestFilesMatchPathFilters(t *testing.T) {
	tests := []struct {
		name   string
		config CommonConfig
		files  []string
		want   bool
	}{
		{
			name:   "no filters configured matches anything",
			config: CommonConfig{},
			files:  []string{"anything.txt"},
			want:   true,
		},
		{
			name:   "no filters configured matches even an empty file list",
			config: CommonConfig{},
			files:  nil,
			want:   true,
		},
		{
			name:   "include path matches",
			config: CommonConfig{Paths: []string{"terraform/**"}},
			files:  []string{"terraform/main.tf", "README.md"},
			want:   true,
		},
		{
			name:   "include path does not match",
			config: CommonConfig{Paths: []string{"terraform/**"}},
			files:  []string{"pipelines/foo.yaml", "README.md"},
			want:   false,
		},
		{
			name:   "include path with empty file list never matches",
			config: CommonConfig{Paths: []string{"terraform/**"}},
			files:  nil,
			want:   false,
		},
		{
			// Pre-existing (unchanged by this fix) quirk: when every
			// changed file is ignored and no include filter is set, the
			// function falls through to its final "return true" rather
			// than treating "nothing relevant changed" as false. Asserted
			// here so a future refactor doesn't silently change it; not
			// something this test suite is trying to fix.
			name:   "ignore path excludes the only changed file, no include filter: matches anyway",
			config: CommonConfig{IgnorePaths: []string{"**/*.md"}},
			files:  []string{"README.md"},
			want:   true,
		},
		{
			name:   "ignore path leaves a non-ignored file, no include filter",
			config: CommonConfig{IgnorePaths: []string{"**/*.md"}},
			files:  []string{"README.md", "main.go"},
			want:   true,
		},
		{
			name: "ignore path leaves a non-ignored file that also matches include",
			config: CommonConfig{
				Paths:       []string{"terraform/**"},
				IgnorePaths: []string{"**/*.md"},
			},
			files: []string{"README.md", "terraform/main.tf"},
			want:  true,
		},
		{
			name: "ignore path leaves a non-ignored file that does NOT match include",
			config: CommonConfig{
				Paths:       []string{"terraform/**"},
				IgnorePaths: []string{"**/*.md"},
			},
			files: []string{"README.md", "pipelines/foo.yaml"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filesMatchPathFilters(tt.config, tt.files)
			if got != tt.want {
				t.Errorf("filesMatchPathFilters() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMatchesPathFilters_FullDiffFallback_RegressionBug reproduces the
// exact reported bug: a PR's cumulative base...HEAD diff (what
// GetChangedFiles / GitHub's pulls/{number}/files endpoint always
// returns) includes a path-matching file from an EARLIER commit, even
// though the LATEST commit touched only unrelated files. Without a
// sinceSHA, MatchesPathFilters has no choice but to fall back to that
// full diff — matching real behavior for a PR with no previously-known
// commit (e.g. appearing for the first time).
func TestMatchesPathFilters_FullDiffFallback_RegressionBug(t *testing.T) {
	const prNumber = 40

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prNumber), func(w http.ResponseWriter, r *http.Request) {
		// The PR's cumulative diff: an earlier commit touched a terraform
		// file; the latest commit (not reflected here at all) only
		// touched a pipeline YAML file.
		_, _ = fmt.Fprint(w, `[
			{"filename": "concourse-demo-setup/pipelines/GCP/teleport-ssh-agent.yaml"},
			{"filename": "concourse-demo-setup/terraform/GCP/03-teleport-ssh-agent/main.tf"}
		]`)
	})
	gc := newTestGithubClientForPaths(t, mux, CommonConfig{Paths: []string{"concourse-demo-setup/terraform/**"}})

	pr := &PullRequest{Number: prNumber, HeadRefOID: "8228e7b92bafa2009649cbebf613a42d475febf0"}

	matches, err := gc.MatchesPathFilters(context.Background(), pr, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matches {
		t.Error("expected the full-diff fallback to match (no sinceSHA available) — this documents the fallback, not a bug")
	}
}

// TestMatchesPathFilters_SinceSHA_FixesTheRegressionBug is the fix for the
// same scenario: once a previously-known commit (sinceSHA) is available,
// MatchesPathFilters diffs only the files changed since that commit —
// which, for the latest push, don't touch the configured path — instead
// of the PR's full history. It must NOT hit the pulls/{number}/files
// endpoint at all once sinceSHA is given.
func TestMatchesPathFilters_SinceSHA_FixesTheRegressionBug(t *testing.T) {
	const prNumber = 40
	const oldSHA = "f17cb0bc598907938993ec4ff7e4691333b93123"
	const newSHA = "8228e7b92bafa2009649cbebf613a42d475febf0"

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prNumber), func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("pulls/%d/files must not be called when sinceSHA is set", prNumber)
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/%s...%s", oldSHA, newSHA), func(w http.ResponseWriter, r *http.Request) {
		// Only the latest commit's own change: the pipeline YAML file,
		// nothing under concourse-demo-setup/terraform/**.
		_, _ = fmt.Fprint(w, `{"files": [
			{"filename": "concourse-demo-setup/pipelines/GCP/teleport-ssh-agent.yaml"}
		]}`)
	})
	gc := newTestGithubClientForPaths(t, mux, CommonConfig{Paths: []string{"concourse-demo-setup/terraform/**"}})

	pr := &PullRequest{Number: prNumber, HeadRefOID: newSHA}

	matches, err := gc.MatchesPathFilters(context.Background(), pr, oldSHA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matches {
		t.Error("expected no match: the latest push only touched an unrelated pipeline file — the bug is back")
	}
}

// TestMatchesPathFilters_NoChangeSinceLastCheck_NoAPICall verifies that
// when sinceSHA already equals the PR's current HEAD (nothing pushed
// since the last check), MatchesPathFilters returns false without making
// any GitHub API call at all.
func TestMatchesPathFilters_NoChangeSinceLastCheck_NoAPICall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no API call should be made when sinceSHA == pr.HeadRefOID, got request for %s", r.URL.Path)
	})
	gc := newTestGithubClientForPaths(t, mux, CommonConfig{Paths: []string{"terraform/**"}})

	const sha = "abc123"
	pr := &PullRequest{Number: 1, HeadRefOID: sha}

	matches, err := gc.MatchesPathFilters(context.Background(), pr, sha)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matches {
		t.Error("expected no match when nothing has changed since the last check")
	}
}

// TestMatchesPathFilters_NoFiltersConfigured_SkipsAPICallEntirely verifies
// the early-return when source.paths/ignore_paths are both unset — no
// GitHub call should happen regardless of sinceSHA.
func TestMatchesPathFilters_NoFiltersConfigured_SkipsAPICallEntirely(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("no API call should be made when no path filters are configured, got request for %s", r.URL.Path)
	})
	gc := newTestGithubClientForPaths(t, mux, CommonConfig{})

	pr := &PullRequest{Number: 1, HeadRefOID: "abc123"}

	matches, err := gc.MatchesPathFilters(context.Background(), pr, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matches {
		t.Error("expected a match when no path filters are configured")
	}
}

func TestGetChangedFilesSince_Pagination(t *testing.T) {
	const oldSHA = "aaa"
	const newSHA = "bbb"
	callCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/%s...%s", oldSHA, newSHA), func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, `{"files": [{"filename": "page2.txt"}]}`)
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/owner/repo/compare/%s...%s?page=2>; rel="next"`, r.Host, oldSHA, newSHA))
		_, _ = fmt.Fprint(w, `{"files": [{"filename": "page1.txt"}]}`)
	})
	gc := newTestGithubClientForPaths(t, mux, CommonConfig{})

	files, err := gc.GetChangedFilesSince(context.Background(), oldSHA, newSHA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 paginated requests, got %d", callCount)
	}
	want := []string{"page1.txt", "page2.txt"}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(files), len(want), files)
	}
	for i, f := range want {
		if files[i] != f {
			t.Errorf("files[%d] = %s, want %s", i, files[i], f)
		}
	}
}
