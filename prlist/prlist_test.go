package prlist

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v60/github"
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

func TestFilterNewVersions(t *testing.T) {
	tests := []struct {
		name        string
		versions    []models.Version
		lastVersion models.Version
		want        int // expected count
	}{
		{
			name: "no new versions",
			versions: []models.Version{
				{PR: "1"},
				{PR: "2"},
			},
			lastVersion: models.Version{PR: "2"},
			want:        0,
		},
		{
			name: "has new versions",
			versions: []models.Version{
				{PR: "1"},
				{PR: "2"},
				{PR: "3"},
			},
			lastVersion: models.Version{PR: "1"},
			want:        2,
		},
		{
			name: "last version not found",
			versions: []models.Version{
				{PR: "1"},
				{PR: "2"},
			},
			lastVersion: models.Version{PR: "99"},
			want:        2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterNewVersions(tt.versions, tt.lastVersion)
			if len(result) != tt.want {
				t.Errorf("filterNewVersions() returned %d versions, want %d", len(result), tt.want)
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

func TestApplyCommentTriggers_FirstObservation_EstablishesBaselineWithoutFiring(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id": 555, "body": "concourse plan"}]`)
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
		fmt.Fprint(w, `[{"id": 100, "body": "concourse plan"}, {"id": 555, "body": "concourse plan"}]`)
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
		fmt.Fprint(w, `[{"id": 555, "body": "concourse plan"}]`)
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
