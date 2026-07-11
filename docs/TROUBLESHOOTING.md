# Troubleshooting Guide

Common issues and solutions for GitHub PR Concourse Resource.

## Table of Contents

- [Authentication Issues](#authentication-issues)
- [Resource Not Triggering](#resource-not-triggering)
- [Build Failures](#build-failures)
- [Performance Issues](#performance-issues)
- [Integration Problems](#integration-problems)
- [Debugging Tips](#debugging-tips)

## Authentication Issues

### Problem: "401 Unauthorized" or "Bad credentials"

**Symptoms:**
```
resource script '/opt/resource/check' failed: exit status 1
ERROR: authentication failed: GET https://api.github.com/repos/owner/repo: 401 Bad credentials
```

**Solutions:**

1. **Verify token scopes**:
   - For private repos: `repo` (full control)
   - For public repos: minimum `repo:status` and `public_repo`
   - Check token at: https://github.com/settings/tokens

2. **Check token is not expired**:
   - Personal access tokens can have expiration dates
   - Regenerate token if expired

3. **Verify token is correctly configured in Concourse**:
   ```bash
   # Check the secret
   fly -t your-target check-resource --resource pipeline/pull-requests
   ```

4. **Ensure no extra whitespace**:
   ```yaml
   # WRONG: token has trailing space
   access_token: "ghp_xxxxx "
   
   # CORRECT:
   access_token: "ghp_xxxxx"
   ```

### Problem: GitHub App Authentication Fails

**Symptoms:**
```
ERROR: failed to get GitHub App access token: could not create JWT
```

**Solutions:**

1. **Verify App ID and Installation ID**:
   ```bash
   # Check your app settings
   https://github.com/settings/apps/YOUR_APP_NAME
   
   # Check installations
   https://github.com/organizations/YOUR_ORG/settings/installations
   ```

2. **Check private key format**:
   - Must be PEM format
   - Include full headers: `-----BEGIN RSA PRIVATE KEY-----` and `-----END RSA PRIVATE KEY-----`
   - No extra whitespace or newlines

   ```yaml
   # Correct format in Concourse vars:
   github_app_private_key: |
     -----BEGIN RSA PRIVATE KEY-----
     MIIEpAIBAAKCAQEA...
     ...actual key content...
     -----END RSA PRIVATE KEY-----
   ```

3. **Verify App permissions**:
   - Repository permissions → Contents: Read-only
   - Repository permissions → Pull requests: Read & write
   - Repository permissions → Commit statuses: Read & write

4. **Check App is installed on the repository**:
   - Go to: `https://github.com/organizations/YOUR_ORG/settings/installations`
   - Ensure your app is installed
   - Verify it has access to the specific repository

### Problem: Rate Limit Exceeded

**Symptoms:**
```
ERROR: API rate limit exceeded. Reset at: 2024-01-15 12:30:00
```

**Solutions:**

1. **Use GitHub App authentication**: 5,000 requests/hour vs 60 for unauthenticated
2. **Increase check interval**: Don't check too frequently
3. **Use path filters**: Reduce unnecessary API calls
4. **Use single PR mode for specific PRs**: Avoid listing all PRs

## Resource Not Triggering

### Problem: PRs Don't Trigger Pipeline

**Possible Causes & Solutions:**

#### 1. Version Strategy Not Set

```yaml
# WRONG: No version strategy
- get: pull-requests
  trigger: true

# CORRECT: Use 'every' for PR list mode
- get: pull-requests
  trigger: true
  version: every
```

#### 2. PR Filtered Out

Check your filters:

```yaml
resources:
  - name: pull-requests
    type: github-pr
    source:
      repository: myorg/myrepo
      access_token: ((github-token))
      skip_drafts: true        # Are your PRs drafts?
      skip_forks: true          # Are PRs from forks?
      base_branch: main         # Is PR targeting different branch?
      required_review_approvals: 1  # Does PR have approval?
      labels: ["ready"]         # Does PR have the label?
      paths: ["src/**"]         # Did you change relevant files?
```

**Debug:** Remove filters one by one to identify which is blocking.

#### 3. PR State Not Matching

```yaml
# Default is OPEN only
states: [OPEN]

# If you want merged PRs too:
states: [OPEN, MERGED]
```

#### 4. Path Filters Too Restrictive

```yaml
# This might be too specific:
paths:
  - "backend/specific-service/**"

# Consider:
paths:
  - "backend/**"
  - "shared/**"
ignore_paths:
  - "**/*.md"
  - "**/test/**"
```

**Debug:**
```bash
# Check what files changed in the PR
gh pr view 123 --json files -q '.files[].path'

# Verify against your patterns
```

#### 5. Commit Has [ci skip]

```yaml
# If skip_ci_skip is true (default), commits with these are ignored:
# - [ci skip]
# - [skip ci]
# - [skip-ci]
# - [ci-skip]
```

Remove the marker from commit message or set `skip_ci_skip: false`.

### Problem: Only One PR Triggers, Not All

**Solution:** Ensure you're using `version: every`:

```yaml
- get: pull-requests
  trigger: true
  version: every  # This is critical!
```

Without `version: every`, Concourse treats the resource like any other and only triggers on "latest" version.

## Build Failures

### Problem: "No such file or directory" in Pipeline

**Symptoms:**
```
task failed: exit status 1
sh: can't open 'my-script.sh': No such file or directory
```

**Solutions:**

1. **Check PR is properly cloned**:
   ```yaml
   inputs:
     - name: pull-requests  # Must match resource name
   ```

2. **Verify working directory**:
   ```yaml
   run:
     path: sh
     dir: pull-requests  # Add this
     args:
       - -c
       - |
         ls -la
         ./build.sh
   ```

3. **Check script permissions**:
   ```bash
   # In your repository
   git ls-files --stage my-script.sh
   # Should be: 100755 (executable)
   
   # If not:
   git update-index --chmod=+x my-script.sh
   ```

### Problem: Merge Conflict During Rebase

**Symptoms:**
```
ERROR: failed to rebase: merge conflicts detected
```

**Solutions:**

1. **Don't auto-rebase**: Let developer resolve conflicts manually
2. **Add comment notification**:
   ```yaml
   - put: my-pr
     params:
       path: my-pr
       rebase: true
     on_failure:
       put: my-pr
       params:
         path: my-pr
         comment: "⚠️ Automatic rebase failed. Please rebase manually."
   ```

3. **Use merge instead of rebase** for automatic integration:
   ```yaml
   - put: my-pr
     params:
       path: my-pr
       merge:
         method: merge
   ```

### Problem: Tests Fail in CI but Pass Locally

**Common Causes:**

1. **Go version mismatch**:
   ```yaml
   # Use same version as local development
   image_resource:
     type: registry-image
     source:
       repository: golang
       tag: "1.23"  # Match your local version
   ```

2. **Missing dependencies**:
   ```yaml
   run:
     path: sh
     args:
       - -c
       - |
         cd my-pr
         go mod download  # Explicitly download dependencies
         go test ./...
   ```

3. **Environment differences**:
   - Check timezone, locale settings
   - Verify environment variables
   - Check for hardcoded paths

## Performance Issues

### Problem: Check Takes Too Long

**Symptoms:**
```
resource check duration: 2m30s
```

**Solutions:**

1. **Use path filters**: Only check when relevant files change
   ```yaml
   paths:
     - "src/**"
     - "go.mod"
   ignore_paths:
     - "**/*.md"
     - "docs/**"
   ```

2. **Use single PR mode for specific PRs**:
   ```yaml
   # Instead of checking all PRs
   - name: my-pr
     type: github-pr
     source:
       repository: myorg/myrepo
       access_token: ((github-token))
       number: 123  # Track only this PR
   ```

3. **Increase check interval**:
   ```yaml
   resources:
     - name: pull-requests
       type: github-pr
       source:
         # ... config ...
       check_every: 5m  # Default is 1m
   ```

4. **Use GitHub App**: Better rate limits (5,000 vs 5,000 requests/hour)

### Problem: Too Many Builds Triggered

**Solutions:**

1. **Skip draft PRs**:
   ```yaml
   skip_drafts: true
   ```

2. **Skip commits with [ci skip]**:
   ```yaml
   skip_ci_skip: true  # Default: true
   ```

3. **Use labels for opt-in**:
   ```yaml
   labels: ["ready-for-ci"]
   ```

4. **Require reviews before testing**:
   ```yaml
   required_review_approvals: 1
   ```

## Integration Problems

### Problem: Status Not Updating on GitHub

**Symptoms:**
- No status check appears on PR
- Status stuck at "pending"

**Solutions:**

1. **Verify status context is set**:
   ```yaml
   - put: my-pr
     params:
       path: my-pr
       status: success
       context: ci/concourse  # Required!
   ```

2. **Check token permissions**:
   - Token needs `repo:status` scope
   - GitHub App needs "Commit statuses: Read & write"

3. **Ensure put step executes**:
   ```yaml
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

### Problem: Comments Not Appearing

**Solutions:**

1. **Check token permissions**:
   - Personal token: `repo` scope (full control)
   - GitHub App: "Pull requests: Read & write"

2. **Verify comment in put**:
   ```yaml
   - put: my-pr
     params:
       path: my-pr
       comment: "Test results here"  # Must be set
   ```

3. **Check for API errors**:
   ```bash
   # Check Concourse logs
   fly -t target watch --job pipeline/job-name
   ```

### Problem: Merge Fails

**Symptoms:**
```
ERROR: failed to merge: PUT https://api.github.com/repos/owner/repo/pulls/123/merge: 405 Method not allowed
```

**Solutions:**

1. **Check branch protection**:
   - Repository Settings → Branches → Branch protection rules
   - Ensure required checks are passing
   - Verify required reviews are approved

2. **Verify merge is not blocked**:
   - Check for merge conflicts
   - Verify PR is not from a protected branch
   - Check that PR has required approvals

3. **Use correct merge method**:
   ```yaml
   params:
     path: my-pr
     merge:
       method: squash  # or merge, rebase
   ```

## Debugging Tips

### Enable Debug Logging

Set environment variable in your task:

```yaml
task: debug-pr
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
        set -x  # Enable debug output
        cd pull-requests
        ls -laR .git/resource/  # Show resource metadata
        cat .git/resource/pr
        cat .git/resource/url
        cat .git/resource/head_name
        cat .git/resource/head_sha
        cat .git/resource/base_name
        cat .git/resource/base_sha
```

### Check Resource Metadata

Resource provides metadata files in `.git/resource/`:

```bash
cd pull-requests
cat .git/resource/pr              # PR number
cat .git/resource/url             # PR URL
cat .git/resource/head_name       # Branch name
cat .git/resource/head_sha        # Commit SHA
cat .git/resource/base_name       # Base branch
cat .git/resource/base_sha        # Base commit SHA
cat .git/resource/changed_files   # List of changed files (newline-separated)
```

### Test Resource Locally

```bash
# Build resource image
docker build -t github-pr-resource .

# Test check
echo '{
  "source": {
    "repository": "myorg/myrepo",
    "access_token": "your-token"
  }
}' | docker run -i github-pr-resource /opt/resource/check

# Test get
echo '{
  "source": {
    "repository": "myorg/myrepo",
    "access_token": "your-token"
  },
  "version": {
    "pr": "123",
    "commit": "abc123"
  }
}' | docker run -i github-pr-resource /opt/resource/in /tmp/output
```

### Check Concourse Logs

```bash
# Watch build live
fly -t your-target watch --job pipeline-name/job-name

# Get build history
fly -t your-target builds -j pipeline-name/job-name

# Download build logs
fly -t your-target watch --job pipeline-name/job-name --build 42 > build.log
```

### Verify GitHub API Access

Test API access directly:

```bash
# List PRs
curl -H "Authorization: token YOUR_TOKEN" \
  https://api.github.com/repos/owner/repo/pulls

# Get specific PR
curl -H "Authorization: token YOUR_TOKEN" \
  https://api.github.com/repos/owner/repo/pulls/123

# Check rate limit
curl -H "Authorization: token YOUR_TOKEN" \
  https://api.github.com/rate_limit
```

## Getting Help

If you're still experiencing issues:

1. **Check existing issues**: https://github.com/ujala-singh/github-pr-concourse-resource/issues
2. **Enable debug logging**: Include logs in issue report
3. **Provide configuration**: Sanitized pipeline YAML (remove tokens!)
4. **Include versions**:
   - Resource version/tag
   - Concourse version
   - Go version (if building from source)

## Common Error Messages

| Error Message | Likely Cause | Solution |
|--------------|--------------|----------|
| `401 Bad credentials` | Invalid or expired token | Regenerate token with correct scopes |
| `404 Not Found` | Repository name wrong or no access | Verify repo name and token permissions |
| `403 Forbidden` | Insufficient permissions | Check token scopes or App permissions |
| `422 Validation Failed` | Invalid API request | Check resource configuration |
| `Resource script failed: exit status 1` | Script error | Check logs for specific error |
| `API rate limit exceeded` | Too many requests | Use GitHub App or reduce check frequency |
| `No versions found` | No PRs match filters | Review filter configuration |
| `failed to clone repository` | Git auth failure | Verify SSH keys or HTTPS token |

## Quick Checklist

When troubleshooting, verify:

- [ ] Token/credentials are valid and not expired
- [ ] Token has required scopes (`repo` for private, `repo:status` for public)
- [ ] Repository name is correct (owner/repo)
- [ ] PR matches all configured filters
- [ ] Using `version: every` for PR list mode
- [ ] Resource inputs match resource names
- [ ] Status context is set when updating status
- [ ] Token has permission for actions (merge, comment, status)
- [ ] No rate limiting issues
- [ ] Concourse can reach api.github.com

