# Real-World Examples

Comprehensive examples for common CI/CD scenarios.

## Table of Contents

- [PR List Mode Examples](#pr-list-mode-examples)
- [Single PR Mode Examples](#single-pr-mode-examples)
- [Advanced Filtering](#advanced-filtering)
- [Integration Examples](#integration-examples)
- [Security Patterns](#security-patterns)

## PR List Mode Examples

### Example 1: Basic PR Testing Pipeline

Create separate build instances for each open PR:

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: ghcr.io/ujala-singh/github-pr-concourse-resource
      tag: latest

resources:
  - name: pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      states: [OPEN]

jobs:
  - name: test-pr
    plan:
      - get: pull-requests
        trigger: true
        version: every
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23-alpine"}
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

### Example 2: Filter by Base Branch

Only test PRs targeting specific branches:

```yaml
resources:
  - name: main-pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      base_branch: main

  - name: develop-pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      base_branch: develop

jobs:
  - name: test-main-prs
    plan:
      - get: main-pull-requests
        trigger: true
        version: every
      - task: full-test-suite
        # ... comprehensive testing ...

  - name: test-develop-prs
    plan:
      - get: develop-pull-requests
        trigger: true
        version: every
      - task: quick-tests
        # ... faster smoke tests ...
```

### Example 3: Skip Draft PRs and Forks

Production-ready PR testing with security filters:

```yaml
resources:
  - name: production-ready-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      skip_drafts: true
      skip_forks: true
      skip_ci_skip: true
      required_review_approvals: 1
      labels: ["ready-for-ci"]

jobs:
  - name: deploy-to-staging
    plan:
      - get: production-ready-prs
        trigger: true
        version: every
      - task: deploy
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine}
          inputs:
            - name: production-ready-prs
          run:
            path: deploy.sh
```

### Example 4: Path-Based Testing

Only trigger builds when relevant files change:

```yaml
resources:
  - name: backend-prs
    type: github-pr
    source:
      repository: myorg/monorepo
      access_token: ((github-token))
      paths:
        - "backend/**"
        - "shared/**"
      ignore_paths:
        - "**/*.md"
        - "**/test/**"

  - name: frontend-prs
    type: github-pr
    source:
      repository: myorg/monorepo
      access_token: ((github-token))
      paths:
        - "frontend/**"
        - "shared/**"
      ignore_paths:
        - "**/*.md"

jobs:
  - name: test-backend
    plan:
      - get: backend-prs
        trigger: true
        version: every
      - task: backend-tests
        # ... Go tests ...

  - name: test-frontend
    plan:
      - get: frontend-prs
        trigger: true
        version: every
      - task: frontend-tests
        # ... Node tests ...
```

## Single PR Mode Examples

### Example 5: Track Specific PR Commits

Test every commit to a specific PR:

```yaml
resources:
  - name: feature-pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 123

jobs:
  - name: test-commits
    plan:
      # Get the PR
      - get: feature-pr
        trigger: true
      
      # Set pending status
      - put: feature-pr
        params:
          path: feature-pr
          status: pending
          context: ci/test
      
      # Run tests
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
          inputs:
            - name: feature-pr
          run:
            path: sh
            args:
              - -c
              - |
                cd feature-pr
                go test -v ./...
        
        # Update status on success
        on_success:
          put: feature-pr
          params:
            path: feature-pr
            status: success
            context: ci/test
        
        # Update status on failure
        on_failure:
          put: feature-pr
          params:
            path: feature-pr
            status: failure
            context: ci/test
```

### Example 6: Multi-Stage Testing with Comments

```yaml
resources:
  - name: my-pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 456

jobs:
  - name: build-and-test
    plan:
      - get: my-pr
        trigger: true
      
      # Announce build start
      - put: my-pr
        params:
          path: my-pr
          comment: "🚀 Starting automated tests..."
      
      # Unit tests
      - task: unit-tests
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
          inputs:
            - name: my-pr
          run:
            path: sh
            args: ["-c", "cd my-pr && go test -short ./..."]
      
      # Integration tests
      - task: integration-tests
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
          inputs:
            - name: my-pr
          run:
            path: sh
            args: ["-c", "cd my-pr && go test -run Integration ./..."]
      
      # Success comment
      on_success:
        put: my-pr
        params:
          path: my-pr
          comment: "✅ All tests passed!"
          status: success
      
      # Failure comment with details
      on_failure:
        put: my-pr
        params:
          path: my-pr
          comment: "❌ Tests failed. Check the build logs for details."
          status: failure
```

## Advanced Filtering

### Example 7: Label-Based Workflows

Different pipelines for different PR types:

```yaml
resources:
  - name: hotfix-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      labels: ["hotfix", "urgent"]
      skip_drafts: true

  - name: feature-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      labels: ["feature"]

  - name: security-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      labels: ["security"]
      required_review_approvals: 2

jobs:
  - name: hotfix-fast-track
    plan:
      - get: hotfix-prs
        trigger: true
        version: every
      - task: quick-test
        # ... minimal smoke tests for quick deployment ...

  - name: feature-full-test
    plan:
      - get: feature-prs
        trigger: true
        version: every
      - task: comprehensive-test
        # ... full test suite ...

  - name: security-audit
    plan:
      - get: security-prs
        trigger: true
        version: every
      - task: security-scan
        # ... SAST, DAST, dependency scanning ...
```

### Example 8: Review Requirements

Enforce code review before testing:

```yaml
resources:
  - name: reviewed-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      required_review_approvals: 2
      skip_drafts: true
      skip_forks: true
      states: [OPEN]

jobs:
  - name: deploy-to-staging
    plan:
      - get: reviewed-prs
        trigger: true
        version: every
      
      # Add deployment comment
      - put: reviewed-prs
        params:
          path: reviewed-prs
          comment: "🚀 Deploying to staging environment..."
      
      - task: deploy-staging
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine}
          inputs:
            - name: reviewed-prs
          run:
            path: deploy-to-staging.sh
      
      on_success:
        put: reviewed-prs
        params:
          path: reviewed-prs
          comment: "✅ Successfully deployed to staging: https://staging.example.com"
```

## Integration Examples

### Example 9: Merge After Successful Tests

Automatically merge PRs that pass all checks:

```yaml
resources:
  - name: auto-merge-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      labels: ["automerge"]
      required_review_approvals: 1
      skip_drafts: true

jobs:
  - name: test-and-merge
    plan:
      - get: auto-merge-prs
        trigger: true
        version: every
      
      - put: auto-merge-prs
        params:
          path: auto-merge-prs
          status: pending
          comment: "Running automated tests before merge..."
      
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
          inputs:
            - name: auto-merge-prs
          run:
            path: sh
            args:
              - -c
              - |
                cd auto-merge-prs
                go test ./...
                go vet ./...
      
      - put: auto-merge-prs
        params:
          path: auto-merge-prs
          merge:
            method: squash
            commit_msg: "file"
          comment: "✅ Tests passed. Merging PR..."
          status: success
```

### Example 10: Rebase and Test

Keep PR up-to-date with base branch:

```yaml
resources:
  - name: my-pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 789

jobs:
  - name: rebase-and-test
    plan:
      - get: my-pr
        trigger: true
      
      # Rebase on base branch
      - put: my-pr
        params:
          path: my-pr
          rebase: true
          comment: "🔄 Rebased on latest base branch"
      
      # Test after rebase
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang, tag: "1.23"}
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
          comment: "✅ Tests passed after rebase"
```

## Security Patterns

### Example 11: GitHub App Authentication

Use GitHub App for enhanced security and rate limits:

```yaml
resources:
  - name: secure-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      github_app_id: ((github-app-id))
      github_app_installation_id: ((github-app-installation-id))
      github_app_private_key: ((github-app-private-key))
      skip_forks: true  # Security: don't run on forks

jobs:
  - name: secure-test
    plan:
      - get: secure-prs
        trigger: true
        version: every
      - task: test
        # ... your tests ...
```

### Example 12: Separate Credentials for Different Repos

```yaml
resources:
  - name: public-repo-prs
    type: github-pr
    source:
      repository: myorg/public-repo
      access_token: ((public-repo-token))  # Token with minimal scopes
      skip_forks: false  # Allow community PRs

  - name: private-repo-prs
    type: github-pr
    source:
      repository: myorg/private-repo
      github_app_id: ((private-app-id))
      github_app_installation_id: ((private-installation-id))
      github_app_private_key: ((private-app-key))
      skip_forks: true  # Require trusted contributors
      required_review_approvals: 2
```

### Example 13: Fork Security with Changed Files Check

```yaml
resources:
  - name: community-prs
    type: github-pr
    source:
      repository: myorg/oss-project
      access_token: ((github-token))
      paths:
        - "docs/**"
        - "*.md"
      # Only allow documentation changes from forks
      skip_forks: false

jobs:
  - name: test-docs-pr
    plan:
      - get: community-prs
        trigger: true
        version: every
      
      # Verify only docs changed
      - task: verify-docs-only
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine}
          inputs:
            - name: community-prs
          run:
            path: sh
            args:
              - -c
              - |
                cd community-prs
                CHANGED=$(cat .git/resource/changed_files)
                if echo "$CHANGED" | grep -qv -E '(docs/|\.md$)'; then
                  echo "ERROR: Non-documentation files changed in fork PR"
                  exit 1
                fi
```

## Best Practices

### Resource Configuration

1. **Use specific tags**: Pin to specific versions in production
   ```yaml
   repository: ghcr.io/ujala-singh/github-pr-concourse-resource
   tag: v1.0.0  # Not 'latest'
   ```

2. **Use GitHub Apps for production**: Better security and higher rate limits
3. **Always skip forks for untrusted code**: Prevent code execution from untrusted sources
4. **Use path filters**: Reduce unnecessary builds
5. **Set review requirements**: Enforce code quality

### Pipeline Design

1. **Use `version: every`**: Ensure all PRs get tested
2. **Set status updates**: Keep developers informed
3. **Add meaningful comments**: Help debugging and collaboration
4. **Separate concerns**: Different jobs for build, test, deploy
5. **Handle failures gracefully**: Always set failure status

### Performance

1. **Use path filters**: Avoid testing irrelevant changes
2. **Skip draft PRs**: Don't waste resources on work-in-progress
3. **Cache dependencies**: Speed up builds
4. **Parallelize tests**: Run independent tests concurrently

### Security

1. **Skip forks by default**: Review fork PRs manually
2. **Require approvals**: Enforce code review
3. **Use minimal token scopes**: Limit access
4. **Validate input**: Check changed files in fork PRs
5. **Rotate credentials**: Regularly update tokens
