package models

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// CommonConfig contains configuration common to all resource types
type CommonConfig struct {
	Repository              string   `json:"repository"`
	AccessToken             string   `json:"access_token"`
	V3Endpoint              string   `json:"v3_endpoint"`
	V4Endpoint              string   `json:"v4_endpoint"`
	HostingEndpoint         string   `json:"hosting_endpoint"`
	GithubAppID             string   `json:"github_app_id"`
	GithubAppInstallationID string   `json:"github_app_installation_id"`
	GithubAppPrivateKey     string   `json:"github_app_private_key"`
	SkipSSLVerification     bool     `json:"skip_ssl_verification"`
	Paths                   []string `json:"paths"`
	IgnorePaths             []string `json:"ignore_paths"`
	DisableCISkip           bool     `json:"disable_ci_skip"`
	DisableForks            bool     `json:"disable_forks"`
	IgnoreDrafts            bool     `json:"ignore_drafts"`
	BaseBranch              string   `json:"base_branch"`
	Labels                  []string `json:"labels"`
	States                  []string `json:"states"`
	// ConcourseURL overrides ATC_EXTERNAL_URL when constructing PR status check target
	// URLs. Use when Concourse's externalUrl Helm value cannot be changed (e.g. it is
	// locked to an STS WebIdentity OIDC issuer) but builds are reached via a different
	// public hostname (e.g. a Teleport proxy).
	ConcourseURL string `json:"concourse_url"`
	// TriggerComments lists comment prefixes (case-insensitive) that re-trigger the
	// pipeline job as if a new commit had arrived. Useful for manually re-running
	// plans via a PR comment such as "concourse plan --all".
	//
	// Supported in both single-PR mode (pr package) and PR-list mode (prlist
	// package). In list mode this is best-effort: the resource's version
	// stream only tracks a single cursor across many PRs, so once the cursor
	// moves on to a different PR, that PR's comment watermark is lost until
	// it's re-established — see prlist.applyCommentTriggers.
	TriggerComments []string `json:"trigger_comments"`
}

// GithubConfig contains GitHub-specific configuration
type GithubConfig struct {
	RequiredReviewApprovals int    `json:"required_review_approvals"`
	GitCryptKey             string `json:"git_crypt_key"`
	DisableGitLFS           bool   `json:"disable_git_lfs"`
}

// Validate checks if the configuration is valid
func (c *CommonConfig) Validate() error {
	if c.Repository == "" {
		return fmt.Errorf("repository must be set")
	}

	// Check authentication method
	hasToken := c.AccessToken != ""
	hasGithubApp := c.GithubAppID != "" && c.GithubAppInstallationID != "" && c.GithubAppPrivateKey != ""

	if !hasToken && !hasGithubApp {
		return fmt.Errorf("either access_token or github_app credentials (github_app_id, github_app_installation_id, github_app_private_key) must be set")
	}

	if hasToken && hasGithubApp {
		return fmt.Errorf("cannot use both access_token and github_app authentication")
	}

	// Validate GitHub App fields if using GitHub App auth
	if hasGithubApp {
		if c.GithubAppID == "" {
			return fmt.Errorf("github_app_id must be set when using GitHub App authentication")
		}
		if c.GithubAppInstallationID == "" {
			return fmt.Errorf("github_app_installation_id must be set when using GitHub App authentication")
		}
		if c.GithubAppPrivateKey == "" {
			return fmt.Errorf("github_app_private_key must be set when using GitHub App authentication")
		}
	}

	// Validate repository format
	parts := strings.Split(c.Repository, "/")
	if len(parts) != 2 {
		return fmt.Errorf("repository must be in owner/repo format")
	}

	// Validate endpoints consistency
	hasV3 := c.V3Endpoint != ""
	hasV4 := c.V4Endpoint != ""
	hasHosting := c.HostingEndpoint != ""

	if hasV3 || hasV4 || hasHosting {
		if !hasV3 || !hasV4 || !hasHosting {
			return fmt.Errorf("if any of hosting_endpoint, v3_endpoint, or v4_endpoint are set, all must be set")
		}
	}

	// Validate states
	if len(c.States) > 0 {
		validStates := map[string]bool{"OPEN": true, "MERGED": true, "CLOSED": true}
		for _, state := range c.States {
			if !validStates[strings.ToUpper(state)] {
				return fmt.Errorf("invalid state: %s (must be OPEN, MERGED, or CLOSED)", state)
			}
		}
	}

	return nil
}

// GetOwnerAndRepo returns the owner and repository name
func (c *CommonConfig) GetOwnerAndRepo() (string, string) {
	parts := strings.Split(c.Repository, "/")
	return parts[0], parts[1]
}

// GithubClient wraps the GitHub API clients
type GithubClient struct {
	V3           *github.Client
	V4           *githubv4.Client
	Config       CommonConfig
	GithubConfig GithubConfig
}

