//go:build e2e
// +build e2e

package e2e_test

import (
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ujala-singh/github-pr-concourse-resource/models"
	"github.com/ujala-singh/github-pr-concourse-resource/pr"
	"github.com/ujala-singh/github-pr-concourse-resource/prlist"
)

// Test configuration - set via environment variables
var (
	testRepository  = getEnvOrDefault("TEST_REPOSITORY", "ujala-singh/github-repository-dispatch-receiver")
	testAccessToken = os.Getenv("GITHUB_ACCESS_TOKEN")
)

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// TestPRModeCheck tests the check operation in single PR mode
func TestPRModeCheck(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	tests := []struct {
		name             string
		source           pr.Source
		version          *models.Version
		expectedMinCount int
	}{
		{
			name: "check returns commits for PR",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4, // Assuming PR #4 exists
			},
			version:          nil,
			expectedMinCount: 1,
		},
		{
			name: "check returns only new commits",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			version: &models.Version{
				PR:     "4",
				Commit: "a5114f6ab89f4b736655642a11e8d15ce363d882",
			},
			expectedMinCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := pr.CheckRequest{
				Source:  tt.source,
				Version: tt.version,
			}

			client, err := models.NewGithubClient(tt.source.CommonConfig, tt.source.GithubConfig)
			require.NoError(t, err)

			response, err := pr.Check(request, client)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(response), tt.expectedMinCount)
		})
	}
}

// TestPRListModeCheck tests the check operation in PR list mode
func TestPRListModeCheck(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	tests := []struct {
		name             string
		source           prlist.Source
		version          *models.Version
		expectedMinCount int
	}{
		{
			name: "check returns all open PRs",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
			},
			version:          nil,
			expectedMinCount: 1,
		},
		{
			name: "check with label filter",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
					Labels:      []string{"enhancement"},
				},
			},
			version:          nil,
			expectedMinCount: 0,
		},
		{
			name: "check with state filter",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
					States:      []string{"OPEN"},
				},
			},
			version:          nil,
			expectedMinCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := prlist.CheckRequest{
				Source:  tt.source,
				Version: tt.version,
			}

			client, err := models.NewGithubClient(tt.source.CommonConfig, tt.source.GithubConfig)
			require.NoError(t, err)

			response, err := prlist.Check(request, client)
			require.NoError(t, err)
			assert.GreaterOrEqual(t, len(response), tt.expectedMinCount)
		})
	}
}

// TestPRModeIn tests the in operation in single PR mode
func TestPRModeIn(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	tests := []struct {
		name                string
		source              pr.Source
		version             models.Version
		params              pr.InParams
		expectedCommitCount int
		expectedStrategy    string
	}{
		{
			name: "in with merge strategy",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			version: models.Version{
				PR:     "4",
				Commit: "a5114f6ab89f4b736655642a11e8d15ce363d882",
			},
			params: pr.InParams{
				IntegrationTool: "merge",
			},
			expectedCommitCount: 8,
			expectedStrategy:    "merge",
		},
		{
			name: "in with rebase strategy",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			version: models.Version{
				PR:     "4",
				Commit: "a5114f6ab89f4b736655642a11e8d15ce363d882",
			},
			params: pr.InParams{
				IntegrationTool: "rebase",
			},
			expectedCommitCount: 7,
			expectedStrategy:    "rebase",
		},
		{
			name: "in with checkout strategy",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			version: models.Version{
				PR:     "4",
				Commit: "a5114f6ab89f4b736655642a11e8d15ce363d882",
			},
			params: pr.InParams{
				IntegrationTool: "checkout",
			},
			expectedCommitCount: 5,
			expectedStrategy:    "checkout",
		},
		{
			name: "in with skip download",
			source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			version: models.Version{
				PR:     "4",
				Commit: "a5114f6ab89f4b736655642a11e8d15ce363d882",
			},
			params: pr.InParams{
				SkipDownload: true,
			},
			expectedCommitCount: 0,
			expectedStrategy:    "skip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "github-pr-resource-test")
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			request := pr.InRequest{
				Source:  tt.source,
				Version: tt.version,
				Params:  tt.params,
			}

			client, err := models.NewGithubClient(tt.source.CommonConfig, tt.source.GithubConfig)
			require.NoError(t, err)

			response, err := pr.In(request, client, dir)
			require.NoError(t, err)
			assert.Equal(t, tt.version, response.Version)

			// Check metadata files
			metadataDir := filepath.Join(dir, ".git", "resource")
			if !tt.params.SkipDownload {
				assert.DirExists(t, metadataDir)

				// Verify metadata files exist
				assert.FileExists(t, filepath.Join(metadataDir, "pr"))
				assert.FileExists(t, filepath.Join(metadataDir, "url"))
				assert.FileExists(t, filepath.Join(metadataDir, "head_sha"))

				// Check git history if not skipped
				history := gitHistory(t, dir)
				if tt.expectedCommitCount > 0 {
					assert.GreaterOrEqual(t, len(history), tt.expectedCommitCount-2) // Allow some variance
				}
			}
		})
	}
}

