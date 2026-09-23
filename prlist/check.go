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

	if len(request.Source.TriggerComments) > 0 {
		var err error
		versions, err = applyCommentTriggers(ctx, request, github, filteredPRs, versions)
		if err != nil {
			return nil, err
		}
	}

	return versions, nil
}

// applyCommentTriggers scans every currently path-matching PR for a comment
// matching source.trigger_comments and appends an extra version for any PR
// whose latest matching comment is new since this resource last observed
// that specific PR.
//
// Caveat: unlike single-PR mode, this resource's version stream only
// remembers a single cursor — the one version Concourse hands back as
// request.Version — not a per-PR history. Once the cursor advances past a
// given PR (e.g. because a different PR got a new commit), that PR's
// comment watermark is lost. A later comment on it is then treated as
// establishing a fresh baseline (no trigger) rather than firing
// immediately. This is best-effort: reliable when at most one PR is being
// actively worked on at a time, but can miss (or, rarely, double-fire) a
// trigger under heavy concurrent PR churn across many matching PRs.
func applyCommentTriggers(ctx context.Context, request CheckRequest, github *models.GithubClient, filteredPRs []*models.PullRequest, versions []models.Version) ([]models.Version, error) {
	for _, pr := range filteredPRs {
		prKey := strconv.Itoa(pr.Number)

		baselineEstablished := request.Version != nil && request.Version.PR == prKey && request.Version.CommentBaseline
		var sinceID int64
		if baselineEstablished {
			sinceID = request.Version.CommentID
		}

		latestMatchID, triggered, err := github.CheckTriggerComments(ctx, pr.Number, request.Source.TriggerComments, sinceID)
		if err != nil {
			return nil, fmt.Errorf("failed to check trigger comments for PR #%d: %w", pr.Number, err)
		}
		if !baselineEstablished {
			triggered = false
		}

		// Stamp the watermark on any version already in this batch for this
		// PR, so the cursor — if it lands here — carries the up-to-date
		// CommentID forward.
		for i := range versions {
			if versions[i].PR == prKey {
				versions[i].CommentID = latestMatchID
				versions[i].CommentBaseline = true
			}
		}

		if triggered {
			versions = append(versions, models.Version{
				PR:                  prKey,
				Commit:              pr.HeadRefOID,
				CommittedDate:       pr.CommittedDate,
				ApprovedReviewCount: pr.ApprovedReviewCount,
				CommentID:           latestMatchID,
				CommentBaseline:     true,
			})
		}
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
