package models

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
)

// generateGithubAppJWT creates a JWT for GitHub App authentication
func generateGithubAppJWT(appID string, privateKeyPEM string) (string, error) {
	// Parse the private key
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", fmt.Errorf("failed to parse private key: %w", err)
	}

	// Create the JWT claims
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    appID,
	}

	// Create the token
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)

	// Sign the token
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return tokenString, nil
}

// getInstallationToken exchanges a JWT for an installation access token
func getInstallationToken(ctx context.Context, jwtToken string, installationID string, v3Endpoint string, httpClient *http.Client) (string, error) {
	// Create a temporary client with JWT auth
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	var tempClient *github.Client
	if v3Endpoint != "" {
		var err error
		tempClient, err = github.NewClient(httpClient).WithAuthToken(jwtToken).WithEnterpriseURLs(v3Endpoint, v3Endpoint)
		if err != nil {
			return "", fmt.Errorf("failed to create temporary GitHub client: %w", err)
		}
	} else {
		tempClient = github.NewClient(httpClient).WithAuthToken(jwtToken)
	}

	// Convert installation ID to int64
	installationIDInt, err := strconv.ParseInt(installationID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse installation ID: %w", err)
	}

	// Get the installation token
	token, _, err := tempClient.Apps.CreateInstallationToken(ctx, installationIDInt, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create installation token: %w", err)
	}

	return token.GetToken(), nil
}

// getGithubAppToken generates a GitHub App installation token
func getGithubAppToken(ctx context.Context, config CommonConfig, httpClient *http.Client) (string, error) {
	// Generate JWT
	jwtToken, err := generateGithubAppJWT(config.GithubAppID, config.GithubAppPrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to generate JWT: %w", err)
	}

	// Exchange JWT for installation token
	installationToken, err := getInstallationToken(ctx, jwtToken, config.GithubAppInstallationID, config.V3Endpoint, httpClient)
	if err != nil {
		return "", fmt.Errorf("failed to get installation token: %w", err)
	}

	return installationToken, nil
}

// githubAppTokenSource implements oauth2.TokenSource for GitHub App authentication
type githubAppTokenSource struct {
	ctx        context.Context
	config     CommonConfig
	httpClient *http.Client
	token      string
	expiresAt  time.Time
}

func (ts *githubAppTokenSource) Token() (*oauth2.Token, error) {
	// Check if we need to refresh the token (refresh 5 minutes before expiry)
	if ts.token != "" && time.Now().Before(ts.expiresAt.Add(-5*time.Minute)) {
		return &oauth2.Token{AccessToken: ts.token}, nil
	}

	// Get a new installation token
	token, err := getGithubAppToken(ts.ctx, ts.config, ts.httpClient)
	if err != nil {
		return nil, err
	}

	ts.token = token
	ts.expiresAt = time.Now().Add(1 * time.Hour) // Installation tokens typically last 1 hour

	return &oauth2.Token{AccessToken: token}, nil
}
