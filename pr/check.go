package pr

import (
	"context"
	"fmt"
	"time"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// Check performs the check operation for single PR mode
// Returns commits to the specified PR after the last version
func Check(request CheckRequest, github *models.GithubClient) ([]models.Version, error) {
	ctx := context.Background()

	// Determine the "since" timestamp
	since := time.Time{}
	if request.Version != nil && request.Version.CommittedDate != "" {
		var err error
		since, err = time.Parse(time.RFC3339, request.Version.CommittedDate)
		if err != nil {
			return nil, fmt.Errorf("failed to parse committed date: %w", err)
		}
	}

	// Get commits for the PR
	prs, err := github.GetPullRequestCommits(ctx, request.Source.Number, since)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull request commits: %w", err)
	}

	// Check if PR matches path filters
	if len(prs) > 0 {
		matches, err := github.MatchesPathFilters(ctx, prs[0])
		if err != nil {
			return nil, fmt.Errorf("failed to check path filters: %w", err)
		}
		if !matches {
			// Return current version if no match
			if request.Version != nil {
				return []models.Version{*request.Version}, nil
			}
			return []models.Version{}, nil
		}
	}

	// Convert to versions
	var versions []models.Version
	for _, pr := range prs {
		versions = append(versions, models.Version{
			PR:                  fmt.Sprintf("%d", pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}

	// If no new versions and we have a previous version, return it
	if len(versions) == 0 && request.Version != nil {
		return []models.Version{*request.Version}, nil
	}

	return versions, nil
}
