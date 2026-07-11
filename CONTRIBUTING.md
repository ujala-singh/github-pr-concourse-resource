# Contributing to GitHub PR Concourse Resource

Thank you for your interest in contributing! This document provides guidelines and instructions for contributing.

## Development Setup

1. **Prerequisites**
   - Go 1.23 or higher
   - Docker 24.0 or higher (for building images)
   - [Task](https://taskfile.dev/) (optional, for using Taskfile)
   - [golangci-lint](https://golangci-lint.run/) (for code linting)

2. **Clone and Setup**
   ```bash
   git clone https://github.com/yourusername/github-pr-concourse-resource.git
   cd github-pr-concourse-resource
   go mod download
   ```

3. **Build**
   ```bash
   task build
   # or
   go build -o bin/check ./cmd/check
   go build -o bin/in ./cmd/in
   go build -o bin/out ./cmd/out
   ```

## Project Structure

```
.
├── cmd/              # Command-line entry points
│   ├── check/        # Check command (find new versions)
│   ├── in/           # In command (fetch resource)
│   └── out/          # Out command (update resource)
├── models/           # Core data models and GitHub client
│   ├── models.go     # Version, metadata, configuration types
│   └── github.go     # GitHub API interactions
├── pr/               # Single PR mode implementation
│   ├── models.go     # PR mode request/response types
│   ├── check.go      # Check implementation
│   ├── in.go         # In implementation (clone & merge)
│   └── out.go        # Out implementation (status & comments)
├── prlist/           # PR list mode implementation
│   ├── models.go     # PR list mode request/response types
│   ├── check.go      # Check implementation
│   └── in.go         # In implementation (metadata only)
├── Dockerfile        # Container image definition
├── go.mod            # Go module definition
└── README.md         # Documentation
```

## Code Style

- Follow standard Go conventions and idioms
- Use `gofmt` for formatting
- Run `go vet` before committing
- Add comments for exported functions and types
- Keep functions focused and small
- Write descriptive variable names

## Testing

1. **Unit Tests**
   ```bash
   task test
   # or
   go test ./...
   ```

2. **Coverage**
   ```bash
   task test-coverage
   # Opens coverage.html in your browser
   ```

3. **Linting**
   ```bash
   task lint
   ```

## Making Changes

1. **Create a branch**
   ```bash
   git checkout -b feature/my-new-feature
   ```

2. **Make your changes**
   - Write code
   - Add tests
   - Update documentation if needed

3. **Verify**
   ```bash
   task verify  # Runs tidy, lint, and test
   ```

4. **Commit**
   ```bash
   git add .
   git commit -m "feat: add new feature description"
   ```
   
   Use conventional commit messages:
   - `feat:` - New features
   - `fix:` - Bug fixes
   - `docs:` - Documentation changes
   - `test:` - Test additions or updates
   - `refactor:` - Code refactoring
   - `chore:` - Maintenance tasks

5. **Push and create PR**
   ```bash
   git push origin feature/my-new-feature
   ```
   Then create a Pull Request on GitHub.

## Testing with Concourse

To test your changes with a real Concourse instance:

1. Build and push your Docker image:
   ```bash
   docker build -t yourname/github-pr-concourse-resource:test .
   docker push yourname/github-pr-concourse-resource:test
   ```

2. Update your pipeline to use your test image:
   ```yaml
   resource_types:
     - name: github-pr
       type: registry-image
       source:
         repository: yourname/github-pr-concourse-resource
         tag: test
   ```

3. Run `fly set-pipeline` and test your changes

## Common Development Tasks

### Adding a new configuration option

1. Add the field to the appropriate struct in `models/models.go`
2. Add validation logic in the `Validate()` method
3. Update the filtering logic in `models/github.go`
4. Add tests
5. Update README.md documentation

### Adding a new metadata field

1. Add the field to the `PullRequest` struct in `models/models.go`
2. Update the GraphQL query in `models/github.go`
3. Add metadata extraction in `pr/in.go` or `prlist/in.go`
4. Update README.md documentation

### Debugging

1. **Local testing**:
   ```bash
   echo '{"source": {"repository": "owner/repo", "access_token": "token"}}' | ./bin/check
   ```

2. **Docker testing**:
   ```bash
   docker build -t test-resource .
   echo '{"source": {...}}' | docker run -i test-resource /opt/resource/check
   ```

3. **Concourse logs**:
   ```bash
   fly -t target builds -j pipeline/job
   fly -t target watch -j pipeline/job -b build-number
   ```

## Questions?

- Open an issue for bugs or feature requests
- Start a discussion for questions
- Check existing issues and PRs before creating new ones

Thank you for contributing! 🎉
