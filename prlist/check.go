package prlist

import (
	"context"
	"fmt"
	"strconv"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// Check performs the check operation for PR list mode
// Returns a list of versions representing the current set of PRs
func Check(request CheckRequest, github *models.GithubClient) ([]models.Version, error) {
	ctx := context.Background()

	prs, err := github.GetPullRequests(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull requests: %w", err)
	}

	// Filter PRs by path if configured
	var filteredPRs []*models.PullRequest
	for _, pr := range prs {
		matches, err := github.MatchesPathFilters(ctx, pr)
		if err != nil {
			return nil, fmt.Errorf("failed to check path filters for PR #%d: %w", pr.Number, err)
		}
		if matches {
			filteredPRs = append(filteredPRs, pr)
		}
	}

	// Convert to versions
	var versions []models.Version
	for _, pr := range filteredPRs {
		versions = append(versions, models.Version{
			PR:                  strconv.Itoa(pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}

	// If we have a previous version, only return new versions
	if request.Version != nil {
		versions = filterNewVersions(versions, *request.Version)
	}

	// If no new versions, return the current version to indicate no changes
	if len(versions) == 0 && request.Version != nil {
		versions = []models.Version{*request.Version}
	}

	return versions, nil
}

// filterNewVersions returns only the versions that are newer than the given version
func filterNewVersions(versions []models.Version, lastVersion models.Version) []models.Version {
	var newVersions []models.Version
	foundLast := false

	for _, v := range versions {
		if v.PR == lastVersion.PR {
			foundLast = true
			continue
		}
		if foundLast {
			newVersions = append(newVersions, v)
		}
	}

	// If we didn't find the last version in the list, return all versions
	if !foundLast {
		return versions
	}

	return newVersions
}
