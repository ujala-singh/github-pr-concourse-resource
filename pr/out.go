package pr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// Out performs the out operation for single PR mode
// Updates PR status, adds comments, etc.
func Out(request OutRequest, github *models.GithubClient, sourcesDir string) (OutResponse, error) {
	ctx := context.Background()

	// Read metadata from the source path
	resourcePath := filepath.Join(sourcesDir, request.Params.Path, ".git", "resource")

	versionFile := filepath.Join(resourcePath, "pr")
	prBytes, err := os.ReadFile(versionFile)
	if err != nil {
		return OutResponse{}, fmt.Errorf("failed to read pr number: %w", err)
	}
	prNumber, err := strconv.Atoi(string(prBytes))
	if err != nil {
		return OutResponse{}, fmt.Errorf("invalid pr number: %w", err)
	}

	commitFile := filepath.Join(resourcePath, "head_sha")
	commitBytes, err := os.ReadFile(commitFile)
	if err != nil {
		return OutResponse{}, fmt.Errorf("failed to read commit sha: %w", err)
	}
	commit := string(commitBytes)

	// Get PR details
	pr, err := github.GetPullRequest(ctx, prNumber)
	if err != nil {
		return OutResponse{}, fmt.Errorf("failed to get pull request: %w", err)
	}

	// Update commit status if requested
	if request.Params.Status != "" {
		baseContext := request.Params.BaseContext
		if baseContext == "" {
			baseContext = "concourse-ci"
		}

		statusContext := request.Params.Context
		if statusContext == "" {
			statusContext = "status"
		}

		description := request.Params.Description

		// Read description from file if specified
		if request.Params.DescriptionFile != "" {
			descriptionPath := filepath.Join(sourcesDir, request.Params.Path, request.Params.DescriptionFile)
			descriptionBytes, err := os.ReadFile(descriptionPath)
			if err != nil {
				return OutResponse{}, fmt.Errorf("failed to read description file: %w", err)
			}
			description = string(descriptionBytes)
		}

		if description == "" {
			description = fmt.Sprintf("Concourse CI build %s", request.Params.Status)
		}

		if err := github.UpdateCommitStatus(ctx, commit, request.Params.Status, safeExpandEnv(request.Params.TargetURL), description, baseContext, safeExpandEnv(statusContext)); err != nil {
			return OutResponse{}, fmt.Errorf("failed to update commit status: %w", err)
		}
	}

	// Delete previous comments if requested
	if request.Params.DeletePreviousComments {
		if err := github.DeletePreviousComments(ctx, prNumber); err != nil {
			return OutResponse{}, fmt.Errorf("failed to delete previous comments: %w", err)
		}
	}

	// Add comment if requested
	if request.Params.Comment != "" || request.Params.CommentFile != "" {
		comment := request.Params.Comment
		if request.Params.CommentFile != "" {
			commentPath := filepath.Join(sourcesDir, request.Params.Path, request.Params.CommentFile)
			commentBytes, err := os.ReadFile(commentPath)
			if err != nil {
				return OutResponse{}, fmt.Errorf("failed to read comment file: %w", err)
			}
			comment = string(commentBytes)
		}

		if comment != "" {
			if err := github.AddComment(ctx, prNumber, safeExpandEnv(comment)); err != nil {
				return OutResponse{}, fmt.Errorf("failed to add comment: %w", err)
			}
		}
	}

	// Create response
	version := models.Version{
		PR:                  strconv.Itoa(pr.Number),
		Commit:              commit,
		CommittedDate:       pr.CommittedDate,
		ApprovedReviewCount: pr.ApprovedReviewCount,
	}

	metadata := []models.Metadata{
		{Name: "pr", Value: strconv.Itoa(pr.Number)},
		{Name: "url", Value: pr.URL},
		{Name: "title", Value: pr.Title},
		{Name: "head_sha", Value: commit},
	}

	if request.Params.Status != "" {
		metadata = append(metadata, models.Metadata{
			Name:  "status",
			Value: request.Params.Status,
		})
	}

	return OutResponse{
		Version:  version,
		Metadata: metadata,
	}, nil
}

// safeExpandEnv expands only Concourse build metadata environment variables
func safeExpandEnv(s string) string {
	return os.Expand(s, func(v string) string {
		switch v {
		case "BUILD_ID", "BUILD_NAME", "BUILD_JOB_NAME", "BUILD_PIPELINE_NAME", "BUILD_TEAM_NAME", "ATC_EXTERNAL_URL":
			return os.Getenv(v)
		}
		return "$" + v
	})
}
