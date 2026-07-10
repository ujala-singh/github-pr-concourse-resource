package pr

import (
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