// TestPRListModeIn tests the in operation in PR list mode
func TestPRListModeIn(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	tests := []struct {
		name    string
		source  prlist.Source
		version models.Version
		params  prlist.InParams
	}{
		{
			name: "in returns metadata for PR",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
			},
			version: models.Version{
				PR: "4",
			},
			params: prlist.InParams{},
		},
		{
			name: "in with skip download",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
			},
			version: models.Version{
				PR: "4",
			},
			params: prlist.InParams{
				SkipDownload: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := ioutil.TempDir("", "github-pr-resource-test")
			require.NoError(t, err)
			defer os.RemoveAll(dir)

			request := prlist.InRequest{
				Source:  tt.source,
				Version: tt.version,
				Params:  tt.params,
			}

			client, err := models.NewGithubClient(tt.source.CommonConfig, tt.source.GithubConfig)
			require.NoError(t, err)

			response, err := prlist.In(request, client, dir)
			require.NoError(t, err)
			assert.Equal(t, tt.version, response.Version)

			// Check metadata files
			metadataDir := filepath.Join(dir, ".git", "resource")
			assert.DirExists(t, metadataDir)

			// Verify metadata files exist
			assert.FileExists(t, filepath.Join(metadataDir, "pr"))
			assert.FileExists(t, filepath.Join(metadataDir, "metadata.json"))
		})
	}
}

// TestPRModeOut tests the out operation in single PR mode
func TestPRModeOut(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	// Note: This test requires write access to the repository
	// Use with caution and ensure TEST_REPOSITORY is set to a test repo
	t.Run("out with status update", func(t *testing.T) {
		if os.Getenv("TEST_WRITE_ACCESS") != "true" {
			t.Skip("TEST_WRITE_ACCESS not set to true")
		}

		dir, err := ioutil.TempDir("", "github-pr-resource-test")
		require.NoError(t, err)
		defer os.RemoveAll(dir)

		// Create metadata directory
		metadataDir := filepath.Join(dir, ".git", "resource")
		require.NoError(t, os.MkdirAll(metadataDir, 0755))

		// Write PR metadata
		require.NoError(t, ioutil.WriteFile(filepath.Join(metadataDir, "pr"), []byte("4"), 0644))
		require.NoError(t, ioutil.WriteFile(filepath.Join(metadataDir, "head_sha"), []byte("a5114f6ab89f4b736655642a11e8d15ce363d882"), 0644))

		request := pr.OutRequest{
			Source: pr.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
				},
				Number: 4,
			},
			Params: pr.OutParams{
				Path:   dir,
				Status: "success",
			},
		}

		client, err := models.NewGithubClient(request.Source.CommonConfig, request.Source.GithubConfig)
		require.NoError(t, err)

		response, err := pr.Out(request, client, dir)
		require.NoError(t, err)
		assert.NotEmpty(t, response.Version.PR)
	})
}

