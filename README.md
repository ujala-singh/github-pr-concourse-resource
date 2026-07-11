# GitHub PR Concourse Resource

[![CI Status](https://github.com/ujala-singh/github-pr-concourse-resource/actions/workflows/ci.yml/badge.svg)](https://github.com/ujala-singh/github-pr-concourse-resource/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![GHCR](https://img.shields.io/badge/GHCR-ghcr.io-2088FF?style=flat&logo=github)](https://github.com/ujala-singh/github-pr-concourse-resource/pkgs/container/github-pr-concourse-resource)
[![Attestations](https://img.shields.io/badge/Attestations-SLSA%20Provenance-green?style=flat&logo=github)](https://github.com/ujala-singh/github-pr-concourse-resource/attestations)

A modern, feature-rich Concourse CI resource for GitHub Pull Requests with dual-mode support and GitHub App authentication.

## 🌟 Features

### ✨ Dual Mode Operation
- **PR List Mode**: Track all PRs matching criteria (perfect for instance pipelines)
- **Single PR Mode**: Track commits to a specific PR (ideal for commit-based testing)

### 🔐 Advanced Security & Authentication
- **GitHub App Authentication** (recommended) - No shared tokens, automatic token rotation
- Personal Access Token support
- Required review approvals enforcement
- Draft PR filtering
- Fork repository filtering  

### 🎯 Smart Filtering & Integration
- **Path-based filtering** with glob patterns (include/exclude)
- **Label-based filtering** for workflow control
- Base branch targeting
- PR state filtering (OPEN, MERGED, CLOSED)
- Multiple integration strategies (merge, rebase, checkout)
- Commit status updates
- PR comments
- Changed files detection

### ⚡ Performance & Flexibility
- Shallow cloning support
- Git submodules support
- Git LFS support
- Configurable git depth
- Multi-architecture support (amd64, arm64)

## 📦 Installation

### Option 1: GitHub Container Registry (Recommended)

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: ghcr.io/ujala-singh/github-pr-concourse-resource
      tag: latest
      # Or use a specific version tag:
      # tag: v1.0.0
```

### Option 2: Docker Hub

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: jolly3/github-pr-concourse-resource
      tag: latest
```

### Option 3: Building from Source

**Requirements:**
- Go 1.23 or higher
- Docker (for containerization)

```bash
# Clone the repository
git clone https://github.com/ujala-singh/github-pr-concourse-resource.git
cd github-pr-concourse-resource

# Build Docker image
docker build -t github-pr-concourse-resource:latest .

# Run tests
go test -v -race -cover ./...

# Push to your registry
docker tag github-pr-concourse-resource:latest your-registry.com/github-pr-concourse-resource:latest
docker push your-registry.com/github-pr-concourse-resource:latest
```

## 📋 Prerequisites

- Concourse CI 7.0 or higher
- GitHub repository with appropriate permissions
- One of the following authentication methods:
  - **GitHub App** (recommended) - [Setup Guide](docs/GITHUB_APP_AUTHENTICATION.md)
  - Personal Access Token with `repo` scope

## 🚀 Quick Start

See our [Quick Start Guide](docs/QUICKSTART.md) for a step-by-step tutorial.

> 📋 **Looking for more examples?**
> - [Complete Pipeline Template](examples/pipeline.yml) - Ready-to-use pipeline with all features
> - [Real-World Examples](docs/EXAMPLES.md) - 13 detailed scenarios with best practices

### Minimal Working Example

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: ghcr.io/ujala-singh/github-pr-concourse-resource

resources:
  - name: pull-requests
    type: github-pr
    source:
      repository: owner/repo
      access_token: ((github-token))

jobs:
  - name: test-pr
    plan:
      - get: pull-requests
        trigger: true
      - task: run-tests
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
          inputs:
            - name: pull-requests
          run:
            path: sh
            args:
              - -c
              - |
                cd pull-requests
                go test ./...
```

## 🎭 Modes of Operation

### Mode 1: PR List (Instance Pipelines)

Track a list of PRs matching your criteria. Perfect for creating instance pipelines where each PR gets its own pipeline.

**When to use**: You want to create separate pipelines for each PR, or you want to trigger on the creation/closure of PRs rather than individual commits.

```yaml
resources:
  - name: prs
    type: github-pr
    source:
      repository: owner/repo
      access_token: ((github-token))
      # No 'number' field = PR List mode
      base_branch: main
      labels: ["ready-for-review"]
      required_review_approvals: 1
```

### Mode 2: Single PR (Commit Tracking)

Track commits to a specific PR number. Perfect for running tests on each commit to a PR.

**When to use**: You have a known PR number and want to run tests on every commit to that PR.

```yaml
resources:
  - name: pr-123
    type: github-pr
    source:
      repository: owner/repo
      access_token: ((github-token))
      number: 123  # Specific PR number = Single PR mode
```

## Source Configuration

### Authentication

You can authenticate with either a **personal access token** or **GitHub App credentials** (recommended).

#### Personal Access Token

```yaml
source:
  repository: owner/repo
  access_token: ((github-token))
```

#### GitHub App (Recommended)

```yaml
source:
  repository: owner/repo
  github_app_id: ((github-app-id))
  github_app_installation_id: ((github-app-installation-id))
  github_app_private_key: ((github-app-private-key))
```

See [docs/GITHUB_APP_AUTHENTICATION.md](docs/GITHUB_APP_AUTHENTICATION.md) for detailed setup instructions.

### Common Configuration (Both Modes)

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `repository` | Yes | - | Repository in `owner/repo` format |
| `access_token` | No* | - | GitHub personal access token with `repo` scope |
| `github_app_id` | No* | - | GitHub App ID |
| `github_app_installation_id` | No* | - | GitHub App Installation ID |
| `github_app_private_key` | No* | - | GitHub App private key (PEM format) |
| `v3_endpoint` | No | `https://api.github.com` | GitHub API v3 endpoint (for GitHub Enterprise) |
| `v4_endpoint` | No | `https://api.github.com/graphql` | GitHub API v4 endpoint (for GitHub Enterprise) |
| `hosting_endpoint` | No | `https://github.com` | GitHub hosting endpoint (for GitHub Enterprise) |
| `paths` | No | `[]` | Only trigger on PRs that change files matching these patterns |
| `ignore_paths` | No | `[]` | Ignore PRs that only change files matching these patterns |
| `disable_ci_skip` | No | `false` | If `false`, PRs with `[ci skip]` or `[skip ci]` in title are skipped |
| `skip_ssl_verification` | No | `false` | Skip SSL certificate verification (use with caution!) |
| `disable_forks` | No | `false` | Only trigger on PRs from the same repository |
| `ignore_drafts` | No | `false` | Skip draft PRs |
| `base_branch` | No | - | Only trigger on PRs targeting this branch |
| `labels` | No | `[]` | Only trigger on PRs with at least one of these labels |
| `states` | No | `["OPEN"]` | PR states to track: `OPEN`, `MERGED`, `CLOSED` |

> **Note:** Either `access_token` OR all three GitHub App parameters (`github_app_id`, `github_app_installation_id`, `github_app_private_key`) must be provided.

### PR List Mode Configuration

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `required_review_approvals` | No | `0` | Minimum number of approved reviews required |

### Single PR Mode Configuration

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `number` | Yes | - | The PR number to track |
| `required_review_approvals` | No | `0` | Minimum number of approved reviews required |
| `git_crypt_key` | No | - | Base64 encoded git-crypt key for encrypted repositories |
| `disable_git_lfs` | No | `false` | Disable Git LFS |

## Behavior

### `check` 

**PR List Mode**: Returns list of PRs matching the filter criteria. A new version is emitted when:
- A new PR is opened
- A PR is closed/merged
- A PR's state changes (e.g., from draft to ready)

**Single PR Mode**: Returns list of commits to the specified PR. A new version is emitted for each new commit.

### `get` (in)

**PR List Mode**: Fetches PR metadata without cloning the repository.

Parameters:
| Parameter | Default | Description |
|-----------|---------|-------------|
| `skip_download` | `false` | Only fetch metadata, don't create files |

**Single PR Mode**: Clones the repository and integrates the PR.

Parameters:
| Parameter | Default | Description |
|-----------|---------|-------------|
| `skip_download` | `false` | Skip cloning (for status updates only) |
| `integration_tool` | `merge` | How to integrate: `merge`, `rebase`, or `checkout` |
| `git_depth` | `0` | Shallow clone depth (0 = full clone) |
| `submodules` | `false` | Recursively clone submodules |
| `list_changed_files` | `false` | Create list of changed files |
| `fetch_tags` | `false` | Fetch tags from remote |

### `put` (out)

**Only available in Single PR Mode**. Updates PR status and adds comments.

Parameters:
| Parameter | Required | Description |
|-----------|----------|-------------|
| `path` | Yes | Path to the PR resource from `get` |
| `status` | No | Commit status: `success`, `failure`, `error`, `pending` |
| `context` | No | Status context (default: `concourse-ci`) |
| `target_url` | No | URL to link from the status |
| `description` | No | Status description |
| `comment` | No | Comment text to add to PR |
| `comment_file` | No | File containing comment text |

## Metadata

The resource provides metadata in multiple formats:

1. **Individual files** in `.git/resource/`:
   - `pr` - PR number
   - `url` - PR URL
   - `title` - PR title
   - `author` - Author username
   - `head_ref` - Head branch name
   - `head_sha` - Head commit SHA
   - `base_ref` - Base branch name
   - `base_sha` - Base commit SHA (Single PR mode only)
   - `state` - PR state
   - `approved_review_count` - Number of approvals

2. **JSON files** in `.git/resource/`:
   - `version.json` - Version object
   - `metadata.json` - Full metadata array

3. **Changed files** (if `list_changed_files: true`):
   - `.git/resource/changed_files` - Newline-separated list

## Examples

### Example 1: Instance Pipelines for Each PR

```yaml
resources:
  - name: prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      base_branch: main
      ignore_drafts: true
      required_review_approvals: 2
      labels: ["ready-for-ci"]

jobs:
  - name: test-pr
    plan:
      - get: prs
        trigger: true
        version: every
      - task: run-tests
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang}
          inputs:
            - name: prs
          run:
            path: sh
            args:
              - -c
              - |
                PR_NUMBER=$(cat prs/.git/resource/pr)
                echo "Testing PR #$PR_NUMBER"
                # Your test commands here
```

### Example 2: Single PR with Status Updates

```yaml
resources:
  - name: my-pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 42

jobs:
  - name: test
    plan:
      - get: my-pr
        trigger: true
      - put: my-pr
        params:
          path: my-pr
          status: pending
          context: tests
      - task: run-tests
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang}
          inputs:
            - name: my-pr
          run:
            path: sh
            args: ["-c", "cd my-pr && go test ./..."]
        on_success:
          put: my-pr
          params:
            path: my-pr
            status: success
            context: tests
        on_failure:
          put: my-pr
          params:
            path: my-pr
            status: failure
            context: tests
            comment: "Tests failed! Please check the build."
```

### Example 3: Path Filtering

```yaml
resources:
  - name: backend-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      paths:
        - "backend/**"
        - "api/**"
      ignore_paths:
        - "**/*.md"
        - "docs/**"
```

### Example 4: Multiple Integration Tools

```yaml
resources:
  - name: pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 42

jobs:
  - name: test-with-rebase
    plan:
      - get: pr
        trigger: true
        params:
          integration_tool: rebase  # Rebase PR on top of base
          list_changed_files: true
```

## 🛠️ Development

### Building Locally

**Requirements:**
- Go 1.23 or higher
- Docker 24.0 or higher
- Git 2.40 or higher

```bash
# Clone repository
git clone https://github.com/ujala-singh/github-pr-concourse-resource.git
cd github-pr-concourse-resource

# Install dependencies
go mod download

# Build binaries
go build -o bin/check ./cmd/check
go build -o bin/in ./cmd/in
go build -o bin/out ./cmd/out

# Build Docker image
docker build -t github-pr-concourse-resource:dev .

# Run locally (for testing)
docker run -i github-pr-concourse-resource:dev /opt/resource/check < check-request.json
```

### Testing

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run with race detection
go test -race ./...

# Run with coverage
go test -cover ./...

# Generate coverage report
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific test
go test ./models -run TestGetAccessToken

# Run tests in watch mode (requires entr)
find . -name "*.go" | entr -c go test ./...
```

### Code Quality

```bash
# Format code
go fmt ./...

# Run linter (requires golangci-lint)
golangci-lint run

# Vet code
go vet ./...

# Static analysis
staticcheck ./...
```

### Project Structure

```
.
├── cmd/
│   ├── check/          # Check for new versions
│   ├── in/             # Fetch resource
│   └── out/            # Update resource (status/comments)
├── models/             # Core data models and GitHub client
│   ├── github_app.go   # GitHub App authentication
│   └── models.go       # Data structures
├── pr/                 # Single PR mode implementation
├── prlist/             # PR List mode implementation
├── docs/               # Comprehensive documentation
│   ├── QUICKSTART.md
│   ├── ARCHITECTURE.md
│   ├── IMPLEMENTATION.md
│   └── GITHUB_APP_AUTHENTICATION.md
└── Dockerfile          # Multi-stage build configuration
```

## 🔍 Troubleshooting

### Common Issues

#### 1. Authentication Failures

**Symptom:** `HTTP 401` or `Bad credentials` errors

**Solutions:**
- Verify your access token has `repo` scope
- For GitHub App: Ensure all three parameters are correct
- Check token hasn't expired
- Verify repository access permissions

```yaml
# Debug: Enable verbose logging
source:
  repository: owner/repo
  access_token: ((github-token))
  # Test token with: curl -H "Authorization: token YOUR_TOKEN" https://api.github.com/user
```

#### 2. Resource Not Triggering

**Symptom:** PRs created but pipeline doesn't trigger

**Solutions:**
- Check filter criteria (labels, base_branch, etc.)
- Verify webhook configuration (if using webhooks)
- Check PR meets required_review_approvals
- Look at draft status if `ignore_drafts: true`

```bash
# Debug check behavior
fly -t your-target check-resource --resource pipeline/pr-resource
```

#### 3. Git Clone Failures

**Symptom:** `fatal: could not read Username` or authentication errors during clone

**Solutions:**
- Ensure GitHub App installation has repository access
- Verify private key format (must be PEM with newlines preserved)
- Check network connectivity to GitHub
- For private repos, confirm authentication method has access

#### 4. Merge Conflicts

**Symptom:** `merge conflict` during `get` step

**Solutions:**
- Use `integration_tool: rebase` instead of `merge`
- Use `integration_tool: checkout` to skip integration
- Address conflicts in the PR before running pipeline

### Debug Mode

Enable detailed logging by setting environment variables:

```yaml
jobs:
  - name: debug-pr
    plan:
      - get: pull-requests
        params:
          skip_download: false
        # Add environment for debugging
        env:
          - name: DEBUG
            value: "true"
```

### Getting Help

- 📖 Check [documentation](docs/)
- 🐛 [Report issues](https://github.com/ujala-singh/github-pr-concourse-resource/issues)
- 💬 [Start a discussion](https://github.com/ujala-singh/github-pr-concourse-resource/discussions)
- 📧 Contact maintainers

## 📊 Performance Considerations

- **Shallow cloning**: Set `git_depth` to reduce clone time
- **Path filtering**: Use `paths` and `ignore_paths` to reduce check frequency
- **PR List Mode**: Scales to hundreds of PRs efficiently
- **Multi-architecture**: Native ARM64 support for Apple Silicon and ARM servers

## 🔒 Security Best Practices

1. **Use GitHub Apps** instead of personal access tokens
2. **Limit token scopes** to minimum required (`repo` for PAT)
3. **Store secrets** in Concourse credential managers (Vault, etc.)
4. **Enable required reviews** with `required_review_approvals`
5. **Filter forks** with `disable_forks: true` for security-sensitive repos
6. **Use signed commits** when possible
7. **Regular updates**: Keep the resource image updated

## 🤝 Contributing

We welcome contributions! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### How to Contribute

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Add/update tests
5. Ensure tests pass (`go test ./...`)
6. Commit your changes (`git commit -m 'feat: add amazing feature'`)
7. Push to the branch (`git push origin feature/amazing-feature`)
8. Open a Pull Request

### Development Setup

See the [Development](#-development) section above for setup instructions.

## 📜 License

MIT License - see [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

Special thanks to all contributors and the Concourse CI community!

## 📚 Additional Documentation

- **[Real-World Examples](docs/EXAMPLES.md)** - 13 detailed examples for common scenarios
- **[Complete Pipeline Template](examples/pipeline.yml)** - Ready-to-use pipeline with all features
- **[Troubleshooting Guide](docs/TROUBLESHOOTING.md)** - Solutions for common issues
- [Architecture Overview](docs/ARCHITECTURE.md)
- [Implementation Details](docs/IMPLEMENTATION.md)
- [GitHub App Setup](docs/GITHUB_APP_AUTHENTICATION.md)
- [Quick Start Guide](docs/QUICKSTART.md)

---

**Maintained with ❤️ by the community** | [Report Issues](https://github.com/ujala-singh/github-pr-concourse-resource/issues) | [Request Features](https://github.com/ujala-singh/github-pr-concourse-resource/issues/new?template=feature_request.md)

