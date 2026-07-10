package models

import (
	"context"
	"fmt"
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

// UpdateCommitStatus updates the status of a commit
func (gc *GithubClient) UpdateCommitStatus(ctx context.Context, sha, state, targetURL, description, context string) error {
	owner, repo := gc.Config.GetOwnerAndRepo()

	status := &github.RepoStatus{
		State:       github.String(state),
		TargetURL:   github.String(targetURL),
		Description: github.String(description),
		Context:     github.String(context),
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

// MatchesPathFilters checks if the PR changes match the path filters
func (gc *GithubClient) MatchesPathFilters(ctx context.Context, pr *PullRequest) (bool, error) {
	// If no path filters, everything matches
	if len(gc.Config.Paths) == 0 && len(gc.Config.IgnorePaths) == 0 {
		return true, nil
	}

	files, err := gc.GetChangedFiles(ctx, pr.Number)
	if err != nil {
		return false, err
	}

	// Check ignore paths first
	if len(gc.Config.IgnorePaths) > 0 {
		for _, file := range files {
			ignored := false
			for _, pattern := range gc.Config.IgnorePaths {
				// Check if it's a prefix match
				if strings.HasPrefix(file, pattern) {
					ignored = true
					break
				}
				// Check if it's a glob pattern
				if matched, _ := filepath.Match(pattern, file); matched {
					ignored = true
					break
				}
			}
			if !ignored {
				// Found at least one file that's not ignored
				if len(gc.Config.Paths) == 0 {
					return true, nil
				}
			}
		}
	}

	// Check include paths
	if len(gc.Config.Paths) > 0 {
		for _, file := range files {
			for _, pattern := range gc.Config.Paths {
				// Check if it's a prefix match
				if strings.HasPrefix(file, pattern) {
					return true, nil
				}
				// Check if it's a glob pattern
				if matched, _ := filepath.Match(pattern, file); matched {
					return true, nil
				}
			}
		}
		return false, nil
	}

	return true, nil
}
