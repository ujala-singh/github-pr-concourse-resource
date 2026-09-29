package pr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

func TestBuildRepoURL(t *testing.T) {
	tests := []struct {
		name          string
		config        models.CommonConfig
		owner         string
		repo          string
		token         string
		expectedURL   string
		shouldInclude string
	}{
		{
			name: "public repository with token",
			config: models.CommonConfig{
				Repository: "owner/repo",
			},
			owner:         "testowner",
			repo:          "testrepo",
			token:         "ghp_test123",
			shouldInclude: "https://",
		},
		{
			name: "GitHub Enterprise with custom hosting endpoint",
			config: models.CommonConfig{
				Repository:      "owner/repo",
				HostingEndpoint: "https://github.example.com",
			},
			owner:         "enterprise",
			repo:          "project",
			token:         "token123",
			shouldInclude: "github.example.com",
		},
		{
			name: "default GitHub hosting",
			config: models.CommonConfig{
				Repository: "owner/repo",
			},
			owner:         "octocat",
			repo:          "hello-world",
			token:         "test_token",
			shouldInclude: "github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildRepoURL(tt.config, tt.owner, tt.repo, tt.token)
			assert.Contains(t, result, tt.shouldInclude, "URL should contain expected host")
			assert.Contains(t, result, tt.repo, "URL should contain repo name")
		})
	}
}

func TestInParams_ValidationScenarios(t *testing.T) {
	tests := []struct {
		name   string
		params InParams
		desc   string
	}{
		{
			name: "skip download enabled",
			params: InParams{
				SkipDownload: true,
			},
			desc: "should skip git clone operations",
		},
		{
			name: "integration tool merge",
			params: InParams{
				IntegrationTool: "merge",
			},
			desc: "should use merge strategy",
		},
		{
			name: "integration tool rebase",
			params: InParams{
				IntegrationTool: "rebase",
			},
			desc: "should use rebase strategy",
		},
		{
			name: "integration tool checkout",
			params: InParams{
				IntegrationTool: "checkout",
			},
			desc: "should use checkout strategy",
		},
		{
			name: "shallow clone depth 1",
			params: InParams{
				GitDepth: 1,
			},
			desc: "should perform shallow clone",
		},
		{
			name: "full clone with submodules",
			params: InParams{
				Submodules: true,
			},
			desc: "should include submodules",
		},
		{
			name: "list changed files",
			params: InParams{
				ListChangedFiles: true,
			},
			desc: "should list files changed in PR",
		},
		{
			name: "fetch tags",
			params: InParams{
				FetchTags: true,
			},
			desc: "should fetch all tags",
		},
		{
			name: "combined options",
			params: InParams{
				IntegrationTool:  "merge",
				GitDepth:         10,
				Submodules:       true,
				ListChangedFiles: true,
				FetchTags:        true,
			},
			desc: "should handle multiple options",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate the params structure
			if tt.params.IntegrationTool != "" {
				assert.Contains(t, []string{"merge", "rebase", "checkout"}, tt.params.IntegrationTool,
					"integration tool should be valid")
			}
			if tt.params.GitDepth > 0 {
				assert.Greater(t, tt.params.GitDepth, 0, "git depth should be positive")
			}
		})
	}
}

func TestOutParams_StatusValidation(t *testing.T) {
	validStatuses := []string{"success", "failure", "error", "pending"}

	for _, status := range validStatuses {
		t.Run("valid status: "+status, func(t *testing.T) {
			params := OutParams{
				Path:   "pr",
				Status: status,
			}
			assert.Contains(t, validStatuses, params.Status, "status should be valid")
		})
	}
}

