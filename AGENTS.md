# Project Guidelines and Conventions

This document outlines the development guidelines, tooling, and conventions for the `mclone` project. All contributors (human and AI) must adhere to these rules.

## Tooling

We use [mise](https://mise.jdx.dev/) as the primary task runner and tool manager.

- **Installation:** Ensure `mise` is installed and available in your PATH.
- **Commands:**
    - `mise run install`: Install dependencies.
    - `mise run codegen`: Update generated code (e.g., `go:generate`).
    - `mise run test`: Run the test suite.
    - `mise run lint`: Run all linters (`golangci-lint`, `actionlint`).
    - `mise run fmt`: Format code (`go fmt`, `prettier`).
    - `mise run build`: Build the binary.
    - `mise run ci`: Run the full CI pipeline (lint, test, build).

**Note:** Always use `mise` to run tasks to ensure consistent environments and versions.

## Code Quality

### Linting

- **Go:** We use `golangci-lint` with a strict configuration.
    - To fix auto-fixable issues, run: `golangci-lint run --fix ./...` (or `mise exec -- golangci-lint run --fix ./...`).
- **GitHub Actions:** We use `actionlint` to verify workflow files.
- **Formatting:**
    - Go code is formatted with `go fmt` (and `gofumpt` via `golangci-lint`).
    - Non-Go files (Markdown, YAML, JSON) are formatted with `prettier`.

### Error Handling

- **Never ignore errors.** Every error must be handled or explicitly reported.
- **Centralized Reporting:** Use `pkg/monitor` for reporting unexpected errors that should be tracked.
    ```go
    import "github.com/lucasew/mclone/pkg/monitor"

    if err != nil {
        monitor.ReportError(ctx, err, "context", "value")
        // handle error flow (return, continue, etc.)
    }
    ```
- **Logging:** Use `log/slog` for standard logging.

### Testing

- Write unit tests for new functionality.
- Ensure `mise run test` passes before submitting.
- Tests should be meaningful and test the logic, not just the framework.

## CI/CD

- The CI pipeline is defined in `.github/workflows/autorelease.yml`.
- It automatically runs:
    1.  `install`
    2.  `codegen` (creates a PR if code changes)
    3.  `ci` (lint, test, build)
    4.  `release` (on tags)

## General Guidelines

- **Retroactive Violations First:** If you see existing code violating these rules, fix it first.
- **Don't Downgrade Dependencies:** Keep dependencies up-to-date unless there is a specific reason not to.
- **Codify the Vague:** If a convention is unclear, clarify it here.