// NewGithubClient creates a new GitHub client with the given configuration
func NewGithubClient(config CommonConfig, githubConfig GithubConfig) (*GithubClient, error) {
	ctx := context.Background()

	var httpClient *http.Client
	if config.SkipSSLVerification {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}

	// Determine which authentication method to use
	var ts oauth2.TokenSource
	if config.GithubAppID != "" && config.GithubAppInstallationID != "" && config.GithubAppPrivateKey != "" {
		// Use GitHub App authentication
		ts = &githubAppTokenSource{
			ctx:        ctx,
			config:     config,
			httpClient: httpClient,
		}
	} else {
		// Use personal access token
		ts = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.AccessToken})
	}

	// Create OAuth2 client
	var tc *http.Client
	if httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}
	tc = oauth2.NewClient(ctx, ts)

	var v3Client *github.Client
	var v4Client *githubv4.Client

	// Setup V3 client
	v3Client = github.NewClient(tc)
	if config.V3Endpoint != "" {
		baseURL, err := url.Parse(config.V3Endpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to parse v3 endpoint: %w", err)
		}
		v3Client, err = v3Client.WithEnterpriseURLs(baseURL.String(), baseURL.String())
		if err != nil {
			return nil, fmt.Errorf("failed to set enterprise URLs: %w", err)
		}
	}

	// Setup V4 client
	if config.V4Endpoint != "" {
		v4Client = githubv4.NewEnterpriseClient(config.V4Endpoint, tc)
	} else {
		v4Client = githubv4.NewClient(tc)
	}

	return &GithubClient{
		V3:           v3Client,
		V4:           v4Client,
		Config:       config,
		GithubConfig: githubConfig,
	}, nil
}

// GetAccessToken returns a valid access token for git operations
func (c *GithubClient) GetAccessToken(ctx context.Context) (string, error) {
	// If using personal access token, return it directly
	if c.Config.AccessToken != "" {
		return c.Config.AccessToken, nil
	}

	// If using GitHub App, generate an installation token
	if c.Config.GithubAppID != "" && c.Config.GithubAppInstallationID != "" && c.Config.GithubAppPrivateKey != "" {
		return getGithubAppToken(ctx, c.Config, nil)
	}

	return "", fmt.Errorf("no authentication method configured")
}

// Version represents a resource version.
//
// Concourse's resource protocol requires every version to be a flat JSON
// object of STRING values (ATC decodes it as map[string]string). A field
// that serializes as a native bool or number — e.g. a populated
// ApprovedReviewCount, CommentID, or CommentBaseline — makes ATC fail check
// entirely with "json: cannot unmarshal <type> into Go value of type
// string". MarshalJSON/UnmarshalJSON below encode/decode every field
// through its string form so this struct can use normal Go types
// internally while staying protocol-compliant on the wire.
type Version struct {
	PR                  string `json:"pr"`
	Commit              string `json:"commit,omitempty"`
	CommittedDate       string `json:"committed,omitempty"`
	ApprovedReviewCount int    `json:"-"`
	// CommentID is the highest trigger-comment ID observed on the PR as of
	// this version. It acts as a watermark so the same comment never
	// triggers more than one build. Zero is a legitimate value (no matching
	// comment has been posted yet) — use CommentBaseline to tell that apart
	// from "the comment-trigger check has never run for this resource".
	CommentID int64 `json:"-"`
	// CommentBaseline is true once the comment-trigger watermark above has
	// been established at least once. Until then, CommentID == 0 is
	// ambiguous (never checked vs. checked-and-found-nothing).
	CommentBaseline bool `json:"-"`
}

// MarshalJSON encodes the version as map[string]string, as required by the
// Concourse resource protocol.
func (v Version) MarshalJSON() ([]byte, error) {
	m := map[string]string{
		"pr": v.PR,
	}
	if v.Commit != "" {
		m["commit"] = v.Commit
	}
	if v.CommittedDate != "" {
		m["committed"] = v.CommittedDate
	}
	if v.ApprovedReviewCount != 0 {
		m["approved_review_count"] = strconv.Itoa(v.ApprovedReviewCount)
	}
	if v.CommentID != 0 {
		m["comment_id"] = strconv.FormatInt(v.CommentID, 10)
	}
	if v.CommentBaseline {
		m["comment_baseline"] = "true"
	}
	return json.Marshal(m)
}

// UnmarshalJSON decodes a version from its map[string]string wire form back
// into native Go types.
func (v *Version) UnmarshalJSON(data []byte) error {
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	v.PR = m["pr"]
	v.Commit = m["commit"]
	v.CommittedDate = m["committed"]
	v.CommentBaseline = m["comment_baseline"] == "true"

	if s, ok := m["approved_review_count"]; ok && s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("invalid approved_review_count %q: %w", s, err)
		}
		v.ApprovedReviewCount = n
	}

	if s, ok := m["comment_id"]; ok && s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid comment_id %q: %w", s, err)
		}
		v.CommentID = n
	}

	return nil
}

// Metadata represents resource metadata
type Metadata struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PullRequest represents a GitHub pull request with all relevant information
type PullRequest struct {
	Number              int
	Title               string
	URL                 string
	HeadRefName         string
	HeadRefOID          string
	BaseRefName         string
	BaseRefOID          string
	Repository          string
	HeadRepository      string
	AuthorLogin         string
	AuthorAvatarURL     string
	IsDraft             bool
	State               string
	CommittedDate       string
	ApprovedReviewCount int
	Labels              []string
	ChangedFiles        []string
}
