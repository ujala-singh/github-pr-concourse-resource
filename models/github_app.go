package models

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
)

// parseGithubAppPrivateKey accepts github_app_private_key in either of two
// forms and validates it eagerly rather than letting a bad key surface only
// once check/in/out actually try to sign a JWT with it:
//
//  1. A raw PEM string (the original, still-supported form).
//  2. That same PEM, base64-encoded — useful when the key is stored in a
//     secrets manager or credential store that mangles multi-line values
//     (embedded newlines collapsed, trailing whitespace trimmed, etc.).
//     Matches the pattern this org's git-app resource type already uses
//     (base64_github_app_pem in its Secrets Manager payload).
//
// It tries the value as raw PEM first, then strips whitespace (so a
// base64 blob copy-pasted across multiple lines still decodes) and tries
// again as base64-encoded PEM. Both attempts failing is reported as a
// single error covering both forms, so a misconfigured key is caught here
// rather than several calls deep inside JWT signing.
func parseGithubAppPrivateKey(value string) (*rsa.PrivateKey, error) {
	trimmed := strings.TrimSpace(value)

	if key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(trimmed)); err == nil {
		return key, nil
	}

	cleaned := strings.Join(strings.Fields(trimmed), "")
	decoded, decodeErr := base64.StdEncoding.DecodeString(cleaned)
	if decodeErr != nil {
		return nil, fmt.Errorf("github_app_private_key is neither a valid PEM key nor valid base64 (base64 decode error: %v)", decodeErr)
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM(decoded)
	if err != nil {
		return nil, fmt.Errorf("github_app_private_key is neither a valid PEM key nor a base64-encoded PEM key: %w", err)
	}

	return key, nil
}

// generateGithubAppJWT creates a JWT for GitHub App authentication
func generateGithubAppJWT(appID string, privateKeyPEM string) (string, error) {
	// Parse the private key (raw PEM or base64-encoded PEM — see
	// parseGithubAppPrivateKey)
	privateKey, err := parseGithubAppPrivateKey(privateKeyPEM)
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

// githubAppTokenSource implements oauth2.TokenSource for GitHub App
// authentication. oauth2.NewClient wraps whatever TokenSource it's given in
// its own mutex-protected reuseTokenSource before handing back an
// http.Client — and models.NewGithubClient is the only place this type gets
// constructed, always immediately passed into oauth2.NewClient — so in
// today's call graph, Token() is never actually invoked concurrently. But
// the oauth2.TokenSource interface itself documents that implementations
// must tolerate concurrent calls, and that external protection is an
// implementation detail of a different package, not something this type can
// rely on holding forever. mu makes it correct on its own terms.
type githubAppTokenSource struct {
	ctx        context.Context
	config     CommonConfig
	httpClient *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func (ts *githubAppTokenSource) Token() (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Check if we need to refresh the token (refresh 5 minutes before expiry)
	if ts.token != "" && time.Now().Before(ts.expiresAt.Add(-5*time.Minute)) {
		return &oauth2.Token{AccessToken: ts.token}, nil
	}

	// Get a new installation token. Holding mu across this network call is
	// deliberate: it also fixes a thundering-herd problem where N
	// concurrent callers hitting an expired token would otherwise each mint
	// their own fresh installation token instead of N-1 of them simply
	// waiting for the first refresh and reusing its result.
	token, err := getGithubAppToken(ts.ctx, ts.config, ts.httpClient)
	if err != nil {
		return nil, err
	}

	ts.token = token
	ts.expiresAt = time.Now().Add(1 * time.Hour) // Installation tokens typically last 1 hour

	return &oauth2.Token{AccessToken: token}, nil
}
