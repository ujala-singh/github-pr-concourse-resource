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

func TestPathMatches(t *testing.T) {
	cases := []struct {
		pattern string
		file    string
		want    bool
	}{
		// ** recursive glob
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/terraform/US-EAST-1/EKS-Resources/main.tf", true},
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/terraform/AZURE/networking/main.tf", true},
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/helm/values.yaml", false},
		{"**/*.tf", "a/b/c/main.tf", true},
		{"**/*.tf", "main.tf", true},
		{"**/*.tf", "main.yaml", false},
		// single * does not cross path separators
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		// plain prefix matching (no glob chars)
		{"concourse-demo-setup/terraform", "concourse-demo-setup/terraform/US-EAST-1/main.tf", true},
		{"concourse-demo-setup/terraform", "concourse-demo-setup/helm/values.yaml", false},
		// exact match
		{"Makefile", "Makefile", true},
		{"Makefile", "src/Makefile", false},
	}
	for _, c := range cases {
		got := pathMatches(c.pattern, c.file)
		if got != c.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}
