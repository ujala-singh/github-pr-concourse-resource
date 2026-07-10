package models

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

// CommonConfig contains configuration common to all resource types
type CommonConfig struct {
	Repository          string   `json:"repository"`
	AccessToken         string   `json:"access_token"`
	V3Endpoint          string   `json:"v3_endpoint"`
	V4Endpoint          string   `json:"v4_endpoint"`
	HostingEndpoint     string   `json:"hosting_endpoint"`
	SkipSSLVerification bool     `json:"skip_ssl_verification"`
	Paths               []string `json:"paths"`
	IgnorePaths         []string `json:"ignore_paths"`
	DisableCISkip       bool     `json:"disable_ci_skip"`
	DisableForks        bool     `json:"disable_forks"`
	IgnoreDrafts        bool     `json:"ignore_drafts"`
	BaseBranch          string   `json:"base_branch"`
	Labels              []string `json:"labels"`
	States              []string `json:"states"`
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
	if c.AccessToken == "" {
		return fmt.Errorf("access_token must be set")
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
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.AccessToken})

	var httpClient *http.Client
	if config.SkipSSLVerification {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}

	tc := oauth2.NewClient(ctx, ts)

	var v3Client *github.Client
	var v4Client *githubv4.Client

	// Setup V3 client
	if config.V3Endpoint != "" {
		baseURL, err := url.Parse(config.V3Endpoint)
		if err != nil {
			return nil, fmt.Errorf("failed to parse v3 endpoint: %w", err)
		}
		v3Client, err = github.NewEnterpriseClient(baseURL.String(), baseURL.String(), tc)
		if err != nil {
			return nil, fmt.Errorf("failed to create v3 client: %w", err)
		}
	} else {
		v3Client = github.NewClient(tc)
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

// Version represents a resource version
type Version struct {
	PR                  string `json:"pr"`
	Commit              string `json:"commit,omitempty"`
	CommittedDate       string `json:"committed,omitempty"`
	ApprovedReviewCount int    `json:"approved_review_count,omitempty"`
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
