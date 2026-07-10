package models

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPrivateKey = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAs840oDfjckrZRMjZi7XpxVsj0mOCy1dBKzFcwdKDapsRj+qY
lE7sIjY+sVVQ1VsEM6HhOCmL2VhIFwt9BrrmI1p9OOu8V/ILogTb8gOBIjpJ5JT8
iYeuEPdemjyU7Fl0f4zLnbP7eNb2MtZUE8JagkMlOqxlYBwWJcNRFWEVgz0GuXrv
Gi/hfxEiB++cVhVZVAL+WNEpdfuqHWQFw2c6gscxaG1XUS5vwZhx4/a9VkjwwhgG
3iL18U59q6aNGHfinJkTQfSS2Ig8lVr1cgCk6qdduR+gG1IztAslSqOX9WiVy9Zq
dzhgZsVH3JVAovF9MIzqMIGhqiVlMl6X5HQJFwIDAQABAoIBAQCELgi955gKwz9p
s4VJkZei/9cbqQ/Tz/cWe5lG2yzEx+5nL/yuuj4ZAGuiDaf40IoMMurQUKqAQsfs
OQPWWLsqLjF0EMhKlqM6nfvas/zQXq1Hnsbvi5DI5DDljbek8DYWNjjRXCh3sv8W
bD0usWe77wSFV4rG1p7pb+ZBozcfXBaVtTFv/IBaGMNR/d2XSUuXAwSWxhmybQTo
IYrQ0hGGG9AdUWItInuT51dscMDIn2gP62Y/XiIC8js9xPj08U8ez2d6SNy83jl8
iHOFrTro+n6Tp04nfievmD3LfwjByuiqhlF/rQtwpw/KAsjq+C0SoA3vtLr4h65R
C3g7WAhBAoGBAOxJWGGpecZL8hVC0gxjVCYBdG7rcUzY+R5JxU8fPHk4T6Lnhpbn
XGbur59nIHpISr4ZVGL0VNmArFWZ+pYNF8VaE77PV4TpLmTkMHAgEQbkv4J+aU4l
eTaC7YgaxFjgOEJlvH2wwAc03wjSZNNza4Vcd6kPqXcMIHiAMw3LufHHAoGBAMLO
hwbyMbXH1UzAsDBUJhdL+bIdn7U0ZDQf2M7AqiRjnViTJqqoF7lngWl1qaGu3gv6
xxJuxXa76mZNX+Fwjl/Zf92t29BG7NDNM31ppux47XsFHptAlmRylzFtkcsquKvJ
6vk6/r7TMn6VySgROBzRUxdoLn+rz1kchKcgPS4xAoGAfRItyDQvEzmsAHkIOipx
plRqzzOtG2JWKyQdXs5H8lpOPQqUgVgh3xJEv/mUhWWyuoEp29888oxbrEv/CmIP
zRTrErsptl6/ggQPZ6pxmNaIUIidMRJA4QvYs4yHlgvJe8viRB3E54ui60aCvDKC
HWteo4x4xV0T6vThEVJfMI0CgYBWDM5+Vft5VaU1uyPYpUMSJWBNumIys8rTb4Hg
iiBd5Ja7any5A3k/T6ZNhEkC/3BcEFFhJgcZlJZMzD7fIU3yruuZa1Peo4W2Ef59
lm7CpAQaxD8pyxTjl+6LSeANw3hBgfbGUrX2auoyGk354elMaXZvr3hisuzravp5
rHb58QKBgQCdxsUeSeMoH12bez98I2eE+ELQMKNEnF4C11k7L64qHZXab44DpkHS
Amfa3i3uTUn1cmkExtN7e5DNhWOzslX6jBEIgZ2iB4WWyzabMU1HmYSsCwAHCiYd
HLDzMFwJLq4H/gVxasnv56MxHDgy2vMUD8sOT0PKXHmQnsAtL5Kz8w==
-----END RSA PRIVATE KEY-----`

func TestGenerateGithubAppJWT(t *testing.T) {
	tests := []struct {
		name          string
		appID         string
		privateKey    string
		wantErr       bool
		errContains   string
		validateToken bool
	}{
		{
			name:          "valid JWT generation",
			appID:         "12345",
			privateKey:    testPrivateKey,
			wantErr:       false,
			validateToken: true,
		},
		{
			name:        "invalid private key",
			appID:       "12345",
			privateKey:  "invalid-key",
			wantErr:     true,
			errContains: "failed to parse private key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := generateGithubAppJWT(tt.appID, tt.privateKey)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, token)

			if tt.validateToken {
				// Parse the JWT to verify it's valid
				parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
					// Verify signing method
					if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
						return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
					}
					// Parse public key from private key for validation
					key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(tt.privateKey))
					if err != nil {
						return nil, err
					}
					return &key.PublicKey, nil
				})

				require.NoError(t, err)
				assert.True(t, parsedToken.Valid)

				// Verify claims
				claims, ok := parsedToken.Claims.(jwt.MapClaims)
				require.True(t, ok)

				assert.Equal(t, tt.appID, claims["iss"])

				// Verify expiration is in the future
				exp := claims["exp"].(float64)
				assert.True(t, time.Now().Unix() < int64(exp))

				// Verify issued at is in the past
				iat := claims["iat"].(float64)
				assert.True(t, time.Now().Unix() >= int64(iat))
			}
		})
	}
}

func TestGetInstallationToken(t *testing.T) {
	t.Run("successful token retrieval", func(t *testing.T) {
		// Create mock server
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify JWT is in Authorization header
			auth := r.Header.Get("Authorization")
			assert.True(t, strings.HasPrefix(auth, "Bearer "))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"token": "ghs_test_installation_token", "expires_at": "2024-12-31T23:59:59Z"}`)
		}))
		defer server.Close()

		ctx := context.Background()
		jwtToken := "test-jwt-token"
		installationID := "12345"

		token, err := getInstallationToken(ctx, jwtToken, installationID, server.URL, nil)
		require.NoError(t, err)
		assert.Equal(t, "ghs_test_installation_token", token)
	})

	t.Run("invalid installation ID", func(t *testing.T) {
		ctx := context.Background()
		jwtToken := "test-jwt-token"
		installationID := "not-a-number"

		_, err := getInstallationToken(ctx, jwtToken, installationID, "", nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse installation ID")
	})
}

