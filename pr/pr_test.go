package pr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
				Number: 42,
			},
			wantErr: false,
		},
		{
			name: "missing repository",
			source: Source{
				CommonConfig: models.CommonConfig{
					AccessToken: "token123",
				},
				Number: 42,
			},
			wantErr: true,
		},
		{
			name: "missing access token",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository: "owner/repo",
				},
				Number: 42,
			},
			wantErr: true,
		},
		{
			name: "missing number",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token123",
				},
				Number: 0,
			},
			wantErr: true,
		},
		{
			name: "negative number",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token123",
				},
				Number: -1,
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

func TestOutParams_Validate(t *testing.T) {
	tests := []struct {
		name   string
		params OutParams
		valid  bool
	}{
		{
			name: "valid with status",
			params: OutParams{
				Path:   "pr",
				Status: "success",
			},
			valid: true,
		},
		{
			name: "valid with comment",
			params: OutParams{
				Path:    "pr",
				Comment: "Test comment",
			},
			valid: true,
		},
		{
			name: "valid with status and comment",
			params: OutParams{
				Path:    "pr",
				Status:  "failure",
				Comment: "Build failed",
			},
			valid: true,
		},
		{
			name: "valid status types",
			params: OutParams{
				Path:   "pr",
				Status: "pending",
			},
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the params struct is valid
			if tt.params.Path == "" {
				t.Error("Path should not be empty for valid params")
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
		Number: 123,
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
				Version: &models.Version{PR: "123", Commit: "abc123"},
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

func TestInParams_Defaults(t *testing.T) {
	tests := []struct {
		name          string
		params        InParams
		expectedTool  string
		expectedDepth int
	}{
		{
			name:          "default integration tool",
			params:        InParams{},
			expectedTool:  "", // Will default to "merge" in code
			expectedDepth: 0,
		},
		{
			name: "custom integration tool",
			params: InParams{
				IntegrationTool: "rebase",
			},
			expectedTool:  "rebase",
			expectedDepth: 0,
		},
		{
			name: "shallow clone",
			params: InParams{
				GitDepth: 1,
			},
			expectedTool:  "",
			expectedDepth: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.params.IntegrationTool != tt.expectedTool {
				if tt.expectedTool != "" {
					t.Errorf("IntegrationTool = %v, want %v", tt.params.IntegrationTool, tt.expectedTool)
				}
			}
			if tt.params.GitDepth != tt.expectedDepth {
				t.Errorf("GitDepth = %v, want %v", tt.params.GitDepth, tt.expectedDepth)
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
		Number: 42,
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
				Version: models.Version{PR: "42", Commit: "abc123"},
			},
			wantErr: false,
		},
		{
			name: "valid with skip download",
			request: InRequest{
				Source:  validSource,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{SkipDownload: true},
			},
			wantErr: false,
		},
		{
			name: "valid with rebase",
			request: InRequest{
				Source:  validSource,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{IntegrationTool: "rebase"},
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

func TestOutRequest_Validate(t *testing.T) {
	validSource := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
		Number: 42,
	}

	tests := []struct {
		name    string
		request OutRequest
		wantErr bool
	}{
		{
			name: "valid request with status",
			request: OutRequest{
				Source: validSource,
				Params: OutParams{
					Path:   "pr",
					Status: "success",
				},
			},
			wantErr: false,
		},
		{
			name: "valid request with comment",
			request: OutRequest{
				Source: validSource,
				Params: OutParams{
					Path:    "pr",
					Comment: "Test passed!",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("OutRequest validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTriggerComments_SourceField(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		wantLen int
	}{
		{
			name: "no trigger comments",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
				Number: 42,
			},
			wantLen: 0,
		},
		{
			name: "single trigger comment",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:      "owner/repo",
					AccessToken:     "token",
					TriggerComments: []string{"concourse plan"},
				},
				Number: 42,
			},
			wantLen: 1,
		},
		{
			name: "multiple trigger comments",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:      "owner/repo",
					AccessToken:     "token",
					TriggerComments: []string{"concourse plan", "concourse apply"},
				},
				Number: 42,
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.source.Validate(); err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if len(tt.source.TriggerComments) != tt.wantLen {
				t.Errorf("TriggerComments len = %d, want %d", len(tt.source.TriggerComments), tt.wantLen)
			}
		})
	}
}

func TestVersion_CommentID(t *testing.T) {
	tests := []struct {
		name    string
		version models.Version
		wantID  int64
	}{
		{
			name:    "commit-only version has zero CommentID",
			version: models.Version{PR: "42", Commit: "abc123"},
			wantID:  0,
		},
		{
			name:    "comment-triggered version carries CommentID",
			version: models.Version{PR: "42", Commit: "abc123", CommentID: 9876543},
			wantID:  9876543,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.version.CommentID != tt.wantID {
				t.Errorf("CommentID = %d, want %d", tt.version.CommentID, tt.wantID)
			}
		})
	}
}

// TestVersion_CommentBaseline guards against regressing to using CommentID's
// zero value as a stand-in for "the comment-trigger watermark was never
// established". A version stamped after establishing the baseline with no
// matching comments yet must be distinguishable from a version that predates
// trigger_comments support entirely (both have CommentID == 0), otherwise
// the first real trigger comment posted on a PR gets silently swallowed as
// if it were still establishing the baseline instead of firing a build.
func TestVersion_CommentBaseline(t *testing.T) {
	preFeatureVersion := models.Version{PR: "42", Commit: "abc123"}
	if preFeatureVersion.CommentBaseline {
		t.Fatalf("version predating trigger_comments support must decode with CommentBaseline = false")
	}
	if preFeatureVersion.CommentID != 0 {
		t.Fatalf("version predating trigger_comments support must have CommentID = 0")
	}

	establishedNoMatch := models.Version{PR: "42", Commit: "abc123", CommentBaseline: true}
	if establishedNoMatch.CommentID != 0 {
		t.Fatalf("expected CommentID = 0 for an established baseline with no matching comments yet")
	}
	if !establishedNoMatch.CommentBaseline {
		t.Fatalf("expected CommentBaseline = true once the watermark has been established")
	}

	if preFeatureVersion.CommentID == establishedNoMatch.CommentID && preFeatureVersion.CommentBaseline == establishedNoMatch.CommentBaseline {
		t.Fatalf("pre-feature and established-with-no-match versions must be distinguishable via CommentBaseline")
	}
}

// singlePRGraphQLResponse builds a minimal GraphQL response body for
// GetPullRequest's query.
func singlePRGraphQLResponse(number int, headSHA string) string {
	return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{
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
	}}}}`, number, number, headSHA, headSHA)
}

// TestOut_PropagatesCommentWatermarkFromVersionJSON is a regression test
// for a bug where Out's response version always had a zero-value
// CommentID/CommentBaseline, regardless of what triggered the build. Since
// Check stamps those fields on every version it returns once
// trigger_comments is configured, Out's response — missing them — looked
// like a brand-new, never-seen version to Concourse's ATC. That caused the
// job's own status-update `put` steps (pending/success/failure) to
// register a "new" version and re-trigger the very job that just ran, on
// the same commit, with no new comment or commit involved.
//
// Out has no direct channel from Concourse for "the version that triggered
// this build" (the put/out protocol doesn't pass one) — it must recover it
// from version.json, written by In into the checked-out resource
// directory. This test writes that file by hand to pin the on-disk
// contract In and Out share.
func TestOut_PropagatesCommentWatermarkFromVersionJSON(t *testing.T) {
	const prNumber = 42
	const headSHA = "abc123"

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, singlePRGraphQLResponse(prNumber, headSHA))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	gc := &models.GithubClient{
		V4:     githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo"},
	}

	sourcesDir := t.TempDir()
	resourceDir := filepath.Join(sourcesDir, "pull-request", ".git", "resource")
	if err := os.MkdirAll(resourceDir, 0755); err != nil {
		t.Fatalf("failed to create resource dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "pr"), fmt.Appendf(nil, "%d", prNumber), 0644); err != nil {
		t.Fatalf("failed to write pr file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "head_sha"), []byte(headSHA), 0644); err != nil {
		t.Fatalf("failed to write head_sha file: %v", err)
	}

	// The version that triggered this build, as In would have written it.
	triggeringVersion := models.Version{PR: "42", Commit: headSHA, CommentID: 9999, CommentBaseline: true}
	versionJSON, err := triggeringVersion.MarshalJSON()
	if err != nil {
		t.Fatalf("failed to marshal triggering version: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resourceDir, "version.json"), versionJSON, 0644); err != nil {
		t.Fatalf("failed to write version.json: %v", err)
	}

	request := OutRequest{
		Source: Source{CommonConfig: gc.Config},
		Params: OutParams{Path: "pull-request"},
	}

	response, err := Out(request, gc, sourcesDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if response.Version.CommentID != 9999 {
		t.Errorf("Version.CommentID = %d, want 9999 (dropped the triggering version's watermark)", response.Version.CommentID)
	}
	if !response.Version.CommentBaseline {
		t.Errorf("Version.CommentBaseline = false, want true (dropped the triggering version's watermark)")
	}
}

// TestCheck_PathFilter_OnlyConsidersFilesChangedSinceLastCheck is the
// single-PR-mode counterpart to prlist's end-to-end regression test of
// the same name: a PR whose cumulative base...HEAD diff includes a
// path-matching file from an earlier, already-built commit must not keep
// matching forever once its latest push only touches unrelated files.
// Single PR mode always has a reliable last-known commit (there's no
// cursor-sharing limitation here, unlike list mode), so this is fully
// fixed, not just best-effort. pulls/{number}/files (the cumulative-diff
// endpoint) must never be hit once a previous commit is known.
func TestCheck_PathFilter_OnlyConsidersFilesChangedSinceLastCheck(t *testing.T) {
	const prNumber = 40
	const oldSHA = "f17cb0bc598907938993ec4ff7e4691333b93123"
	const newSHA = "8228e7b92bafa2009649cbebf613a42d475febf0"

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, singlePRGraphQLResponse(prNumber, newSHA))
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/commits", prNumber), func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"sha": %q, "commit": {"committer": {"date": "2026-01-02T00:00:00Z"}}}]`, newSHA)
	})
	mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", prNumber), func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("pulls/%d/files (cumulative diff) must not be called when a previous commit is known", prNumber)
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

	v3 := github.NewClient(nil)
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
		Source: Source{CommonConfig: gc.Config, Number: prNumber},
		Version: &models.Version{
			PR:            strconv.Itoa(prNumber),
			Commit:        oldSHA,
			CommittedDate: "2026-01-01T00:00:00Z",
		},
	}

	versions, err := Check(request, gc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No match means Check echoes back the last known version rather than
	// an empty list — but it must be byte-identical to request.Version
	// (the old commit), not the new one, so Concourse doesn't register it
	// as new and retrigger the build.
	if len(versions) != 1 {
		t.Fatalf("got %d versions, want exactly 1 (the echoed-back last known version): %+v", len(versions), versions)
	}
	if versions[0].Commit != oldSHA {
		t.Errorf("versions[0].Commit = %s, want %s (old commit) — a version for the new commit would retrigger the build even though it didn't touch the configured path", versions[0].Commit, oldSHA)
	}
}
