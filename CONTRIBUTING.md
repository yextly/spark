# Contributing to the Spark Worker Operator

Thank you for your interest in contributing!
This project welcomes bug reports, feature requests, documentation improvements, and code contributions.

## How to Contribute

### 1. Fork the repository

Create a personal fork and clone it locally.

### 2. Create a feature branch

```bash
git checkout -b feature/my-new-feature
```

## Development Environment

- Go 1.21+
- Kubernetes 1.25+
- Kubebuilder / controller-runtime
- make, Docker

## Setup

```bash
make install
make run
make generate
make manifests
```

## Code Style

- Follow Go conventions
- Keep controllers idempotent
- Emit useful Kubernetes events
- Use finalizers for cleanup

## Testing

```bash
make test
```

## Pull Request Guidelines

- Clearly describe changes
- Reference issues
- Include tests when relevant
- Keep PRs focused
