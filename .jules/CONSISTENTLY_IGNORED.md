## IGNORE: Automated single dependency bumps

**- Pattern:** PRs automatically bumping a single dependency module (e.g., via dependabot or renovate).
**- Justification:** Automated single dependency updates are consistently autoclosed. Do not create PRs for single dependency bumps.
**- Files Affected:** `go.mod`, `go.sum`

## IGNORE: Automated wrapping of deferred Close operations

**- Pattern:** Wrapping `defer resp.Body.Close()` or similar stream closing operations in anonymous functions to explicitly check the error and log it using `monitor.ReportError`.
**- Justification:** Automated patches attempting to enforce error checking on deferred `Close` calls are repeatedly rejected. Reviewers prefer the existing simpler syntax.
**- Files Affected:** `pkg/providers/*/`, `pkg/tools/*/`, `pkg/config/config.go`

## IGNORE: Automated clean-up of output formatting functions

**- Pattern:** Modifying standard output formatting functions (like `fmt.Fprintln` or `fmt.Fprintf`) to explicitly assign their return values to blank identifiers (`_`) or check for errors.
**- Justification:** Any automated clean-up of these `fmt` calls is consistently rejected by reviewers, despite linting rules suggesting otherwise.
**- Files Affected:** `cmd/mclone/config.go`, `cmd/mclone/ls.go`

## IGNORE: Obvious or redundant docstrings

**- Pattern:** Documentation PRs adding obvious or redundant descriptions to types and functions (e.g., repeating the name of the type in the docstring).
**- Justification:** Project conventions require docstrings to focus on the 'why' and non-obvious nuances. Adding redundant explanations creates noise and is consistently rejected.
**- Files Affected:** `pkg/server/*.go`, `pkg/message/*.go`

## IGNORE: S1016 and S1039 code simplifications

**- Pattern:** Janitor PRs fixing `gosimple` S1016 (type conversion vs struct literal for identical types) and S1039 (simplifying constant strings or boolean algebra).
**- Justification:** These specific code simplification linting rules are consistently rejected when submitted as automated cleanup PRs.
**- Files Affected:** `pkg/chatui/chatui.go`, `cmd/mclone/chat.go`, `pkg/providers/*/`, `pkg/config/config.go`

## IGNORE: Default server binding vulnerability reports

**- Pattern:** Sentinel PRs flagging the default unauthenticated server listen interfaces (e.g. replacing `:%d` with `%s:%d` targeting `127.0.0.1`).
**- Justification:** The proxy server (`cmd/mclone/serve.go`) already defaults to listening on `127.0.0.1` (via the `--host` flag), so replacing the bind address for security reasons is a false positive and consistently rejected.
**- Files Affected:** `cmd/mclone/serve.go`
