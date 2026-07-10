# E2E Tests

End-to-end tests for the GitHub PR Concourse Resource that verify functionality against real GitHub repositories.

## Prerequisites

1. **GitHub Access Token**: You need a GitHub personal access token with repo access
2. **Test Repository**: A GitHub repository with pull requests to test against (default: your current repo)

### Test Repository Requirements

The test repository should have:
- At least one **open pull request** for testing
- Optionally: PRs with **labels** (for filter tests)
- Optionally: PRs with **different file changes** (for path filter tests)
- For write tests: You need **write access** to the repository

## Setup

### 1. Create a GitHub Personal Access Token

Go to GitHub Settings > Developer settings > Personal access tokens > Generate new token

Required scopes:
- `repo` (Full control of private repositories)
- `read:org` (if testing with organization repositories)

### 2. Set Environment Variables

```bash
export GITHUB_ACCESS_TOKEN="your_token_here"
export TEST_REPOSITORY="owner/repo"  # Optional, defaults to ujala-singh/github-repository-dispatch-receiver

# Example using the default test repo (already has 6+ PRs with labels):
export TEST_REPOSITORY="ujala-singh/github-repository-dispatch-receiver"
```

### 3. For Write Tests (Optional)

Some tests require write access to the repository (e.g., creating status updates, comments):

```bash
export TEST_WRITE_ACCESS="true"
```

**Warning**: Only enable write access when testing against a repository you own or have explicit permission to modify.

## Running E2E Tests

### Using Go directly

```bash
# Run all e2e tests
go test -v -tags=e2e ./e2e/...

# Run specific test
go test -v -tags=e2e ./e2e/... -run TestPRModeCheck

# Run with verbose output
go test -v -tags=e2e ./e2e/... -v
```

### Using Taskfile

```bash
# Run e2e tests
task test-e2e

# Run e2e with verbose output
task test-e2e-verbose
```

## Test Coverage

### PR Mode (Single PR) Tests

- ✅ **Check**: Fetch commits for a specific PR
- ✅ **In (Merge)**: Clone and merge PR into base branch
- ✅ **In (Rebase)**: Clone and rebase PR commits
- ✅ **In (Checkout)**: Clone PR at specific commit
- ✅ **In (Skip Download)**: Metadata only, no git operations
- ✅ **Out**: Update commit status on PR

### PR List Mode Tests

- ✅ **Check**: Fetch all PRs matching criteria
- ✅ **Check with Filters**: Label, state, path filters
- ✅ **In**: Fetch PR metadata
- ✅ **In (Skip Download)**: Metadata only

### Path Filtering Tests

- ✅ Include paths (e.g., `*.md`)
- ✅ Exclude paths (e.g., `*.txt`)
- ✅ Glob patterns
- ✅ Prefix patterns

### API Cost Tests

- ✅ Verify PR mode API call count
- ✅ Verify PR list mode API call count
- ✅ Rate limit awareness

## Test Structure

```
e2e/
├── e2e_test.go           # Main test suite
└── README.md             # This file
```

## Test Cases

### 1. TestPRModeCheck
Tests the `check` operation for single PR mode.

**Scenarios**:
- Returns commits for a specific PR
- Returns only new commits since last check
- Handles various PR states

### 2. TestPRListModeCheck
Tests the `check` operation for PR list mode.

**Scenarios**:
- Returns all open PRs
- Filters by labels
- Filters by state
- Filters by paths

### 3. TestPRModeIn
Tests the `in` operation for single PR mode.

**Scenarios**:
- Merge strategy: Merges PR into base
- Rebase strategy: Rebases PR commits
- Checkout strategy: Checks out PR commit
- Skip download: Metadata only

### 4. TestPRListModeIn
Tests the `in` operation for PR list mode.

**Scenarios**:
- Returns PR metadata
- Skip download option

### 5. TestPRModeOut
Tests the `out` operation (requires write access).

**Scenarios**:
- Update commit status
- Add PR comments
- Set custom context

### 6. TestCheckAPICost
Verifies API usage efficiency.

**Scenarios**:
- PR mode API call count
- PR list mode API call count
- Rate limit consumption

### 7. TestPathFiltering
Tests path-based PR filtering.

**Scenarios**:
- Include paths with glob patterns
- Exclude paths with glob patterns
- Combined include/exclude

## Expected Test Repository Structure

The tests assume the test repository has:
- At least one open PR with number #4
- PR #4 has specific commits
- Various PRs with different labels
- PRs with different file changes

### Using itsdalmo/test-repository

The default test repository (`itsdalmo/test-repository`) has:
- Multiple historical PRs for testing
- Consistent commit history
- Known PR numbers and SHAs
- Various labels and states

## Troubleshooting

### Rate Limit Errors

```
Error: API rate limit exceeded
```

**Solution**: Wait for rate limit to reset or use a token with higher limits.

### Authentication Errors

```
Error: 401 Unauthorized
```

**Solution**: Check that `GITHUB_ACCESS_TOKEN` is set correctly and has required scopes.

### Test Repository Not Found

```
Error: 404 Not Found
```

**Solution**: Verify `TEST_REPOSITORY` points to an accessible repository.

### Write Access Required

```
Test skipped: TEST_WRITE_ACCESS not set to true
```

**Solution**: Set `TEST_WRITE_ACCESS=true` only if you own the test repository.

## CI/CD Integration

### GitHub Actions

```yaml
- name: Run E2E Tests
  env:
    GITHUB_ACCESS_TOKEN: ${{ secrets.GITHUB_ACCESS_TOKEN }}
    TEST_REPOSITORY: ${{ github.repository }}
  run: go test -v -tags=e2e ./e2e/...
```

### Local CI

```bash
# Run e2e in CI mode (no interactive prompts)
CI=true task test-e2e
```

## Best Practices

1. **Use Test Repositories**: Never run e2e tests against production repositories
2. **Rotate Tokens**: Use dedicated tokens for testing and rotate regularly
3. **Monitor Rate Limits**: Be aware of GitHub API rate limits
4. **Clean Up**: Tests should clean up any created resources (PRs, comments, etc.)
5. **Idempotent**: Tests should be idempotent and not depend on previous runs

## Contributing

When adding new e2e tests:

1. Follow the existing test structure
2. Use table-driven tests
3. Add appropriate skip conditions (token checks)
4. Document expected repository state
5. Clean up test artifacts
6. Test against both PR and PR list modes when applicable

## See Also

- [Main README](../README.md)
- [Architecture Documentation](../docs/ARCHITECTURE.md)
- [Contributing Guide](../CONTRIBUTING.md)