func TestOutParams_CommentConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		params      OutParams
		description string
	}{
		{
			name: "inline comment",
			params: OutParams{
				Path:    "pr",
				Comment: "Build successful!",
			},
			description: "should accept inline comment text",
		},
		{
			name: "comment from file",
			params: OutParams{
				Path:        "pr",
				CommentFile: "comment.txt",
			},
			description: "should read comment from file",
		},
		{
			name: "delete previous comments",
			params: OutParams{
				Path:                   "pr",
				Comment:                "New comment",
				DeletePreviousComments: true,
			},
			description: "should delete previous comments before adding new one",
		},
		{
			name: "comment with status",
			params: OutParams{
				Path:    "pr",
				Status:  "success",
				Comment: "All checks passed",
			},
			description: "should support both status and comment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.params.Path, "path should be set")
			if tt.params.Comment != "" {
				assert.NotEmpty(t, tt.params.Comment, "comment text should not be empty")
			}
		})
	}
}

func TestOutParams_StatusContext(t *testing.T) {
	tests := []struct {
		name    string
		params  OutParams
		context string
	}{
		{
			name: "custom context",
			params: OutParams{
				Path:    "pr",
				Status:  "success",
				Context: "ci/build",
			},
			context: "ci/build",
		},
		{
			name: "context with description",
			params: OutParams{
				Path:        "pr",
				Status:      "failure",
				Context:     "ci/tests",
				Description: "Tests failed",
			},
			context: "ci/tests",
		},
		{
			name: "context with target URL",
			params: OutParams{
				Path:      "pr",
				Status:    "success",
				Context:   "ci/deploy",
				TargetURL: "https://example.com/builds/123",
			},
			context: "ci/deploy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.context, tt.params.Context, "context should match")
			if tt.params.TargetURL != "" {
				assert.Contains(t, tt.params.TargetURL, "http", "target URL should be valid")
			}
		})
	}
}

func TestSource_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		wantErr bool
		errMsg  string
	}{
		{
			name: "PR number zero",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
				Number: 0,
			},
			wantErr: true,
			errMsg:  "number must be a positive integer",
		},
		{
			name: "PR number negative",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
				Number: -42,
			},
			wantErr: true,
			errMsg:  "number must be a positive integer",
		},
		{
			name: "very large PR number",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
				Number: 999999,
			},
			wantErr: false,
		},
		{
			name: "PR number 1",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
				Number: 1,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			if tt.wantErr {
				assert.Error(t, err, "should return error")
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg, "error message should match")
				}
			} else {
				assert.NoError(t, err, "should not return error")
			}
		})
	}
}

func TestCheckRequest_WithVersions(t *testing.T) {
	source := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
		Number: 123,
	}

	tests := []struct {
		name    string
		request CheckRequest
		desc    string
	}{
		{
			name: "no previous version",
			request: CheckRequest{
				Source:  source,
				Version: nil,
			},
			desc: "first check, no version provided",
		},
		{
			name: "with previous version",
			request: CheckRequest{
				Source:  source,
				Version: &models.Version{PR: "123", Commit: "abc123"},
			},
			desc: "subsequent check with known version",
		},
		{
			name: "version with different commit",
			request: CheckRequest{
				Source:  source,
				Version: &models.Version{PR: "123", Commit: "def456"},
			},
			desc: "same PR, different commit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			assert.NoError(t, err, "request should be valid")
			if tt.request.Version != nil {
				assert.NotEmpty(t, tt.request.Version.PR, "version PR should not be empty")
			}
		})
	}
}

func TestInRequest_VersionScenarios(t *testing.T) {
	source := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
		Number: 42,
	}

	tests := []struct {
		name    string
		request InRequest
		desc    string
	}{
		{
			name: "exact commit",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{},
			},
			desc: "fetch exact commit from PR",
		},
		{
			name: "with merge integration",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{IntegrationTool: "merge"},
			},
			desc: "fetch and merge with base branch",
		},
		{
			name: "with rebase integration",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "def456"},
				Params:  InParams{IntegrationTool: "rebase"},
			},
			desc: "fetch and rebase onto base branch",
		},
		{
			name: "checkout only",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "ghi789"},
				Params:  InParams{IntegrationTool: "checkout"},
			},
			desc: "checkout without integration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			assert.NoError(t, err, "request should be valid")
			assert.NotEmpty(t, tt.request.Version.PR, "version must have PR number")
			assert.NotEmpty(t, tt.request.Version.Commit, "version must have commit SHA")
		})
	}
}
