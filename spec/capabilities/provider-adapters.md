# Capability: Provider Adapters

## What It Does

Implements a common Go interface for all supported LLM providers. Each provider is a single-file plugin that translates an internal `ChatRequest` into the provider's native HTTP request format, executes the call (with streaming SSE support), and translates the response back into the internal `ChatResponse` format. New providers are added by implementing the interface and registering the file — no changes to core routing logic.

## Inputs

| Input | Type | Source | Required |
|-------|------|---------|----------|
| Internal `ChatRequest` | Go struct | Routing Engine / request handler | Yes |
| Decrypted provider API key | String (in-memory only) | Key Management (AES-decrypt at call time) | Yes |
| `context.Context` with deadline | Go context | Request handler | Yes |

## Outputs

| Output | Type | Destination |
|--------|------|-------------|
| Streaming SSE channel or `io.ReadCloser` | Go channel / reader | Routing Engine → HTTP response writer |
| `ChatResponse` (non-streaming) | Go struct | Routing Engine (for metadata recording) |
| Provider error with classification | `ProviderError` struct | Routing Engine (for retry/hedge decision) |

## External Calls

| System | Operation | On Failure |
|--------|-----------|------------|
| Google AI Studio API | `POST https://generativelanguage.googleapis.com/v1beta/models/{model}:streamGenerateContent` | Return `ProviderError{Class: ErrClassProviderError}` |
| Groq Cloud API | `POST https://api.groq.com/openai/v1/chat/completions` (OpenAI-compat) | Return `ProviderError` |
| OpenRouter API | `POST https://openrouter.ai/api/v1/chat/completions` (OpenAI-compat) | Return `ProviderError` |
| NVIDIA NIM API | `POST https://integrate.api.nvidia.com/v1/chat/completions` (OpenAI-compat) | Return `ProviderError` |
| Together AI API | `POST https://api.together.xyz/v1/chat/completions` (OpenAI-compat) | Return `ProviderError` |
| Baseten API | `POST https://model-<id>.api.baseten.co/production/predict` | Return `ProviderError` |

## Business Rules

### Provider Interface

Every adapter implements:

```go
type Provider interface {
    Name() string
    Models() []string
    Complete(ctx context.Context, req *ChatRequest, key string) (*ChatResponse, error)
    Stream(ctx context.Context, req *ChatRequest, key string) (<-chan StreamChunk, error)
    Probe(ctx context.Context, key string) (*ProbeResult, error)
}
```

- `Name()` returns the canonical provider identifier string (matches `provider_keys.provider` column).
- `Models()` returns the list of model slugs this adapter supports.
- `Complete()` is the non-streaming path.
- `Stream()` opens a streaming SSE connection and returns a channel that emits `StreamChunk` values; the channel is closed when the stream ends or the context is cancelled.
- `Probe()` sends a minimal-token request (prompt: "hi", max_tokens: 1) and returns latency + status.

### Supported Providers and Models (Phase 1)

| Provider | Canonical Name | Models |
|----------|---------------|--------|
| Google AI Studio | `google` | `gemini-1.5-flash`, `gemini-1.5-pro` |
| Groq Cloud | `groq` | `llama-3.1-70b-versatile`, `mixtral-8x7b-32768` |
| OpenRouter | `openrouter` | All models available under the user's OpenRouter key (aggregator) |
| NVIDIA NIM | `nvidia` | `meta/llama-3.1-70b-instruct`, `meta/llama-3.1-8b-instruct` |
| Together AI | `together` | `meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo`, `mistralai/Mixtral-8x7B-Instruct-v0.1` |
| Baseten | `baseten` | Model IDs are user-configured at key registration time (not hardcoded) |

### Streaming SSE

- The adapter opens an HTTP/1.1 persistent connection to the provider and reads SSE data lines.
- Each `data: {…}` line is parsed into a `StreamChunk{Delta string, FinishReason string, InputTokens int, OutputTokens int}`.
- The adapter emits each chunk on the returned channel immediately — no buffering.
- On `data: [DONE]` or context cancellation, the channel is closed.
- Token counts are accumulated from the final chunk's `usage` field (or summed from streaming deltas where the provider supplies per-chunk counts).

