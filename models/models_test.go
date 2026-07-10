package models

import (
	"testing"
)

func TestCommonConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  CommonConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
			},
			wantErr: false,
		},
		{
			name: "missing repository",
			config: CommonConfig{
				AccessToken: "token123",
			},
			wantErr: true,
		},
		{
			name: "missing access token",
			config: CommonConfig{
				Repository: "owner/repo",
			},
			wantErr: true,
		},
		{
			name: "invalid repository format",
			config: CommonConfig{
				Repository:  "invalid",
				AccessToken: "token123",
			},
			wantErr: true,
		},
		{
			name: "partial endpoint configuration",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				V3Endpoint:  "https://api.github.com",
			},
			wantErr: true,
		},
		{
			name: "complete endpoint configuration",
			config: CommonConfig{
				Repository:      "owner/repo",
				AccessToken:     "token123",
				V3Endpoint:      "https://api.github.com",
				V4Endpoint:      "https://api.github.com/graphql",
				HostingEndpoint: "https://github.com",
			},
			wantErr: false,
		},
		{
			name: "invalid state",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				States:      []string{"INVALID"},
			},
			wantErr: true,
		},
		{
			name: "valid states",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				States:      []string{"OPEN", "MERGED", "CLOSED"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("CommonConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCommonConfig_GetOwnerAndRepo(t *testing.T) {
	config := CommonConfig{
		Repository: "myorg/myrepo",
	}

	owner, repo := config.GetOwnerAndRepo()

	if owner != "myorg" {
		t.Errorf("Expected owner to be 'myorg', got '%s'", owner)
	}

	if repo != "myrepo" {
		t.Errorf("Expected repo to be 'myrepo', got '%s'", repo)
	}
}
