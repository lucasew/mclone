# mclone Specification

Unified LLM gateway. Re-exposes different providers through OpenAI and Anthropic compatible APIs.

## Configuration

TOML at `~/.config/mclone/mclone.conf`.

### Provider Types

| Type | Options |
|------|---------|
| `anthropic` | `api_key` |
| `openai` | `api_key`, `base_url` |
| `gemini` | `api_key` |
| `ollama` | `base_url` |
| `route` | `<model> = "<remote>[:<backend_model>]"` |

### Implicit Balance

Remotes sharing a prefix separated by `:` form an automatic balanced pool.

```toml
[remotes."anth:1"]
type = "anthropic"
[remotes."anth:1".options]
api_key = "sk-key1"

[remotes."anth:2"]
type = "anthropic"
[remotes."anth:2".options]
api_key = "sk-key2"
```

Referencing `anth` resolves to a balanced group of `anth:1` + `anth:2`.

### Balance Behavior

- **Cache affinity**: system prompt hash determines backend sticky routing.
- **Rate limit handling**: on 429, if `retry_after <= failover_threshold` wait; otherwise failover to next backend.
- **`available_at`**: per-backend timestamp, starts at 0. Backend is available when `available_at <= now`.

### Failover Threshold Inheritance

```
specific remote > group config > default (60s)
```

A group config is a remote with no `type`, just shared options:

```toml
[remotes.anth]
[remotes.anth.options]
failover_threshold = "30s"
```

### Route

Maps client model names to remotes.

```toml
[remotes.smart]
type = "route"
[remotes.smart.options]
claude-sonnet-4-5 = "anth"              # keeps model name
fast = "gemini:gemini-2.5-flash"         # remaps model name
```

## Wire Protocols

| Endpoint | Protocol |
|----------|----------|
| `POST /v1/messages` | Anthropic |
| `POST /v1/chat/completions` | OpenAI |
| `GET /v1/models` | Model listing |

## Internal Representation

All conversions pass through `pkg/message` types, even when client and backend use the same protocol.

```
Wire request → message.Message → Provider → message.ChatResponse → Wire response
```