### Error Classification

`ProviderError` carries a `Class` field:

| Class | Meaning | Routing Engine action |
|-------|---------|----------------------|
| `ErrClassRateLimit` | Provider returned 429; user key exhausted | Mark key rate-limited; do not select for the exhausted window |
| `ErrClassProviderThrottle` | Provider blocked at platform/IP level (not key-specific) | Mark provider globally throttled; skip all keys for this provider |
| `ErrClassAuth` | 401/403 — key is invalid or revoked | Set key `status = error`; remove from pool |
| `ErrClassTimeout` | Context deadline exceeded before first byte | Count toward p95 latency; eligible for retry |
| `ErrClassUpstream` | 5xx from provider | Count toward error rate; eligible for retry |
| `ErrClassUnknown` | Any other error | Count toward error rate; eligible for retry |

### Rate-Limit Header Parsing

Adapters for OpenAI-compatible providers (Groq, OpenRouter, NVIDIA, Together) must parse these response headers when present and return them in `ChatResponse.RateLimitHints`:

- `x-ratelimit-remaining-requests`
- `x-ratelimit-remaining-tokens`
- `x-ratelimit-reset-requests`
- `x-ratelimit-reset-tokens`

Google AI Studio uses `x-goog-quota-remaining`. NVIDIA NIM uses `x-ratelimit-remaining-requests`. Baseten does not expose rate-limit headers; use probe-only health assessment.

### Google AI Studio Adapter

Google's API uses a different request/response schema from OpenAI. The adapter translates:
- `messages` array → `contents` array (roles `user`/`assistant` → `user`/`model`)
- `max_tokens` → `generationConfig.maxOutputTokens`
- Response `candidates[0].content.parts[0].text` → `message.content`
- Streaming uses `?alt=sse` query param and returns SSE lines.

### Baseten Adapter

Baseten model IDs are deployment-specific. The `provider_keys` record for Baseten stores the model ID in an additional `model_id` column set at key registration. The adapter constructs the endpoint URL as `https://model-{model_id}.api.baseten.co/production/predict`. The request/response schema is OpenAI-compatible for Baseten-hosted models.

### OpenRouter Adapter

OpenRouter accepts any model slug via the `model` field. When the user's request specifies `model: "auto"` or omits the model, the adapter passes through the OpenRouter-selected model. The `X-OpenRouter-Model-Used` response header is captured and reported in `ChatResponse.ActualModel`.

### Adapter Registration

Adapters are registered at process startup in `internal/providers/registry.go`:

```go
var registry = map[string]Provider{
    "google":      &GoogleProvider{},
    "groq":        &GroqProvider{},
    "openrouter":  &OpenRouterProvider{},
    "nvidia":      &NvidiaProvider{},
    "together":    &TogetherProvider{},
    "baseten":     &BasetenProvider{},
}
```

Adding a new provider: implement the interface, add one file under `internal/providers/`, add one line to the registry map.

## Success Criteria

- [ ] `GroqProvider.Stream()` with a valid Groq API key and prompt "count to 3" returns at least 3 `StreamChunk` values on the channel and the channel closes without error.
- [ ] `GoogleProvider.Complete()` with a valid Google AI Studio key and prompt "say hi" returns a non-empty `ChatResponse.Message.Content` and `InputTokens > 0`.
- [ ] `GroqProvider.Probe()` with a valid key returns `ProbeResult{OK: true, LatencyMs: <N>}` with N < 5000.
- [ ] An intentionally invalid API key passed to any adapter's `Complete()` returns a `ProviderError` with `Class = ErrClassAuth`.
- [ ] A request to Groq with a deliberately exhausted key (mocked 429 response in unit test) returns `ProviderError{Class: ErrClassRateLimit}`.
- [ ] The Google adapter correctly maps `messages: [{role:"user", content:"hello"}]` to `contents: [{role:"user", parts:[{text:"hello"}]}]` — verified by an integration test that asserts a non-error response.
- [ ] `OpenRouterProvider.Stream()` streams at least one chunk and the `ChatResponse.ActualModel` is non-empty.
- [ ] Cancelling the context passed to `Stream()` causes the channel to close within 200 ms.