func TestGithubAppTokenSource(t *testing.T) {
	t.Run("token caching logic", func(t *testing.T) {
		// Create a mock config
		config := CommonConfig{
			Repository:              "owner/repo",
			GithubAppID:             "12345",
			GithubAppInstallationID: "67890",
			GithubAppPrivateKey:     testPrivateKey,
		}

		ts := &githubAppTokenSource{
			ctx:    context.Background(),
			config: config,
		}

		// First call should attempt to generate a new token
		// This will fail because we don't have a real GitHub App, but we can test the caching logic
		_, err := ts.Token()
		// Expected to fail without real GitHub App credentials
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get installation token")

		// Manually set a token to test caching
		ts.token = "test-token"
		ts.expiresAt = time.Now().Add(1 * time.Hour)

		// Second call within 55 minutes should return cached token
		token, err := ts.Token()
		require.NoError(t, err)
		assert.Equal(t, "test-token", token.AccessToken)

		// Simulate token near expiry
		ts.expiresAt = time.Now().Add(4 * time.Minute)

		// This should trigger a refresh attempt (will fail without real credentials)
		_, err = ts.Token()
		assert.Error(t, err) // Expected to fail without real GitHub App
	})
}

func TestNewGithubClientWithGithubApp(t *testing.T) {
	t.Run("creates client with GitHub App auth", func(t *testing.T) {
		config := CommonConfig{
			Repository:              "owner/repo",
			GithubAppID:             "12345",
			GithubAppInstallationID: "67890",
			GithubAppPrivateKey:     testPrivateKey,
		}

		githubConfig := GithubConfig{}

		client, err := NewGithubClient(config, githubConfig)

		// Note: This will fail without a real GitHub App, but we can verify structure
		// In a real scenario, you'd mock the GitHub API
		if err != nil {
			// Expected to fail without real credentials, but verify it tried to use GitHub App
			assert.Contains(t, err.Error(), "installation token", "Should attempt GitHub App authentication")
		} else {
			require.NotNil(t, client)
			assert.NotNil(t, client.V3)
			assert.NotNil(t, client.V4)
		}
	})

	t.Run("creates client with access token", func(t *testing.T) {
		config := CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "ghp_test_token",
		}

		githubConfig := GithubConfig{}

		client, err := NewGithubClient(config, githubConfig)
		require.NoError(t, err)
		require.NotNil(t, client)
		assert.NotNil(t, client.V3)
		assert.NotNil(t, client.V4)
		assert.Equal(t, config, client.Config)
	})
}

func TestCommonConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      CommonConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid with access token",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "ghp_token",
			},
			wantErr: false,
		},
		{
			name: "valid with GitHub App credentials",
			config: CommonConfig{
				Repository:              "owner/repo",
				GithubAppID:             "12345",
				GithubAppInstallationID: "67890",
				GithubAppPrivateKey:     "private-key",
			},
			wantErr: false,
		},
		{
			name: "no authentication",
			config: CommonConfig{
				Repository: "owner/repo",
			},
			wantErr:     true,
			errContains: "either access_token or github_app credentials",
		},
		{
			name: "both authentication methods",
			config: CommonConfig{
				Repository:              "owner/repo",
				AccessToken:             "ghp_token",
				GithubAppID:             "12345",
				GithubAppInstallationID: "67890",
				GithubAppPrivateKey:     "private-key",
			},
			wantErr:     true,
			errContains: "cannot use both access_token and github_app",
		},
		{
			name: "incomplete GitHub App credentials - missing app_id",
			config: CommonConfig{
				Repository:              "owner/repo",
				GithubAppInstallationID: "67890",
				GithubAppPrivateKey:     "private-key",
			},
			wantErr:     true,
			errContains: "either access_token or github_app credentials",
		},
		{
			name: "incomplete GitHub App credentials - missing installation_id",
			config: CommonConfig{
				Repository:          "owner/repo",
				GithubAppID:         "12345",
				GithubAppPrivateKey: "private-key",
			},
			wantErr:     true,
			errContains: "either access_token or github_app credentials",
		},
		{
			name: "incomplete GitHub App credentials - missing private_key",
			config: CommonConfig{
				Repository:              "owner/repo",
				GithubAppID:             "12345",
				GithubAppInstallationID: "67890",
			},
			wantErr:     true,
			errContains: "either access_token or github_app credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGithubClient_GetAccessToken(t *testing.T) {
	t.Run("returns access token when configured with PAT", func(t *testing.T) {
		config := CommonConfig{
			Repository:  "owner/repo",
			AccessToken: "ghp_test_personal_access_token",
		}
		githubConfig := GithubConfig{}

		client, err := NewGithubClient(config, githubConfig)
		require.NoError(t, err)

		token, err := client.GetAccessToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ghp_test_personal_access_token", token)
	})

	t.Run("generates installation token when configured with GitHub App", func(t *testing.T) {
		// Create mock server for GitHub App API
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Verify JWT is in Authorization header
			auth := r.Header.Get("Authorization")
			assert.True(t, strings.HasPrefix(auth, "Bearer "))

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintln(w, `{"token": "ghs_test_installation_token", "expires_at": "2024-12-31T23:59:59Z"}`)
		}))
		defer server.Close()

		config := CommonConfig{
			Repository:              "owner/repo",
			GithubAppID:             "12345",
			GithubAppInstallationID: "67890",
			GithubAppPrivateKey:     testPrivateKey,
			V3Endpoint:              server.URL,
			V4Endpoint:              server.URL + "/graphql",
			HostingEndpoint:         server.URL,
		}
		githubConfig := GithubConfig{}

		client, err := NewGithubClient(config, githubConfig)
		require.NoError(t, err)

		token, err := client.GetAccessToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "ghs_test_installation_token", token)
	})

	t.Run("returns error when no authentication method configured", func(t *testing.T) {
		// Create a client with no authentication (this shouldn't be possible via NewGithubClient
		// due to validation, but we test the GetAccessToken logic directly)
		client := &GithubClient{
			Config: CommonConfig{
				Repository: "owner/repo",
			},
		}

		_, err := client.GetAccessToken(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no authentication method configured")
	})
}
