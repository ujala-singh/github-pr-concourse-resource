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

	// Allow the pipeline to supply a Teleport / proxy hostname that differs from
	// Concourse's locked externalUrl (which may be bound to an STS OIDC issuer).
	// Overriding ATC_EXTERNAL_URL here affects both safeExpandEnv and the
	// auto-generated build URL in UpdateCommitStatus.
	if request.Source.ConcourseURL != "" {
		if err := os.Setenv("ATC_EXTERNAL_URL", request.Source.ConcourseURL); err != nil {
			return OutResponse{}, fmt.Errorf("failed to set ATC_EXTERNAL_URL: %w", err)
		}
	}

	// The version the build's get fetched, from its signed version.json:
	// the put reports on that PR and commit only, and returns that version
	// unchanged. Nothing else in the working copy (the pr or head_sha
	// files) is trusted, since tasks — and so a PR's own code — can change
	// it; the signature (see models.WriteVersionSignature) can't be forged
	// without this resource's credentials. Returning the version unchanged,
	// rather than rebuilding it from the PR now (its head, commit date,
	// approvals may have moved on), keeps the job's own put from looking
	// like a new version and triggering the job again.
	resourcePath := filepath.Join(sourcesDir, request.Params.Path, ".git", "resource")
	version, err := models.ReadSignedVersion(resourcePath, request.Source.CommonConfig)
	if err != nil {
		return OutResponse{}, err
	}
	prNumber, err := strconv.Atoi(version.PR)
	if err != nil || prNumber < 1 {
		return OutResponse{}, fmt.Errorf("version names no PR: %q", version.PR)
	}
	if request.Source.Number != 0 && prNumber != request.Source.Number {
		return OutResponse{}, fmt.Errorf("the checkout is of PR #%d, but this resource is for PR #%d", prNumber, request.Source.Number)
	}
	commit := version.Commit

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
			if err := github.AddComment(ctx, prNumber, models.DefuseNoteMarkers(safeExpandEnv(comment))); err != nil {
				return OutResponse{}, fmt.Errorf("failed to add comment: %w", err)
			}
		}
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
