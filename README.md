# GitHub PR Concourse Resource

A modern, feature-rich Concourse CI resource for GitHub Pull Requests with dual-mode support.

## Features

✨ **Dual Mode Operation**
- **PR List Mode**: Track all PRs matching criteria (for instance pipelines)
- **Single PR Mode**: Track commits to a specific PR

🔐 **Security & Filtering**
- Required review approvals
- Draft PR filtering
- Fork repository filtering  
- Path-based filtering (include/exclude patterns)
- Label-based filtering
- Base branch filtering
- PR state filtering (OPEN, MERGED, CLOSED)

🎯 **Smart Integration**
- Multiple integration strategies (merge, rebase, checkout)
- Commit status updates
- PR comments
- Changed files list
- Submodule support
- Git LFS support
- Configurable git depth

## Installation

### From jolly3 Registry

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: jolly3/github-pr-concourse-resource
      tag: latest
      # Or use a specific version:
      # tag: v1.0.0
```

### Building from Source

```bash
# Build Docker image
docker build -t jolly3/github-pr-concourse-resource:latest .

# Push to registry
docker push jolly3/github-pr-concourse-resource:latest
```

## Modes of Operation

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

*Note: Either `access_token` OR all three GitHub App parameters (`github_app_id`, `github_app_installation_id`, `github_app_private_key`) must be provided.
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

## Development

### Building

```bash
# Build binaries
go build -o cmd/check/check ./cmd/check
go build -o cmd/in/in ./cmd/in
go build -o cmd/out/out ./cmd/out

# Build Docker image
docker build -t github-pr-concourse-resource .
```

### Testing

```bash
# Run unit tests
go test ./...

# Run with coverage
go test -cover ./...

# Run with coverage report
task test-coverage

# Run e2e tests (requires GITHUB_ACCESS_TOKEN)
export GITHUB_ACCESS_TOKEN=your_token_here
task test-e2e

# Run all tests including e2e
task test-all
```

#### E2E Tests

End-to-end tests verify functionality against real GitHub repositories. See [e2e/README.md](e2e/README.md) for detailed setup instructions.

**Quick start:**
```bash
# Set up token
export GITHUB_ACCESS_TOKEN="your_github_token"

# Optional: specify test repository
export TEST_REPOSITORY="owner/repo"

# Run e2e tests
go test -v -tags=e2e ./e2e/...
```

**What's tested:**
- ✅ Check operation (both PR and PR list modes)
- ✅ In operation (merge, rebase, checkout strategies)
- ✅ Out operation (status updates, comments)
- ✅ Path filtering (include/exclude patterns)
- ✅ API cost optimization
- ✅ Metadata file generation

**CI Integration:**
E2E tests run automatically in CI using GitHub's automatic `GITHUB_TOKEN` (no setup required).

## Differences from Other Resources

### vs. `github-pr-resource` (telia-oss)
- ✅ Adds PR list mode for instance pipelines
- ✅ More filtering options
- ✅ Modern Go dependencies
- ✅ Better path filtering

### vs. `github-pr-instances-resource` (aoldershaw)
- ✅ Cleaner, more maintainable codebase
- ✅ Up-to-date dependencies (Go 1.22, latest GitHub API)
- ✅ Better separation of concerns
- ✅ More comprehensive documentation

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## Credits

This resource combines the best features from:
- [telia-oss/github-pr-resource](https://github.com/telia-oss/github-pr-resource)
- [aoldershaw/github-pr-instances-resource](https://github.com/aoldershaw/github-pr-instances-resource)

