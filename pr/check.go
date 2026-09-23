package pr

import (
	"context"
	"fmt"
	"time"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// Check performs the check operation for single PR mode.
// Returns new versions since the last known version. A version may be
// triggered either by a new commit or by a PR comment matching
// source.trigger_comments (see CommonConfig.TriggerComments).
func Check(request CheckRequest, github *models.GithubClient) ([]models.Version, error) {
	ctx := context.Background()

	firstRun := request.Version == nil

	// ── 1. Commit-based check ────────────────────────────────────────────────
	since := time.Time{}
	if !firstRun && request.Version.CommittedDate != "" {
		var err error
		since, err = time.Parse(time.RFC3339, request.Version.CommittedDate)
		if err != nil {
			return nil, fmt.Errorf("failed to parse committed date: %w", err)
		}
	}

	prs, err := github.GetPullRequestCommits(ctx, request.Source.Number, since)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull request commits: %w", err)
	}

	if len(prs) > 0 {
		matches, err := github.MatchesPathFilters(ctx, prs[0])
		if err != nil {
			return nil, fmt.Errorf("failed to check path filters: %w", err)
		}
		if !matches {
			if !firstRun {
				return []models.Version{*request.Version}, nil
			}
			return []models.Version{}, nil
		}
	}

	var versions []models.Version
	for _, pr := range prs {
		versions = append(versions, models.Version{
			PR:                  fmt.Sprintf("%d", pr.Number),
			Commit:              pr.HeadRefOID,
			CommittedDate:       pr.CommittedDate,
			ApprovedReviewCount: pr.ApprovedReviewCount,
		})
	}

	if len(versions) == 0 && !firstRun {
		versions = []models.Version{*request.Version}
	}

	// ── 2. Comment-trigger check ─────────────────────────────────────────────
	// When trigger_comments is set, scan PR comments for matching prefixes.
	// A match newer than the last known CommentID fires an extra version,
	// causing the plan job to re-run exactly as if a new commit had arrived.
	if len(request.Source.TriggerComments) > 0 {
		// firstCheck = true when: (a) first run overall, or (b) the stored
		// version predates trigger_comments support (CommentID == 0). In both
		// cases we establish a watermark without firing a build.
		firstCheck := firstRun || request.Version.CommentID == 0
		var sinceCommentID int64
		if !firstCheck {
			sinceCommentID = request.Version.CommentID
		}

		latestMatchID, triggered, err := github.CheckTriggerComments(
			ctx, request.Source.Number, request.Source.TriggerComments,
			sinceCommentID, firstCheck,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to check trigger comments: %w", err)
		}

		// Stamp the watermark on all versions so future checks don't re-trigger.
		for i := range versions {
			if latestMatchID > versions[i].CommentID {
				versions[i].CommentID = latestMatchID
			}
		}

		if triggered {
			// Fetch current PR HEAD — the comment may have arrived after the
			// last commit, so we re-read rather than trusting the cached SHA.
			pr, err := github.GetPullRequest(ctx, request.Source.Number)
			if err != nil {
				return nil, fmt.Errorf("failed to get PR for comment trigger: %w", err)
			}
			versions = append(versions, models.Version{
				PR:                  fmt.Sprintf("%d", request.Source.Number),
				Commit:              pr.HeadRefOID,
				CommittedDate:       pr.CommittedDate,
				ApprovedReviewCount: pr.ApprovedReviewCount,
				CommentID:           latestMatchID,
			})
		}
	}

	return versions, nil
}
