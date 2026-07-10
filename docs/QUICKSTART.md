# Quick Start Guide

Get started with the GitHub PR Concourse Resource in 5 minutes.

## Prerequisites

- A Concourse CI instance
- A GitHub repository
- A GitHub personal access token with `repo` scope

## 1. Create a GitHub Token

1. Go to GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)
2. Click "Generate new token (classic)"
3. Select scopes:
   - For public repos: `repo:status`
   - For private repos: `repo` (full control)
4. Generate and copy the token

## 2. Add Resource Type to Your Pipeline

```yaml
resource_types:
  - name: github-pr
    type: registry-image
    source:
      repository: yourusername/github-pr-concourse-resource
      tag: latest
```

## 3. Choose Your Mode

### Option A: Track All PRs (Instance Pipelines)

Perfect for creating separate build pipelines for each PR:

```yaml
resources:
  - name: pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))

jobs:
  - name: test-pr
    plan:
      - get: pull-requests
        trigger: true
        version: every  # Trigger for each PR
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine}
          inputs:
            - name: pull-requests
          run:
            path: sh
            args:
              - -c
              - |
                PR=$(cat pull-requests/.git/resource/pr)
                echo "Testing PR #$PR"
```

### Option B: Track Single PR Commits

Perfect for testing every commit to a specific PR:

```yaml
resources:
  - name: my-pr
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      number: 123  # Your PR number

jobs:
  - name: test-commits
    plan:
      - get: my-pr
        trigger: true
      # Set pending status
      - put: my-pr
        params:
          path: my-pr
          status: pending
      # Run your tests
      - task: test
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: golang}
          inputs:
            - name: my-pr
          run:
            path: sh
            args:
              - -c
              - |
                cd my-pr
                go test ./...
        on_success:
          put: my-pr
          params:
            path: my-pr
            status: success
        on_failure:
          put: my-pr
          params:
            path: my-pr
            status: failure
```

## 4. Set Your Pipeline

```bash
# Save your pipeline as pipeline.yml
fly -t your-target set-pipeline -p test-prs -c pipeline.yml

# Unpause it
fly -t your-target unpause-pipeline -p test-prs
```

## 5. (Optional) Configure Filtering

Add these options to `source` for more control:

```yaml
resources:
  - name: pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      
      # Only PRs to main branch
      base_branch: main
      
      # Ignore draft PRs
      ignore_drafts: true
      
      # Require at least 1 approval
      required_review_approvals: 1
      
      # Only trigger on specific labels
      labels: ["ready-for-ci"]
      
      # Only trigger on changes to these paths
      paths:
        - "src/**"
        - "api/**"
      
      # Ignore changes to these paths
      ignore_paths:
        - "**/*.md"
        - "docs/**"
```

## Common Use Cases

### Use Case 1: Run Tests on Every PR

```yaml
jobs:
  - name: test-all-prs
    plan:
      - get: pull-requests
        trigger: true
        version: every
      - task: notify
        config:
          # ... your test task
```

### Use Case 2: Update PR Status

```yaml
jobs:
  - name: build-and-test
    plan:
      - get: my-pr
        trigger: true
      - put: my-pr
        params:
          path: my-pr
          status: pending
          context: ci/tests
      - task: test
        # ... your test task
        on_success:
          put: my-pr
          params:
            path: my-pr
            status: success
            context: ci/tests
```

### Use Case 3: Add PR Comments

```yaml
- put: my-pr
  params:
    path: my-pr
    comment: |
      ## Build Results
      ✅ All tests passed!
      📊 Coverage: 85%
```

### Use Case 4: Filter by Changed Files

```yaml
resources:
  - name: backend-prs
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      paths: ["backend/**"]

jobs:
  - name: test-backend
    plan:
      - get: backend-prs
        trigger: true
        version: every
      # Only runs when backend files change
```

## Troubleshooting

### PRs Not Triggering?

1. Check your access token has correct permissions
2. Verify `check_every` or webhook configuration
3. Check filters (labels, base_branch, etc.)
4. Look at Concourse logs: `fly -t target watch -j pipeline/job`

### Git Clone Failing?

- Ensure token has `repo` scope (not just `repo:status`)
- For private repos, need full `repo` access

### Status Not Updating?

- Token needs at least `repo:status` permission
- Check the `context` parameter is unique per job

## Next Steps

- Read the full [README.md](README.md) for all configuration options
- Check out [examples/pipeline.yml](examples/pipeline.yml) for more examples
- See [CONTRIBUTING.md](CONTRIBUTING.md) to contribute

## Getting Help

- 📖 Read the full documentation in [README.md](README.md)
- 🐛 Found a bug? [Open an issue](https://github.com/yourusername/github-pr-concourse-resource/issues)
- 💬 Have questions? [Start a discussion](https://github.com/yourusername/github-pr-concourse-resource/discussions)

---

That's it! You now have a GitHub PR resource running in Concourse. 🎉
