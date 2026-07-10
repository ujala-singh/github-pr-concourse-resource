package prlist

import (
	"context"
	"testing"

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

// Mock GitHub client for testing
type mockGithubClient struct {
	prs         []*models.PullRequest
	pathsMatch  bool
	getError    error
	filterError error
}

func (m *mockGithubClient) GetPullRequests(ctx context.Context) ([]*models.PullRequest, error) {
	return m.prs, m.getError
}

func (m *mockGithubClient) MatchesPathFilters(ctx context.Context, pr *models.PullRequest) (bool, error) {
	return m.pathsMatch, m.filterError
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
