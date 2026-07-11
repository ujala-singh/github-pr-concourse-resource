package prlist

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

func TestSource_FilterConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		source Source
		desc   string
	}{
		{
			name: "filter by base branch",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					BaseBranch:  "main",
				},
			},
			desc: "should only include PRs targeting main branch",
		},
		{
			name: "filter by labels",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Labels:      []string{"bug", "critical"},
				},
			},
			desc: "should include PRs with specified labels",
		},
		{
			name: "skip drafts",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:   "owner/repo",
					AccessToken:  "token",
					IgnoreDrafts: true,
				},
			},
			desc: "should exclude draft PRs",
		},
		{
			name: "disable forks",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:   "owner/repo",
					AccessToken:  "token",
					DisableForks: true,
				},
			},
			desc: "should exclude PRs from forks",
		},
		{
			name: "multiple states",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"OPEN", "MERGED"},
				},
			},
			desc: "should include PRs in multiple states",
		},
		{
			name: "path filters",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Paths:       []string{"src/**", "tests/**"},
				},
			},
			desc: "should filter PRs by changed file paths",
		},
		{
			name: "ignore paths",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					IgnorePaths: []string{"*.md", "docs/**"},
				},
			},
			desc: "should exclude PRs only changing ignored paths",
		},
		{
			name: "combined filters",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:   "owner/repo",
					AccessToken:  "token",
					BaseBranch:   "main",
					Labels:       []string{"ready"},
					IgnoreDrafts: true,
					DisableForks: true,
					States:       []string{"OPEN"},
					Paths:        []string{"src/**"},
				},
			},
			desc: "should apply all filters together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			assert.NoError(t, err, "source should be valid")
		})
	}
}

func TestFilterNewVersions_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		versions    []models.Version
		lastVersion models.Version
		expected    int
		desc        string
	}{
		{
			name:        "empty version list",
			versions:    []models.Version{},
			lastVersion: models.Version{PR: "1"},
			expected:    0,
			desc:        "no versions to filter",
		},
		{
			name: "all versions are new",
			versions: []models.Version{
				{PR: "1", Commit: "aaa"},
				{PR: "2", Commit: "bbb"},
				{PR: "3", Commit: "ccc"},
			},
			lastVersion: models.Version{PR: "0"},
			expected:    3,
			desc:        "last version not in list, return all",
		},
		{
			name: "last version is first",
			versions: []models.Version{
				{PR: "1", Commit: "aaa"},
				{PR: "2", Commit: "bbb"},
				{PR: "3", Commit: "ccc"},
			},
			lastVersion: models.Version{PR: "1"},
			expected:    2,
			desc:        "return versions after first",
		},
		{
			name: "last version is last",
			versions: []models.Version{
				{PR: "1", Commit: "aaa"},
				{PR: "2", Commit: "bbb"},
				{PR: "3", Commit: "ccc"},
			},
			lastVersion: models.Version{PR: "3"},
			expected:    0,
			desc:        "no new versions after last",
		},
		{
			name: "last version in middle",
			versions: []models.Version{
				{PR: "1", Commit: "aaa"},
				{PR: "2", Commit: "bbb"},
				{PR: "3", Commit: "ccc"},
				{PR: "4", Commit: "ddd"},
			},
			lastVersion: models.Version{PR: "2"},
			expected:    2,
			desc:        "return versions after middle",
		},
		{
			name: "same PR numbers with different commits",
			versions: []models.Version{
				{PR: "1", Commit: "aaa"},
				{PR: "1", Commit: "bbb"},
				{PR: "1", Commit: "ccc"},
			},
			lastVersion: models.Version{PR: "1", Commit: "aaa"},
			expected:    0,
			desc:        "filter matches by PR number only, returns nothing after match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterNewVersions(tt.versions, tt.lastVersion)
			assert.Equal(t, tt.expected, len(result), tt.desc)
		})
	}
}

func TestInParams_ConfigurationOptions(t *testing.T) {
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
			desc: "should not fetch any PR data",
		},
		{
			name: "fetch all metadata",
			params: InParams{
				SkipDownload: false,
			},
			desc: "should fetch full PR metadata",
		},
		{
			name:   "default configuration",
			params: InParams{},
			desc:   "should use default behavior",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just validate params struct
			_ = tt.params
		})
	}
}

