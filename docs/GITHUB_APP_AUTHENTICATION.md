# GitHub App Authentication

This Concourse resource supports authentication using GitHub Apps in addition to personal access tokens.

## Why Use GitHub Apps?

GitHub Apps offer several advantages over personal access tokens:

- **Higher rate limits**: GitHub Apps receive their own rate limit quota
- **Better security**: Fine-grained permissions and installation-specific access
- **Organization-wide**: Can be installed once and used across multiple repositories
- **Audit trail**: Actions appear as the GitHub App, not individual users
- **Token rotation**: Installation tokens automatically expire after 1 hour

## Creating a GitHub App

1. Go to your GitHub account/organization settings
2. Navigate to **Developer settings** → **GitHub Apps** → **New GitHub App**
3. Fill in the required fields:
   - **GitHub App name**: Give your app a unique name
   - **Homepage URL**: Your organization or project URL
   - **Webhook**: Can be left unchecked if not needed
4. Set **Repository permissions**:
   - **Contents**: Read (for cloning repositories)
   - **Pull requests**: Read & Write (for reading PRs and posting comments)
   - **Commit statuses**: Read & Write (for setting commit statuses)
5. Click **Create GitHub App**
6. Note the **App ID** shown on the next page
7. Generate a private key:
   - Scroll down to **Private keys**
   - Click **Generate a private key**
   - Save the downloaded `.pem` file securely

## Installing the GitHub App

1. On the GitHub App's settings page, click **Install App**
2. Select the organization or account where you want to install it
3. Choose whether to install on all repositories or select specific ones
4. After installation, note the **Installation ID** from the URL
   - The URL will be like: `https://github.com/settings/installations/12345678`
   - The number at the end is your installation ID

## Configuration

Configure the Concourse resource with GitHub App credentials:

```yaml
resources:
  - name: pr-resource
    type: github-pr-concourse-resource
    source:
      repository: owner/repo
      github_app_id: "12345"                    # Your App ID
      github_app_installation_id: "67890"       # Your Installation ID
      github_app_private_key: |                 # Contents of the .pem file
        -----BEGIN RSA PRIVATE KEY-----
        MIIEpAIBAAKCAQEA...
        ...
        -----END RSA PRIVATE KEY-----
```

### Using Credential Manager

Store the private key securely using a credential manager:

```yaml
resources:
  - name: pr-resource
    type: github-pr-concourse-resource
    source:
      repository: owner/repo
      github_app_id: ((github-app-id))
      github_app_installation_id: ((github-app-installation-id))
      github_app_private_key: ((github-app-private-key))
```

### GitHub Enterprise

For GitHub Enterprise Server, also specify the endpoints:

```yaml
resources:
  - name: pr-resource
    type: github-pr-concourse-resource
    source:
      repository: owner/repo
      github_app_id: ((github-app-id))
      github_app_installation_id: ((github-app-installation-id))
      github_app_private_key: ((github-app-private-key))
      v3_endpoint: https://github.enterprise.com/api/v3/
      v4_endpoint: https://github.enterprise.com/api/graphql
      hosting_endpoint: https://github.enterprise.com
```

## Authentication Methods

You must use **either** a personal access token **or** GitHub App credentials, not both.

### Personal Access Token

```yaml
source:
  repository: owner/repo
  access_token: ghp_your_token_here
```

### GitHub App (recommended)

```yaml
source:
  repository: owner/repo
  github_app_id: "12345"
  github_app_installation_id: "67890"
  github_app_private_key: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
```

## Troubleshooting

### "Integration not found" error

This usually means:
- The App ID is incorrect
- The private key doesn't match the app
- The app hasn't been installed on the repository

### "Installation not found" error

This usually means:
- The Installation ID is incorrect
- The app isn't installed on the specified repository
- The app was uninstalled

### Token refresh

Installation tokens automatically expire after 1 hour. The resource automatically:
- Generates a JWT using the App ID and private key
- Exchanges the JWT for an installation access token
- Caches the token and refreshes it before expiry

You don't need to manually refresh tokens.

## Permissions Required

The GitHub App needs these permissions:

| Permission | Access Level | Required For |
|-----------|--------------|--------------|
| Contents | Read | Cloning repositories, reading files |
| Pull requests | Read & Write | Reading PR data, posting comments |
| Commit statuses | Read & Write | Setting commit status (e.g., pending, success) |
| Metadata | Read | Basic repository metadata (automatic) |