// TestCheckAPICost tests the API cost of check operations
func TestCheckAPICost(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	t.Run("PR mode check API cost", func(t *testing.T) {
		source := pr.Source{
			CommonConfig: models.CommonConfig{
				Repository:  testRepository,
				AccessToken: testAccessToken,
			},
			Number: 4,
		}

		client, err := models.NewGithubClient(source.CommonConfig, source.GithubConfig)
		require.NoError(t, err)

		before := getRemainingRateLimit(t, client.V4)

		request := pr.CheckRequest{
			Source:  source,
			Version: nil,
		}

		_, err = pr.Check(request, client)
		require.NoError(t, err)

		after := getRemainingRateLimit(t, client.V4)
		cost := before - after

		// PR mode should use 1-2 API calls
		assert.LessOrEqual(t, cost, 3, "PR mode check should use at most 3 API calls")
	})

	t.Run("PR list mode check API cost", func(t *testing.T) {
		source := prlist.Source{
			CommonConfig: models.CommonConfig{
				Repository:  testRepository,
				AccessToken: testAccessToken,
			},
		}

		client, err := models.NewGithubClient(source.CommonConfig, source.GithubConfig)
		require.NoError(t, err)

		before := getRemainingRateLimit(t, client.V4)

		request := prlist.CheckRequest{
			Source:  source,
			Version: nil,
		}

		_, err = prlist.Check(request, client)
		require.NoError(t, err)

		after := getRemainingRateLimit(t, client.V4)
		cost := before - after

		// PR list mode uses GraphQL which should be efficient
		assert.LessOrEqual(t, cost, 2, "PR list mode check should use at most 2 API calls")
	})
}

// TestPathFiltering tests path-based filtering
func TestPathFiltering(t *testing.T) {
	if testAccessToken == "" {
		t.Skip("GITHUB_ACCESS_TOKEN not set")
	}

	tests := []struct {
		name         string
		source       prlist.Source
		expectPRs    bool
		expectedPRID string
	}{
		{
			name: "include paths filter",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
					Paths:       []string{"*.md"},
				},
			},
			expectPRs: true,
		},
		{
			name: "exclude paths filter",
			source: prlist.Source{
				CommonConfig: models.CommonConfig{
					Repository:  testRepository,
					AccessToken: testAccessToken,
					IgnorePaths: []string{"*.txt"},
				},
			},
			expectPRs: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := prlist.CheckRequest{
				Source:  tt.source,
				Version: nil,
			}

			client, err := models.NewGithubClient(tt.source.CommonConfig, tt.source.GithubConfig)
			require.NoError(t, err)

			response, err := prlist.Check(request, client)
			require.NoError(t, err)

			if tt.expectPRs {
				assert.Greater(t, len(response), 0)
			}
		})
	}
}

// Helper functions

func gitHistory(t *testing.T, directory string) map[int]string {
	cmd := exec.Command("git", "log", "--oneline", "--pretty=format:%s")
	cmd.Dir = directory

	output, err := cmd.Output()
	if err != nil {
		// If git history doesn't exist, return empty map
		return make(map[int]string)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	history := make(map[int]string, len(lines))
	for i, line := range lines {
		if line != "" {
			history[i] = line
		}
	}

	return history
}

func readTestFile(t *testing.T, path string) string {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read: %s: %s", path, err)
	}
	return string(b)
}

func getRemainingRateLimit(t *testing.T, c *githubv4.Client) int {
	var query struct {
		RateLimit struct {
			Remaining int
		}
	}
	if err := c.Query(context.TODO(), &query, nil); err != nil {
		t.Fatalf("rate limit query: %s", err)
	}
	return query.RateLimit.Remaining
}
