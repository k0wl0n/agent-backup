# Contributing to Agent Backup

Thank you for your interest in contributing! This document provides guidelines for contributing to the project.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR_USERNAME/agent-backup.git`
3. Create a feature branch: `git checkout -b feature/your-feature-name`
4. Make your changes
5. Run tests: `go test -race -count=1 ./...`
6. Commit your changes: `git commit -am 'Add some feature'`
7. Push to the branch: `git push origin feature/your-feature-name`
8. Create a Pull Request

## Development Setup

### Prerequisites

- Go 1.25 or later
- Docker and Docker Compose (for integration tests)
- Git

### Local Development

```bash
# Install dependencies
go mod download

# Run tests
go test ./...

# Run with race detector
go test -race ./...

# Build
go build -o jokowipe-agent ./cmd/agent

# Run locally
./jokowipe-agent --config agent.local.yaml
```

### Testing with Docker Compose

The repository includes a `docker-compose.yml` with test databases:

```bash
docker compose up -d
# Databases available:
# - PostgreSQL on localhost:5433
# - MySQL on localhost:3307
```

## Code Guidelines

### Go Style

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` for formatting
- Run `go vet` before committing
- Keep functions small and focused
- Add comments for exported functions and types

### Testing

- Write tests for new features
- Maintain or improve code coverage
- Use table-driven tests where appropriate
- Mock external dependencies

### Commit Messages

- Use clear, descriptive commit messages
- Start with a verb in present tense: "Add", "Fix", "Update", "Remove"
- Reference issues: "Fix #123: Description of fix"

## Pull Request Process

1. **Update documentation** if you're changing functionality
2. **Add tests** for new features
3. **Ensure CI passes** - all tests must pass
4. **Keep PRs focused** - one feature/fix per PR
5. **Respond to feedback** - address review comments promptly

## Reporting Bugs

Use GitHub Issues to report bugs. Include:

- **Description**: Clear description of the bug
- **Steps to reproduce**: Minimal steps to reproduce the issue
- **Expected behavior**: What you expected to happen
- **Actual behavior**: What actually happened
- **Environment**: OS, Go version, agent version
- **Logs**: Relevant log output (sanitize sensitive data)

## Feature Requests

Feature requests are welcome! Please:

- Check if the feature already exists or is planned
- Describe the use case clearly
- Explain why this would be valuable
- Be open to discussion and alternatives

## Code of Conduct

- Be respectful and inclusive
- Welcome newcomers
- Focus on constructive feedback
- Assume good intentions

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
