package prlist

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// In performs the in operation for PR list mode
// Clones the repository and checks out the PR commit
func In(request InRequest, github *models.GithubClient, destinationDir string) (InResponse, error) {
	ctx := context.Background()

	// Handle skip_download parameter
	if request.Params.SkipDownload {
		// Just create minimal metadata without cloning
		prNumber, err := strconv.Atoi(request.Version.PR)
		if err != nil {
			return InResponse{}, fmt.Errorf("invalid PR number: %w", err)
		}

		pr, err := github.GetPullRequest(ctx, prNumber)
		if err != nil {
			return InResponse{}, fmt.Errorf("failed to get pull request: %w", err)
		}

		metadata := []models.Metadata{
			{Name: "pr", Value: strconv.Itoa(pr.Number)},
			{Name: "url", Value: pr.URL},
			{Name: "head_sha", Value: request.Version.Commit},
		}

		if err := writeMetadataFiles(destinationDir, metadata, request.Version); err != nil {
			return InResponse{}, fmt.Errorf("failed to write metadata: %w", err)
		}

		return InResponse{
			Version:  request.Version,
			Metadata: metadata,
		}, nil
	}

	prNumber, err := strconv.Atoi(request.Version.PR)
	if err != nil {
		return InResponse{}, fmt.Errorf("invalid PR number: %w", err)
	}

	pr, err := github.GetPullRequest(ctx, prNumber)
	if err != nil {
		return InResponse{}, fmt.Errorf("failed to get pull request: %w", err)
	}

	// Get access token for git operations
	accessToken, err := github.GetAccessToken(ctx)
	if err != nil {
		return InResponse{}, fmt.Errorf("failed to get access token: %w", err)
	}

	// Clone the base branch
	owner, repo := github.Config.GetOwnerAndRepo()
	repoURL := buildRepoURL(github.Config, owner, repo, accessToken)

	if err := cloneRepo(repoURL, pr.BaseRefName, destinationDir, request.Params.GitDepth); err != nil {
		return InResponse{}, fmt.Errorf("failed to clone repository: %w", err)
	}

	// Fetch the PR
	if err := fetchPR(destinationDir, prNumber); err != nil {
		return InResponse{}, fmt.Errorf("failed to fetch PR: %w", err)
	}

	// Checkout the specific commit
	if err := checkoutCommit(destinationDir, request.Version.Commit); err != nil {
		return InResponse{}, fmt.Errorf("failed to checkout commit: %w", err)
	}

	// Create metadata
	metadata := []models.Metadata{
		{Name: "pr", Value: strconv.Itoa(pr.Number)},
		{Name: "url", Value: pr.URL},
		{Name: "title", Value: pr.Title},
		{Name: "author", Value: pr.AuthorLogin},
		{Name: "author_avatar", Value: pr.AuthorAvatarURL},
		{Name: "head_ref", Value: pr.HeadRefName},
		{Name: "head_sha", Value: pr.HeadRefOID},
		{Name: "base_ref", Value: pr.BaseRefName},
		{Name: "state", Value: pr.State},
		{Name: "draft", Value: strconv.FormatBool(pr.IsDraft)},
		{Name: "approved_review_count", Value: strconv.Itoa(pr.ApprovedReviewCount)},
	}

	if len(pr.Labels) > 0 {
		labelsStr := ""
		for i, label := range pr.Labels {
			if i > 0 {
				labelsStr += ", "
			}
			labelsStr += label
		}
		metadata = append(metadata, models.Metadata{Name: "labels", Value: labelsStr})
	}

	// Write metadata to files in destination directory
	if err := writeMetadataFiles(destinationDir, metadata, request.Version); err != nil {
		return InResponse{}, fmt.Errorf("failed to write metadata: %w", err)
	}

	return InResponse{
		Version:  request.Version,
		Metadata: metadata,
	}, nil
}

// writeMetadataFiles writes metadata to individual files in the .git/resource directory
func writeMetadataFiles(destDir string, metadata []models.Metadata, version models.Version) error {
	resourceDir := filepath.Join(destDir, ".git", "resource")
	if err := os.MkdirAll(resourceDir, 0755); err != nil {
		return fmt.Errorf("failed to create resource directory: %w", err)
	}

	// Write individual metadata files
	for _, m := range metadata {
		filePath := filepath.Join(resourceDir, m.Name)
		if err := os.WriteFile(filePath, []byte(m.Value), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", m.Name, err)
		}
	}

	// Write version.json
	versionPath := filepath.Join(resourceDir, "version.json")
	versionJSON := fmt.Sprintf(`{"pr":"%s","commit":"%s","committed":"%s","approved_review_count":%d}`,
		version.PR, version.Commit, version.CommittedDate, version.ApprovedReviewCount)
	if err := os.WriteFile(versionPath, []byte(versionJSON), 0644); err != nil {
		return fmt.Errorf("failed to write version.json: %w", err)
	}

	return nil
}

// Git helper functions

func buildRepoURL(config models.CommonConfig, owner, repo, token string) string {
	if config.HostingEndpoint != "" {
		return fmt.Sprintf("https://x-access-token:%s@%s/%s/%s.git",
			token,
			strings.TrimPrefix(config.HostingEndpoint, "https://"),
			owner, repo)
	}
	return fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git",
		token, owner, repo)
}

func cloneRepo(repoURL, branch, destination string, depth int) error {
	args := []string{"clone", "--single-branch", "--branch", branch}

	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}

	args = append(args, "--no-tags", repoURL, destination)

	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func fetchPR(repoDir string, prNumber int) error {
	cmd := exec.Command("git", "fetch", "origin", fmt.Sprintf("pull/%d/head", prNumber))
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkoutCommit(repoDir, sha string) error {
	cmd := exec.Command("git", "checkout", "-q", sha)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
