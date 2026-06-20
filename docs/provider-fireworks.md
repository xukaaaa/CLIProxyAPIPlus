# Fireworks Provider Design Notes

Branch: `provider/fireworks`

## Goal

Add first-class native support for [Fireworks AI](https://fireworks.ai) as a provider.

Phase 1 supports Fireworks' Anthropic-compatible endpoint only:

- `/v1/messages` — Anthropic Messages API compatible.

`/v1/responses` is deferred until Fireworks Responses API docs are available.

## Confirmed Fireworks Messages Details

- SDK base URL: `https://api.fireworks.ai/inference`
- Full messages endpoint: `https://api.fireworks.ai/inference/v1/messages`
- Auth: `Authorization: Bearer <api_key>`
- `anthropic-version` header is not required and is ignored.
- Non-streaming and streaming SSE are supported.
- Non-streaming responses include top-level `usage`.
- Streaming responses include real token usage in the final `message_delta` event.
- Streaming `message_start.usage` values are always zero and should not be used for metering.

Manual smoke tests confirmed temporary model:

- `accounts/fireworks/models/kimi-k2p7-code`

`max_tokens: 4097` with non-streaming returned `200` for short output, so phase 1 does not enforce a proxy-side `max_tokens > 4096` block.

## Config Shape

Fireworks API keys mirror existing Claude/Codex/Gemini API-key config style: one list item per key.

```yaml
fireworks-api-key:
  - api-key: ${FIREWORKS_API_KEY_1}
  - api-key: ${FIREWORKS_API_KEY_2}
```

Each item becomes a separate `fireworks` auth entry. Multiple keys sharing the same provider/model are selected by existing auth routing/round-robin behavior.

Optional per-key fields follow existing provider patterns:

```yaml
fireworks-api-key:
  - api-key: ${FIREWORKS_API_KEY_1}
    base-url: https://api.fireworks.ai/inference
    prefix: test
    headers:
      X-Custom-Header: custom-value
    proxy-url: socks5://proxy.example.com:1080
    models:
      - name: accounts/fireworks/models/kimi-k2p7-code
        alias: kimi-code
    excluded-models:
      - accounts/fireworks/models/legacy-*
```

## Phase 1 Architecture

Create a native `FireworksExecutor` instead of reusing `ClaudeExecutor` directly.

Rationale:

- Fireworks uses Claude message shapes but not Anthropic-specific request details.
- Avoid sending Anthropic-only `?beta=true`.
- Avoid `x-api-key`; Fireworks uses bearer auth.
- Avoid Claude OAuth tool remapping and CCH signing.
- Avoid auto-injecting Anthropic cache-control optimizations.

Phase 1 executor behavior:

- Translate inbound requests to Claude message format via existing translator.
- POST to `<base_url>/v1/messages`.
- Default `base_url` to `https://api.fireworks.ai/inference`.
- Preserve client streaming contract:
  - client non-stream -> upstream non-stream -> JSON response
  - client stream -> upstream stream -> SSE response
- Pass unsupported/unknown Fireworks fields upstream; do not locally validate server tools, `output_config.speed`, or adaptive thinking.

## Implemented/Required Changes

1. **Config**
   - Add `fireworks-api-key` list config.
   - Support multiple API keys from the start.
   - Support optional `base-url`, `prefix`, `headers`, `proxy-url`, `models`, `excluded-models`, and `disable-cooling`.

2. **Auth synthesis**
   - Synthesize one `fireworks` auth per API key.
   - Store API key in `Attributes["api_key"]`.
   - Store optional base URL in `Attributes["base_url"]`.

3. **Executor**
   - Add `internal/runtime/executor/fireworks_executor.go`.
   - Use bearer auth.
   - Use default base URL `https://api.fireworks.ai/inference`.
   - Parse non-stream usage with Claude usage parser.
   - Parse streaming usage only from `message_delta` events.

4. **Model definitions**
   - Add `fireworks` static model channel.
   - Add temporary model `accounts/fireworks/models/kimi-k2p7-code`.

5. **Service registration**
   - Register native `FireworksExecutor` for provider `fireworks`.
   - Register Fireworks static models for Fireworks auth entries.

6. **Config example**
   - Add `fireworks-api-key` example to `config.example.yaml`.

## Deferred Work

- `/v1/responses` support.
- Responses streaming/compact behavior.
- Full model list and capability metadata.
- Error-code-specific retry/status handling.
- Optional internal upstream-stream aggregation for non-stream clients.

## References

- Fireworks docs index: `https://docs.fireworks.ai/llms.txt`
- Fireworks Anthropic compatibility: `https://docs.fireworks.ai/tools-sdks/anthropic-compatibility.md`
- `internal/runtime/executor/claude_executor.go` — Anthropic Messages handling reference.
- `sdk/cliproxy/service.go` — executor/model registration entry point.