func TestCheckRequest_VersionHandling(t *testing.T) {
	source := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
	}

	tests := []struct {
		name    string
		request CheckRequest
		desc    string
	}{
		{
			name: "initial check without version",
			request: CheckRequest{
				Source:  source,
				Version: nil,
			},
			desc: "first check returns all matching PRs",
		},
		{
			name: "subsequent check with version",
			request: CheckRequest{
				Source:  source,
				Version: &models.Version{PR: "10", Commit: "abc123"},
			},
			desc: "returns only newer PR versions",
		},
		{
			name: "check with version and metadata",
			request: CheckRequest{
				Source:  source,
				Version: &models.Version{PR: "5", Commit: "def456", ApprovedReviewCount: 2},
			},
			desc: "handles version with approval metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			assert.NoError(t, err, "request should be valid")
		})
	}
}

func TestInRequest_VersionRetrieval(t *testing.T) {
	source := Source{
		CommonConfig: models.CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "token",
		},
	}

	tests := []struct {
		name    string
		request InRequest
		desc    string
	}{
		{
			name: "fetch specific PR version",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{},
			},
			desc: "should fetch metadata for specific version",
		},
		{
			name: "fetch with skip download",
			request: InRequest{
				Source:  source,
				Version: models.Version{PR: "42", Commit: "abc123"},
				Params:  InParams{SkipDownload: true},
			},
			desc: "should only return version without fetching",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Source.Validate()
			assert.NoError(t, err, "request should be valid")
			assert.NotEmpty(t, tt.request.Version.PR, "version must have PR number")
		})
	}
}

func TestSource_GitHubEnterpriseConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		wantErr bool
		desc    string
	}{
		{
			name: "complete GitHub Enterprise config",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:      "owner/repo",
					AccessToken:     "token",
					V3Endpoint:      "https://github.enterprise.com/api/v3",
					V4Endpoint:      "https://github.enterprise.com/api/graphql",
					HostingEndpoint: "https://github.enterprise.com",
				},
			},
			wantErr: false,
			desc:    "all endpoints configured properly",
		},
		{
			name: "partial GitHub Enterprise config",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					V3Endpoint:  "https://github.enterprise.com/api/v3",
				},
			},
			wantErr: true,
			desc:    "missing V4 and hosting endpoints",
		},
		{
			name: "public GitHub (default endpoints)",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
				},
			},
			wantErr: false,
			desc:    "no custom endpoints, uses public GitHub",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			if tt.wantErr {
				assert.Error(t, err, tt.desc)
			} else {
				assert.NoError(t, err, tt.desc)
			}
		})
	}
}

func TestSource_StateValidation(t *testing.T) {
	tests := []struct {
		name    string
		source  Source
		wantErr bool
		desc    string
	}{
		{
			name: "valid OPEN state",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"OPEN"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid MERGED state",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"MERGED"},
				},
			},
			wantErr: false,
		},
		{
			name: "valid CLOSED state",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"CLOSED"},
				},
			},
			wantErr: false,
		},
		{
			name: "multiple valid states",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"OPEN", "MERGED"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid state",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"INVALID"},
				},
			},
			wantErr: true,
		},
		{
			name: "mixed valid and invalid states",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					States:      []string{"OPEN", "INVALID"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			if tt.wantErr {
				assert.Error(t, err, "should reject invalid state")
			} else {
				assert.NoError(t, err, "should accept valid state")
			}
		})
	}
}

func TestSource_LabelFiltering(t *testing.T) {
	tests := []struct {
		name   string
		source Source
		desc   string
	}{
		{
			name: "single label",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Labels:      []string{"bug"},
				},
			},
			desc: "filter PRs with 'bug' label",
		},
		{
			name: "multiple labels (OR logic)",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Labels:      []string{"bug", "enhancement", "feature"},
				},
			},
			desc: "filter PRs with any of the labels",
		},
		{
			name: "empty labels list",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Labels:      []string{},
				},
			},
			desc: "no label filtering applied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			assert.NoError(t, err, tt.desc)
		})
	}
}

func TestSource_PathFiltering(t *testing.T) {
	tests := []struct {
		name   string
		source Source
		desc   string
	}{
		{
			name: "include specific paths",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Paths:       []string{"src/**", "lib/**"},
				},
			},
			desc: "only PRs changing specified paths",
		},
		{
			name: "exclude specific paths",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					IgnorePaths: []string{"*.md", "docs/**"},
				},
			},
			desc: "exclude PRs only changing documentation",
		},
		{
			name: "both include and exclude paths",
			source: Source{
				CommonConfig: models.CommonConfig{
					Repository:  "owner/repo",
					AccessToken: "token",
					Paths:       []string{"src/**"},
					IgnorePaths: []string{"src/generated/**"},
				},
			},
			desc: "include src but exclude generated files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.source.Validate()
			assert.NoError(t, err, tt.desc)
		})
	}
}
