# Plan: Lefthook + GitHub Actions for go-otel

## Goal

Set up local git hooks (lefthook) and CI (GitHub Actions) for go-otel, ensuring code is auto-formatted and linted before commits, and fully validated in CI on every pull request.

## Research Reference

`docs/claude/20260311-2258-lefthook-github-actions/research.md`

## Approach

Follow the established pattern from the sibling `alchemist` project, adapted for go-otel's two-stage lefthook design (fixers-then-checks) and expanded CI checks (gofumpt diff, govulncheck). Four files to create:

1. `lefthook.yml` — two groups: sequential fixers, then parallel checks
2. `.github/actions/setup/action.yml` — composite action (shared by all workflows)
3. `.github/workflows/check.yml` — linting/vetting/security jobs
4. `.github/workflows/test.yml` — test job using gotestsum

No new Go dependencies or code changes — this is purely config/CI.

## Detailed Changes

### 1. `lefthook.yml` (new file)

Uses lefthook's `jobs` feature (v1.10.0+, we have v2.1.3) with nested `group` blocks. The piped fixers group runs first; if all fixers succeed, the parallel checks group runs.

```yaml
pre-commit:
  jobs:
    - group:
        piped: true
        jobs:
          - name: gofumpt
            glob: "*.go"
            run: gofumpt -w {staged_files}
            stage_fixed: true
          - name: goimports
            glob: "*.go"
            run: goimports -w {staged_files}
            stage_fixed: true
          - name: gci
            glob: "*.go"
            run: gci write --section Standard --section Default --section "Prefix(github.com/bitsmithy)" {staged_files}
            stage_fixed: true
          - name: lint-fix
            glob: "*.go"
            run: golangci-lint run --fix ./...
            stage_fixed: true
    - group:
        parallel: true
        jobs:
          - name: vet
            run: go vet ./...
          - name: test
            run: gotestsum ./...
```

**Key design decisions:**

- Two `group` blocks under `jobs`: first group is `piped` (sequential fixers), second is `parallel` (concurrent checks)
- Groups run sequentially — checks only start after all fixers succeed. If a fixer fails, checks are skipped.
- `glob: "*.go"` + `{staged_files}` ensures fixers only process staged Go files
- `stage_fixed: true` auto-stages files modified by fixers
- `gci` sections: Standard (stdlib), Default (third-party), Prefix (our module) — matches the import ordering already used in the codebase

### 2. `.github/actions/setup/action.yml` (new file)

Composite action matching alchemist's pattern, pinned to Go 1.24:

```yaml
---
name: "Setup"
description: "Sets up the Go environment, fetching deps, initial build"
runs:
  using: "composite"
  steps:
    - uses: actions/checkout@v4
    - name: Setup Go
      uses: actions/setup-go@v5
      with:
        go-version: "1.24"
    - name: Install dependencies
      shell: bash
      run: go get .
    - name: Build
      shell: bash
      run: go build -v ./...
```

### 3. `.github/workflows/check.yml` (new file)

Four parallel jobs: gofumpt diff, go vet, golangci-lint, govulncheck.

```yaml
---
name: Check
on:
  pull_request:
    branches: [main]
  workflow_dispatch:
jobs:
  format:
    runs-on: blacksmith-2vcpu-ubuntu-2404-arm
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup
      - name: Install gofumpt
        shell: bash
        run: go install mvdan.cc/gofumpt@latest
      - name: Check formatting
        shell: bash
        run: gofumpt -d .
  vet:
    runs-on: blacksmith-2vcpu-ubuntu-2404-arm
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup
      - name: Vet
        run: go vet ./...
  lint:
    runs-on: blacksmith-2vcpu-ubuntu-2404-arm
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup
      - name: Install linter
        uses: golangci/golangci-lint-action@v8
      - name: Lint
        run: golangci-lint run ./...
  vulncheck:
    runs-on: blacksmith-2vcpu-ubuntu-2404-arm
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup
      - name: Install govulncheck
        shell: bash
        run: go install golang.org/x/vuln/cmd/govulncheck@latest
      - name: Vulnerability check
        shell: bash
        run: govulncheck ./...
```

**Notes:**

- `gofumpt -d .` outputs a unified diff of formatting issues and exits non-zero if any files need formatting. More informative than `-l` (which only lists filenames).
- govulncheck is installed at runtime (CI-only tool, not needed locally)
- Each job checks out and runs setup independently (GitHub Actions jobs are isolated)

### 4. `.github/workflows/test.yml` (new file)

Single test job using gotestsum:

```yaml
---
name: Test
on:
  pull_request:
    branches: [main]
  workflow_dispatch:
jobs:
  test:
    runs-on: blacksmith-2vcpu-ubuntu-2404-arm
    steps:
      - uses: actions/checkout@v4
      - uses: ./.github/actions/setup
      - name: Install gotestsum
        shell: bash
        run: go install gotest.tools/gotestsum@latest
      - name: Test
        shell: bash
        run: gotestsum ./...
```

## Dependencies

None — all tools are either already installed locally or installed at runtime in CI. No changes to `go.mod`.

## Considerations & Trade-offs

1. **Lefthook jobs with nested groups vs legacy `commands` with `priority`**: The legacy `commands` + `priority` approach doesn't support same-priority parallel execution — commands always run sequentially regardless. The `jobs` feature (v1.10.0+) with nested `group` blocks cleanly separates the piped fixers stage from the parallel checks stage. This is the recommended approach for mixed sequential/parallel workflows.

2. **gofumpt in CI — `-d` vs `-l`**: `gofumpt -d .` outputs a unified diff and exits non-zero when files need formatting. `-l` only lists filenames. The `-d` approach gives developers actionable output to fix formatting issues.

3. **govulncheck CI-only**: govulncheck is slow and requires network access to fetch vulnerability data. Not suitable for a pre-commit hook. Installing at runtime in CI avoids requiring it locally.

4. **No .golangci.yml**: The alchemist project also uses default golangci-lint settings. We'll follow the same approach — add a config file later if specific linters need tuning.

## Migration / Data Changes

None.

## Testing Strategy

This feature is configuration-only (YAML files). Testing is manual:

- **Lefthook**: Run `lefthook install` to install hooks, then make a commit with a deliberately unformatted Go file to verify the fixer pipeline auto-corrects it and the checks pass.
- **GitHub Actions**: Push the branch, open a PR against main, and verify all four check jobs and the test job pass.
- **Verify gofumpt diff**: Commit an unformatted file and confirm the `format` job fails in CI.

## Todo List

### Phase 1: Shared Setup
- [x] Create `.github/actions/setup/action.yml` composite action

### Phase 2: Lefthook
- [x] Create `lefthook.yml` with piped fixers group and parallel checks group
- [x] Run `lefthook install` to register hooks

### Phase 3: GitHub Actions — Check
- [x] Create `.github/workflows/check.yml` with format, vet, lint, and vulncheck jobs

### Phase 4: GitHub Actions — Test
- [x] Create `.github/workflows/test.yml` with gotestsum job

### Phase 5: Verification
- [x] Run `lefthook run pre-commit` to verify hooks work locally
- [x] Confirm all Go files pass `gofumpt -d .` (no formatting issues)
- [x] Confirm `go vet ./...` passes
- [x] Confirm `golangci-lint run ./...` passes
- [x] Confirm `gotestsum ./...` passes
