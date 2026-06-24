## IGNORE: Automated single dependency bumps

**- Pattern:** PRs automatically bumping a single dependency module.
**- Justification:** Automated single dependency updates (e.g., from dependabot or renovate) are consistently autoclosed.
**- Files Affected:** `go.mod`, `go.sum`

## IGNORE: Automated clean-up of output formatting functions

**- Pattern:** Modifying standard output formatting functions (like `fmt.Fprintln` or `fmt.Fprintf`) to explicitly assign their return values to blank identifiers or check for errors.
**- Justification:** Any automated clean-up of these `fmt` calls is being rejected by reviewers, despite linting rules suggesting otherwise.
**- Files Affected:** `cmd/mclone/config.go`, `cmd/mclone/ls.go`

## IGNORE: Obvious or redundant docstrings

**- Pattern:** Documentation PRs adding obvious or redundant descriptions to types and functions (e.g., repeating the name of the type in the docstring).
**- Justification:** Project conventions require docstrings to focus on the 'why' and non-obvious nuances. Adding redundant explanations creates noise and is consistently rejected.
**- Files Affected:** `pkg/server/chat.go`, `pkg/server/middleware.go`, `pkg/server/models.go`, `pkg/server/server.go`, `pkg/server/types.go`, `pkg/message/event.go`, `pkg/message/message.go`, `pkg/message/request.go`

## IGNORE: Automated wrapping of deferred Close operations

**- Pattern:** Wrapping `defer resp.Body.Close()` or similar stream closing operations in anonymous functions to explicitly check the error and log it using `monitor.ReportError`.
**- Justification:** Automated patches attempting to enforce error checking on deferred `Close` calls are repeatedly rejected. Reviewers likely prefer the existing, simpler syntax or the fixes were poorly executed across multiple PRs.
**- Files Affected:** `pkg/providers/anthropic/anthropic.go`, `pkg/providers/antigravity/antigravity.go`, `pkg/providers/gemini/gemini.go`, `pkg/providers/ollama/ollama.go`, `pkg/providers/openai/openai.go`, `pkg/tools/duckduckgo/ddg.go`, `pkg/tools/webfetch/webfetch.go`, `pkg/config/config.go`
