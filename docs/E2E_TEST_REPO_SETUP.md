# Setting Up Your Own E2E Test Repository

## Why Use Your Own Test Repo?

E2E tests need a real GitHub repository with pull requests to validate the resource works correctly. Instead of relying on external test repositories (like `itsdalmo/test-repository`), you should use your own repository for better control and reliability.

## Quick Setup

### Option 1: Use This Repository (Simplest)

The easiest approach is to test against this repository itself:

```bash
# In CI - automatically uses current repo
# No configuration needed!

# Locally
export TEST_REPOSITORY="ujala-singh/github-pr-concourse-resource"
task test-e2e
```

**Requirements**: Just ensure you have at least one open pull request in this repo.

### Option 2: Create a Dedicated Test Repository

For more comprehensive testing, create a dedicated test repository:

1. **Create a new repository** on GitHub (e.g., `ujala-singh/concourse-test-repo`)

2. **Create sample pull requests**:
   ```bash
   git clone git@github.com:ujala-singh/concourse-test-repo.git
   cd concourse-test-repo
   
   # Create test branches with PRs
   git checkout -b test-pr-1
   echo "Test change 1" > test1.txt
   git add test1.txt
   git commit -m "feat: test PR 1"
   git push -u origin test-pr-1
   # Open PR on GitHub
   
   git checkout main
   git checkout -b test-pr-2
   echo "Test change 2" > test2.txt
   git add test2.txt
   git commit -m "fix: test PR 2"
   git push -u origin test-pr-2
   # Open PR on GitHub
   ```

3. **Add labels** (optional, for filter tests):
   - Go to GitHub → Issues → Labels
   - Create labels like: `bug`, `feature`, `enhancement`
   - Apply labels to your test PRs

4. **Configure E2E tests**:
   ```bash
   export TEST_REPOSITORY="ujala-singh/concourse-test-repo"
   task test-e2e
   ```

## Repository Requirements

For E2E tests to work properly, your test repository should have:

### Minimum Requirements
- ✅ **At least 1 open pull request** (for basic check/in tests)
- ✅ **Read access** via GitHub token (automatic in CI)

### Recommended for Full Test Coverage
- ✅ **Multiple open PRs** (tests various scenarios)
- ✅ **PRs with labels** (tests label filtering)
- ✅ **PRs with different files** (tests path filtering)
- ✅ **PRs with multiple commits** (tests commit tracking)

### Optional for Write Tests
- ✅ **Write access** (for status update and comment tests)
- Set `TEST_WRITE_ACCESS=true` only for repos you own

## Configuring in CI

### GitHub Actions (Automatic)

By default, E2E tests use the **current repository**:

```yaml
# .github/workflows/ci.yml
- name: Run E2E tests
  env:
    GITHUB_ACCESS_TOKEN: ${{ secrets.GITHUB_TOKEN }}
    TEST_REPOSITORY: ${{ github.repository }}  # Uses current repo
  run: go test -v -tags=e2e ./e2e/...
```

### Use a Different Repository

Add a repository secret:

1. Go to **Settings → Secrets → Actions**
2. Add secret: `E2E_TEST_REPOSITORY`
3. Value: `owner/repo` (e.g., `ujala-singh/concourse-test-repo`)

The CI workflow will automatically use it:
```yaml
TEST_REPOSITORY: ${{ secrets.E2E_TEST_REPOSITORY || github.repository }}
```

## Local Testing

### Quick Test
```bash
# Use current repository
export GITHUB_ACCESS_TOKEN="ghp_your_token_here"
task test-e2e
```

### Custom Test Repository
```bash
export GITHUB_ACCESS_TOKEN="ghp_your_token_here"
export TEST_REPOSITORY="ujala-singh/my-test-repo"
task test-e2e
```

### With Write Tests
```bash
export GITHUB_ACCESS_TOKEN="ghp_your_token_here"
export TEST_REPOSITORY="ujala-singh/my-test-repo"
export TEST_WRITE_ACCESS="true"  # Only if you own the repo!
task test-e2e
```

## Example Test Repositories

Here are some patterns you can follow:

### Pattern 1: Minimal (Single PR)
```
my-test-repo/
├── README.md
└── PR #1 (open): "Test PR"
```
✅ Sufficient for basic check/in tests

### Pattern 2: Standard (Multiple PRs)
```
my-test-repo/
├── README.md
├── PR #1 (open): "feat: feature A" [label: feature]
├── PR #2 (open): "fix: bug B" [label: bug]
└── PR #3 (closed): "docs: update readme"
```
✅ Tests state filtering, label filtering

### Pattern 3: Comprehensive (Full Coverage)
```
my-test-repo/
├── src/app.js
├── src/utils.js
├── docs/README.md
├── PR #1 (open): Changes src/ files [label: feature]
├── PR #2 (open): Changes docs/ files [label: docs]
├── PR #3 (open): Multiple commits
└── PR #4 (closed): Merged PR
```
✅ Tests path filtering, multiple commits, state filtering

## Troubleshooting

### "No pull requests found"
- Ensure you have at least one **open** PR in the test repository
- Check that `TEST_REPOSITORY` is set correctly

### "404 Not Found"
- Verify the repository exists and you have access
- Check repository name format: `owner/repo` (not `github.com/owner/repo`)

### "Rate limit exceeded"
- Wait for the rate limit to reset (~1 hour)
- Use a token with higher limits
- Reduce test frequency

### "Permission denied" (write tests)
- Ensure you own the test repository
- Set `TEST_WRITE_ACCESS=true` only for repos you control
- Verify token has `repo` scope (not just `public_repo`)

## Best Practices

1. ✅ **Keep test PRs open** - Don't merge or close them between test runs
2. ✅ **Use descriptive labels** - Makes it easier to understand test scenarios
3. ✅ **Document test cases** - Add PR descriptions explaining what each tests
4. ✅ **Separate test repo** - Don't mix with production repositories
5. ✅ **Rotate tokens** - Use dedicated test tokens and rotate regularly
6. ✅ **Monitor rate limits** - Be aware of GitHub API usage

## Summary

- **Simplest**: Use this repo (`ujala-singh/github-pr-concourse-resource`) - just keep one PR open
- **Recommended**: Create dedicated test repo with 2-3 sample PRs with labels
- **CI Default**: Uses current repository automatically
- **Override**: Set `E2E_TEST_REPOSITORY` secret for different repo

**You don't need to use `itsdalmo/test-repository` - use your own repository for better control! 🎉**
