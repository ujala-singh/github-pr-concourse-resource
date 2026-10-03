package models

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"
)

// GraphQL query structures
type prNode struct {
	Number      githubv4.Int
	Title       githubv4.String
	URL         githubv4.String
	State       githubv4.String
	IsDraft     githubv4.Boolean
	BaseRefName githubv4.String
	HeadRefName githubv4.String
	HeadRefOID  githubv4.String
	Repository  struct {
		URL githubv4.String
	}
	HeadRepository struct {
		URL githubv4.String
	}
	Author struct {
		Login     githubv4.String
		AvatarURL githubv4.String
	}
	Labels struct {
		Nodes []struct {
			Name githubv4.String
		}
	} `graphql:"labels(first: 100)"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				OID           githubv4.String
				CommittedDate githubv4.DateTime
				Additions     githubv4.Int
				Deletions     githubv4.Int
			}
		}
	} `graphql:"commits(last: 1)"`
	Reviews struct {
		Nodes []struct {
			State  githubv4.String
			Author struct {
				Login githubv4.String
			}
		}
	} `graphql:"reviews(last: 100, states: [APPROVED])"`
}

type prQuery struct {
	Repository struct {
		PullRequests struct {
			Edges []struct {
				Node prNode
			}
			PageInfo struct {
				EndCursor   githubv4.String
				HasNextPage githubv4.Boolean
			}
		} `graphql:"pullRequests(first: 100, after: $cursor, states: $states, baseRefName: $baseRefName)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

type singlePRQuery struct {
	Repository struct {
		PullRequest prNode `graphql:"pullRequest(number: $number)"`
	} `graphql:"repository(owner: $owner, name: $name)"`
}

// GetPullRequests fetches all pull requests matching the filter criteria
func (gc *GithubClient) GetPullRequests(ctx context.Context) ([]*PullRequest, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	var allPRs []*PullRequest
	var cursor *githubv4.String

	// Determine which states to query
	states := []githubv4.PullRequestState{githubv4.PullRequestStateOpen}
	if len(gc.Config.States) > 0 {
		states = []githubv4.PullRequestState{}
		for _, s := range gc.Config.States {
			switch strings.ToUpper(s) {
			case "OPEN":
				states = append(states, githubv4.PullRequestStateOpen)
			case "MERGED":
				states = append(states, githubv4.PullRequestStateMerged)
			case "CLOSED":
				states = append(states, githubv4.PullRequestStateClosed)
			}
		}
	}

	baseRefName := githubv4.String("")
	if gc.Config.BaseBranch != "" {
		baseRefName = githubv4.String(gc.Config.BaseBranch)
	}

	for {
		var query prQuery
		variables := map[string]interface{}{
			"owner":       githubv4.String(owner),
			"name":        githubv4.String(repo),
			"cursor":      cursor,
			"states":      states,
			"baseRefName": (*githubv4.String)(nil),
		}

		if baseRefName != "" {
			variables["baseRefName"] = baseRefName
		}

		if err := gc.V4.Query(ctx, &query, variables); err != nil {
			return nil, fmt.Errorf("failed to query pull requests: %w", err)
		}

		for _, edge := range query.Repository.PullRequests.Edges {
			pr := gc.convertPRNode(edge.Node)

			// Apply filters
			if gc.shouldSkipPR(pr) {
				continue
			}

			allPRs = append(allPRs, pr)
		}

		if !query.Repository.PullRequests.PageInfo.HasNextPage {
			break
		}
		cursor = &query.Repository.PullRequests.PageInfo.EndCursor
	}

	return allPRs, nil
}

// GetPullRequest fetches a single pull request by number
func (gc *GithubClient) GetPullRequest(ctx context.Context, number int) (*PullRequest, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	var query singlePRQuery
	variables := map[string]interface{}{
		"owner":  githubv4.String(owner),
		"name":   githubv4.String(repo),
		"number": githubv4.Int(number),
	}

	if err := gc.V4.Query(ctx, &query, variables); err != nil {
		return nil, fmt.Errorf("failed to query pull request #%d: %w", number, err)
	}

	pr := gc.convertPRNode(query.Repository.PullRequest)
	return pr, nil
}

// GetPullRequestCommits fetches all commits for a specific PR
func (gc *GithubClient) GetPullRequestCommits(ctx context.Context, number int, since time.Time) ([]*PullRequest, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	commits, _, err := gc.V3.PullRequests.ListCommits(ctx, owner, repo, number, &github.ListOptions{
		PerPage: 100,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list commits: %w", err)
	}

	var prs []*PullRequest
	for _, commit := range commits {
		if commit.Commit.Committer.Date.Before(since) {
			continue
		}

		pr, err := gc.GetPullRequest(ctx, number)
		if err != nil {
			return nil, err
		}

		pr.HeadRefOID = commit.GetSHA()
		pr.CommittedDate = commit.Commit.Committer.Date.Format(time.RFC3339)
		prs = append(prs, pr)
	}

	return prs, nil
}

// GetChangedFiles returns the list of files changed in a PR
func (gc *GithubClient) GetChangedFiles(ctx context.Context, number int) ([]string, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	var allFiles []string
	opts := &github.ListOptions{PerPage: 100}

	for {
		files, resp, err := gc.V3.PullRequests.ListFiles(ctx, owner, repo, number, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list files: %w", err)
		}

		for _, file := range files {
			allFiles = append(allFiles, file.GetFilename())
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allFiles, nil
}

// GetChangedFilesSince returns the files changed between baseSHA and
// headSHA (exclusive of baseSHA, inclusive of headSHA) using GitHub's
// compare-two-commits API. Unlike GetChangedFiles — which always reflects
// a PR's entire base-branch...HEAD diff, regardless of which commit you're
// asking about — this answers "what changed between these two specific
// commits," which is what's needed to tell whether a new push touched
// matching paths, rather than whether the PR has ever, cumulatively,
// touched them.
func (gc *GithubClient) GetChangedFilesSince(ctx context.Context, baseSHA, headSHA string) ([]string, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	var allFiles []string
	opts := &github.ListOptions{PerPage: 100}

	for {
		comparison, resp, err := gc.V3.Repositories.CompareCommits(ctx, owner, repo, baseSHA, headSHA, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to compare commits %s...%s: %w", baseSHA, headSHA, err)
		}

		for _, file := range comparison.Files {
			allFiles = append(allFiles, file.GetFilename())
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allFiles, nil
}

// UpdateCommitStatus updates the status of a commit
// If targetURL is empty, it automatically generates a Concourse build URL
func (gc *GithubClient) UpdateCommitStatus(ctx context.Context, sha, state, targetURL, description, baseContext, statusContext string) error {
	owner, repo := gc.Config.GetOwnerAndRepo()

	// Auto-generate build URL if not provided (matching telia-oss behavior)
	if targetURL == "" {
		atcURL := os.Getenv("ATC_EXTERNAL_URL")
		buildID := os.Getenv("BUILD_ID")
		if atcURL != "" && buildID != "" {
			targetURL = strings.Join([]string{atcURL, "builds", buildID}, "/")
		}
	}

	// Combine base context and status context (e.g., "concourse-ci/status")
	fullContext := path.Join(baseContext, statusContext)

	status := &github.RepoStatus{
		State:       github.String(state),
		TargetURL:   github.String(targetURL),
		Description: github.String(description),
		Context:     github.String(fullContext),
	}

	_, _, err := gc.V3.Repositories.CreateStatus(ctx, owner, repo, sha, status)
	if err != nil {
		return fmt.Errorf("failed to update commit status: %w", err)
	}

	return nil
}

// AddComment adds a comment to a pull request
func (gc *GithubClient) AddComment(ctx context.Context, number int, body string) error {
	owner, repo := gc.Config.GetOwnerAndRepo()

	comment := &github.IssueComment{
		Body: github.String(body),
	}

	_, _, err := gc.V3.Issues.CreateComment(ctx, owner, repo, number, comment)
	if err != nil {
		return fmt.Errorf("failed to add comment: %w", err)
	}

	return nil
}

// DeletePreviousComments deletes all previous comments made by the current user on a PR
func (gc *GithubClient) DeletePreviousComments(ctx context.Context, prNumber int) error {
	owner, repo := gc.Config.GetOwnerAndRepo()

	// Get the current authenticated user
	var viewerQuery struct {
		Viewer struct {
			Login githubv4.String
		}
	}

	if err := gc.V4.Query(ctx, &viewerQuery, nil); err != nil {
		return fmt.Errorf("failed to get viewer: %w", err)
	}

	currentUserLogin := string(viewerQuery.Viewer.Login)

	// Get all comments on the PR
	var commentsQuery struct {
		Repository struct {
			PullRequest struct {
				Comments struct {
					Edges []struct {
						Node struct {
							DatabaseId githubv4.Int
							Author     struct {
								Login githubv4.String
							}
						}
					}
				} `graphql:"comments(last: $commentsLast)"`
			} `graphql:"pullRequest(number: $prNumber)"`
		} `graphql:"repository(owner: $repositoryOwner, name: $repositoryName)"`
	}

	variables := map[string]interface{}{
		"repositoryOwner": githubv4.String(owner),
		"repositoryName":  githubv4.String(repo),
		"prNumber":        githubv4.Int(prNumber),
		"commentsLast":    githubv4.Int(100),
	}

	if err := gc.V4.Query(ctx, &commentsQuery, variables); err != nil {
		return fmt.Errorf("failed to query comments: %w", err)
	}

	// Delete comments made by the current user
	for _, edge := range commentsQuery.Repository.PullRequest.Comments.Edges {
		if string(edge.Node.Author.Login) == currentUserLogin {
			commentID := int64(edge.Node.DatabaseId)
			_, err := gc.V3.Issues.DeleteComment(ctx, owner, repo, commentID)
			if err != nil {
				return fmt.Errorf("failed to delete comment %d: %w", commentID, err)
			}
		}
	}

	return nil
}

// CheckTriggerComments scans PR comments for any that match the given prefixes
// (case-insensitive). It returns the ID of the latest matching comment (0 if
// none match) and whether that comment is newer than sinceID (i.e. a new
// trigger has arrived). Callers are responsible for suppressing the trigger
// on the first-ever check for a resource (see pr.Check), since sinceID alone
// cannot distinguish "no baseline yet" from "baseline is legitimately zero".
func (gc *GithubClient) CheckTriggerComments(ctx context.Context, prNumber int, patterns []string, sinceID int64) (latestMatchID int64, triggered bool, err error) {
	owner, repo := gc.Config.GetOwnerAndRepo()

	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		comments, resp, err := gc.V3.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return 0, false, fmt.Errorf("failed to list PR comments: %w", err)
		}

		for _, c := range comments {
			id := c.GetID()
			body := strings.TrimSpace(c.GetBody())
			for _, pattern := range patterns {
				if strings.HasPrefix(strings.ToLower(body), strings.ToLower(strings.TrimSpace(pattern))) {
					if id > latestMatchID {
						latestMatchID = id
					}
					break
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if latestMatchID == 0 {
		// No matching comments on this PR at all.
		return 0, false, nil
	}

	return latestMatchID, latestMatchID > sinceID, nil
}

// Helper functions

func (gc *GithubClient) convertPRNode(node prNode) *PullRequest {
	pr := &PullRequest{
		Number:          int(node.Number),
		Title:           string(node.Title),
		URL:             string(node.URL),
		HeadRefName:     string(node.HeadRefName),
		BaseRefName:     string(node.BaseRefName),
		Repository:      string(node.Repository.URL),
		HeadRepository:  string(node.HeadRepository.URL),
		AuthorLogin:     string(node.Author.Login),
		AuthorAvatarURL: string(node.Author.AvatarURL),
		IsDraft:         bool(node.IsDraft),
		State:           string(node.State),
	}

	// Extract labels
	for _, label := range node.Labels.Nodes {
		pr.Labels = append(pr.Labels, string(label.Name))
	}

	// Extract latest commit info
	if len(node.Commits.Nodes) > 0 {
		lastCommit := node.Commits.Nodes[0].Commit
		pr.HeadRefOID = string(lastCommit.OID)
		pr.CommittedDate = lastCommit.CommittedDate.Format(time.RFC3339)
	}

	// Count approved reviews
	approvedReviewers := make(map[string]bool)
	for _, review := range node.Reviews.Nodes {
		if string(review.State) == "APPROVED" {
			approvedReviewers[string(review.Author.Login)] = true
		}
	}
	pr.ApprovedReviewCount = len(approvedReviewers)

	return pr
}

func (gc *GithubClient) shouldSkipPR(pr *PullRequest) bool {
	// Skip drafts if configured
	if gc.Config.IgnoreDrafts && pr.IsDraft {
		return true
	}

	// Skip forks if configured
	if gc.Config.DisableForks && pr.HeadRepository != pr.Repository {
		return true
	}

	// Check required approvals
	if pr.ApprovedReviewCount < gc.GithubConfig.RequiredReviewApprovals {
		return true
	}

	// Check labels if specified
	if len(gc.Config.Labels) > 0 {
		hasLabel := false
		for _, requiredLabel := range gc.Config.Labels {
			for _, prLabel := range pr.Labels {
				if requiredLabel == prLabel {
					hasLabel = true
					break
				}
			}
			if hasLabel {
				break
			}
		}
		if !hasLabel {
			return true
		}
	}

	// Check CI skip
	if !gc.Config.DisableCISkip {
		title := strings.ToLower(pr.Title)
		if strings.Contains(title, "[ci skip]") || strings.Contains(title, "[skip ci]") {
			return true
		}
	}

	return false
}

// matchGlobPath reports whether a slash-separated file path matches a glob
// pattern. Supports **, *, ?, and [] — where ** matches zero or more path
// segments (e.g. "foo/**" matches "foo/bar/baz.tf").
func matchGlobPath(pattern, file string) bool {
	parts := strings.Split(pattern, "/")
	segs := strings.Split(file, "/")
	return matchGlobParts(parts, segs)
}

func matchGlobParts(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		// ** matches zero or more path segments.
		for i := 0; i <= len(name); i++ {
			if matchGlobParts(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	// Delegate single-segment matching to filepath.Match for *, ?, [] support.
	ok, _ := filepath.Match(pat[0], name[0])
	return ok && matchGlobParts(pat[1:], name[1:])
}

// pathMatches reports whether file matches pattern, supporting both glob
// patterns (with ** for recursive matching) and plain prefix matching.
func pathMatches(pattern, file string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		return matchGlobPath(pattern, file)
	}
	return strings.HasPrefix(file, pattern)
}

// MatchesPathFilters checks whether pr's relevant changed files match
// source.paths/ignore_paths.
//
// sinceSHA, when non-empty, is the last commit this resource previously
// recorded a version for on this exact PR. Passing it scopes the check to
// files changed between sinceSHA and the PR's current HEAD (via
// GetChangedFilesSince) — correctly answering "did the latest push change
// matching paths" rather than "has this PR, across its entire history,
// ever touched matching paths." That second, broader question is what
// GetChangedFiles always answers (GitHub's pulls/{number}/files endpoint
// returns the PR's full base...HEAD diff regardless of which commit is
// being checked) — it's used here only when sinceSHA is empty, which is
// the only option for a PR with no previously-known commit to diff from
// (e.g. appearing for the first time).
//
// If sinceSHA already equals pr.HeadRefOID, nothing has changed since the
// last check, so this returns false without an API call.
func (gc *GithubClient) MatchesPathFilters(ctx context.Context, pr *PullRequest, sinceSHA string) (bool, error) {
	// If no path filters, everything matches
	if len(gc.Config.Paths) == 0 && len(gc.Config.IgnorePaths) == 0 {
		return true, nil
	}

	if sinceSHA != "" && sinceSHA == pr.HeadRefOID {
		return false, nil
	}

	var files []string
	var err error
	if sinceSHA != "" {
		files, err = gc.GetChangedFilesSince(ctx, sinceSHA, pr.HeadRefOID)
	} else {
		files, err = gc.GetChangedFiles(ctx, pr.Number)
	}
	if err != nil {
		return false, err
	}

	return filesMatchPathFilters(gc.Config, files), nil
}

// filesMatchPathFilters applies source.paths/ignore_paths to an already-
// fetched list of changed files. Pulled out of MatchesPathFilters so the
// matching logic itself — as opposed to which files it's given — can be
// tested without any GitHub API involved.
func filesMatchPathFilters(config CommonConfig, files []string) bool {
	// Check ignore paths first: a file is ignored if it matches any ignore pattern.
	// A PR is included only when at least one changed file is not ignored.
	if len(config.IgnorePaths) > 0 {
		for _, file := range files {
			ignored := false
			for _, pattern := range config.IgnorePaths {
				if pathMatches(pattern, file) {
					ignored = true
					break
				}
			}
			if !ignored {
				// At least one non-ignored file — proceed to include-path check.
				if len(config.Paths) == 0 {
					return true
				}
				break
			}
		}
	}

	// Check include paths
	if len(config.Paths) > 0 {
		for _, file := range files {
			for _, pattern := range config.Paths {
				if pathMatches(pattern, file) {
					return true
				}
			}
		}
		return false
	}

	return true
}
