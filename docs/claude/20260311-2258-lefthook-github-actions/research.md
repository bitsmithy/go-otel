# Research: Lefthook + GitHub Actions for go-otel

## Overview

Setting up local git hooks (lefthook) and CI (GitHub Actions) for go-otel. The project currently has no CI or hook configuration.

## Current State

- **Go version**: 1.24.6
- **Module**: `github.com/bitsmithy/go-otel`
- **Package structure**: root package `otel` + sub-package `oteltest/`
- **Existing config**: None — no lefthook.yml, .github/, .golangci.yml, or Makefile

## Tools Required

| Tool | Purpose | Installed locally? |
|------|---------|-------------------|
| gofumpt | Strict Go formatting | Yes |
| goimports | Import management | Yes |
| gci | Import ordering/grouping | Yes |
| golangci-lint | Comprehensive linter | Yes |
| gotestsum | Test runner with better output | Needs check |
| govulncheck | Vulnerability scanner | Needs check |
| lefthook | Git hooks manager | Yes |

## Reference: alchemist (sibling Go project)

The `alchemist` project at `~/Documents/Projects/bitsmithy/alchemist/` uses the same Go version and has an established CI pattern:

### lefthook.yml

- `pre-commit` hook with `parallel: true`
- 4 commands: build, vet, lint, test

### GitHub Actions

- **Runner**: `blacksmith-2vcpu-ubuntu-2404-arm`
- **check.yml**: lint (golangci-lint-action@v8) + vet jobs
- **test.yml**: go test
- **actions/setup/action.yml**: composite action — checkout, setup-go@v5 (go 1.24), go get, go build

## Desired Configuration

### Lefthook (pre-commit)

1. **Fixers** (sequential, in order): gofumpt → goimports → gci → golangci-lint --fix
2. **Checks** (parallel, run after fixers): gotestsum, go vet

### GitHub Actions

- **check.yml**: gofumpt diff check, go vet, golangci-lint, govulncheck
- **test.yml**: gotestsum
- **actions/setup/action.yml**: composite setup action (matching alchemist pattern)

## Key Files

| File | Role |
|------|------|
| `lefthook.yml` | Git hook configuration |
| `.github/workflows/check.yml` | Linting/vetting CI workflow |
| `.github/workflows/test.yml` | Test CI workflow |
| `.github/actions/setup/action.yml` | Composite Go setup action |
