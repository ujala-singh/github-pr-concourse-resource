package pr

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

// In performs the in operation for single PR mode
// Clones the repository and merges/rebases the PR
func In(request InRequest, github *models.GithubClient, destinationDir string) (InResponse, error) {
	ctx := context.Background()

	if request.Params.SkipDownload {
		// Just create minimal metadata
		return InResponse{
			Version: request.Version,
			Metadata: []models.Metadata{
				{Name: "pr", Value: request.Version.PR},
				{Name: "commit", Value: request.Version.Commit},
			},
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

	owner, repo := github.Config.GetOwnerAndRepo()
	repoURL := buildRepoURL(github.Config, owner, repo, accessToken)

	baseSHA, integrationTool, err := checkoutPR(repoURL, pr.BaseRefName, prNumber, request.Version.Commit, request.Params, destinationDir)
	if err != nil {
		return InResponse{}, err
	}

	// Handle submodules
	if request.Params.Submodules {
		if err := updateSubmodules(destinationDir); err != nil {
			return InResponse{}, fmt.Errorf("failed to update submodules: %w", err)
		}
	}

	// Create metadata
	metadata := []models.Metadata{
		{Name: "pr", Value: strconv.Itoa(pr.Number)},
		{Name: "url", Value: pr.URL},
		{Name: "title", Value: pr.Title},
		{Name: "author", Value: pr.AuthorLogin},
		{Name: "head_ref", Value: pr.HeadRefName},
		{Name: "head_sha", Value: request.Version.Commit},
		{Name: "base_ref", Value: pr.BaseRefName},
		{Name: "base_sha", Value: baseSHA},
		{Name: "integration_tool", Value: integrationTool},
		{Name: "state", Value: pr.State},
		{Name: "approved_review_count", Value: strconv.Itoa(pr.ApprovedReviewCount)},
	}

	// List changed files if requested
	if request.Params.ListChangedFiles {
		files, err := github.GetChangedFiles(ctx, prNumber)
		if err != nil {
			return InResponse{}, fmt.Errorf("failed to get changed files: %w", err)
		}

		changedFilesPath := filepath.Join(destinationDir, ".git", "resource", "changed_files")
		if err := os.WriteFile(changedFilesPath, []byte(strings.Join(files, "\n")), 0644); err != nil {
			return InResponse{}, fmt.Errorf("failed to write changed_files: %w", err)
		}

		metadata = append(metadata, models.Metadata{
			Name:  "changed_files_count",
			Value: strconv.Itoa(len(files)),
		})
	}

	// Write metadata files
	if err := writeMetadataFiles(destinationDir, metadata, request.Version); err != nil {
		return InResponse{}, fmt.Errorf("failed to write metadata: %w", err)
	}

	return InResponse{
		Version:  request.Version,
		Metadata: metadata,
	}, nil
}

// Git helper functions

// checkoutPR clones baseRef into destinationDir, fetches the PR, and leaves
// the working tree integrated according to params.IntegrationTool (merge by
// default, rebase, or checkout of the PR head alone).
//
// It returns the base branch tip it cloned (the base_sha metadata) and the
// integration tool used. The base tip is recorded right after the clone,
// before the PR head is checked out: reading HEAD any later would return the
// PR head, not the base.
func checkoutPR(repoURL, baseRef string, prNumber int, headSHA string, params InParams, destinationDir string) (baseSHA, integrationTool string, err error) {
	integrationTool = params.IntegrationTool
	if integrationTool == "" {
		integrationTool = "merge"
	}
	switch integrationTool {
	case "merge", "rebase", "checkout":
	default:
		return "", "", fmt.Errorf("invalid integration_tool: %s (must be merge, rebase, or checkout)", integrationTool)
	}

	if err := cloneRepo(repoURL, baseRef, destinationDir, params.GitDepth, params.FetchTags); err != nil {
		return "", "", fmt.Errorf("failed to clone repository: %w", err)
	}

	baseSHA, err = getCommitSHA(destinationDir, "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("failed to get base SHA: %w", err)
	}

	if err := fetchPR(destinationDir, prNumber); err != nil {
		return "", "", fmt.Errorf("failed to fetch PR: %w", err)
	}

	if err := checkoutCommit(destinationDir, headSHA); err != nil {
		return "", "", fmt.Errorf("failed to checkout commit: %w", err)
	}

	if integrationTool == "checkout" {
		return baseSHA, integrationTool, nil
	}

	if err := checkoutBranch(destinationDir, baseRef); err != nil {
		return "", "", fmt.Errorf("failed to checkout base branch: %w", err)
	}
	switch integrationTool {
	case "merge":
		if err := mergePR(destinationDir, headSHA); err != nil {
			return "", "", fmt.Errorf("failed to merge PR: %w", err)
		}
	case "rebase":
		if err := rebasePR(destinationDir, headSHA); err != nil {
			return "", "", fmt.Errorf("failed to rebase PR: %w", err)
		}
	}
	return baseSHA, integrationTool, nil
}

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

func cloneRepo(repoURL, branch, destination string, depth int, fetchTags bool) error {
	args := []string{"clone", "--single-branch", "--branch", branch}

	if depth > 0 {
		args = append(args, "--depth", strconv.Itoa(depth))
	}

	if !fetchTags {
		args = append(args, "--no-tags")
	}

	args = append(args, repoURL, destination)

	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func fetchPR(repoDir string, prNumber int) error {
	cmd := exec.Command("git", "fetch", "origin", fmt.Sprintf("pull/%d/head", prNumber))
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkoutCommit(repoDir, sha string) error {
	cmd := exec.Command("git", "checkout", "-q", sha)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func checkoutBranch(repoDir, branch string) error {
	cmd := exec.Command("git", "checkout", "-B", branch, "origin/"+branch)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func getCommitSHA(repoDir, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gitIdentity is the committer for the commits merge/rebase create locally.
// Resource containers have no git user configured, and `git merge --no-ff`
// (always) and `git rebase` (when replaying commits) refuse to commit without
// one. These commits never leave the build's checkout.
var gitIdentity = []string{"-c", "user.name=Concourse CI", "-c", "user.email=concourse-ci@users.noreply.github.com"}

func mergePR(repoDir, sha string) error {
	args := append(append([]string{}, gitIdentity...), "merge", "--no-ff", sha, "-m", fmt.Sprintf("Merge PR commit %s", sha))
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func rebasePR(repoDir, sha string) error {
	args := append(append([]string{}, gitIdentity...), "rebase", sha)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateSubmodules(repoDir string) error {
	cmd := exec.Command("git", "submodule", "update", "--init", "--recursive")
	cmd.Dir = repoDir
	cmd.Stdout = os.Stderr // stdout is reserved for the JSON response
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

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

	// Write version.json using the same encoding Check/In/Out exchange with
	// Concourse (see Version.MarshalJSON), so Out can read this file back
	// and recover fields — like CommentID/CommentBaseline — that aren't
	// otherwise derivable from the checked-out working directory.
	versionPath := filepath.Join(resourceDir, "version.json")
	versionJSON, err := version.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal version.json: %w", err)
	}
	if err := os.WriteFile(versionPath, versionJSON, 0644); err != nil {
		return fmt.Errorf("failed to write version.json: %w", err)
	}

	// Write metadata.json
	metadataPath := filepath.Join(resourceDir, "metadata.json")
	var metadataJSON strings.Builder
	metadataJSON.WriteString("[")
	for i, m := range metadata {
		if i > 0 {
			metadataJSON.WriteString(",")
		}
		fmt.Fprintf(&metadataJSON, `{"name":"%s","value":"%s"}`, m.Name, m.Value)
	}
	metadataJSON.WriteString("]")
	if err := os.WriteFile(metadataPath, []byte(metadataJSON.String()), 0644); err != nil {
		return fmt.Errorf("failed to write metadata.json: %w", err)
	}

	return nil
}
