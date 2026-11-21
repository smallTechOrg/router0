# Capability: Provider Adapters

Phase 1: Groq and OpenRouter (OpenAI-compatible) hardened, with classification, usage in streams, and configurable base URLs. Phase 2: Anthropic (native Messages API), Gemini (tool-calling conformance), Mesh API by Fiesta Labs ("AI Fiesta Agent Mesh"; portal app.meshapi.ai). Existing NVIDIA, Together, and Baseten adapters stay registered but are not priorities.

## What It Does

Translates router0's internal `ChatRequest` into each provider's wire format, performs the HTTP call (streaming or not), translates responses and stream chunks back to OpenAI shape, lists models for the catalog, and classifies failures.

## Inputs

| Input | Source |
|-------|--------|
| `*providers.ChatRequest` (model = provider_model_id, messages, tools, tool_choice, stream, params) | Routing executor |
| Decrypted API key (parameter) | Executor attempt func |
| Base URL env var (read per call) | Environment / tests |

## Outputs

| Output | Destination |
|--------|-------------|
| `*ChatResponse` (content, tool_calls, finish_reason, usage) | Inference handler |
| `<-chan ChatChunk` (content delta, tool_call deltas, finish_reason, usage, or `Err`) | Inference handler |
| `[]ModelInfo` | Catalog discovery |
| `*ProviderError` | Routing executor |

## External Calls

| Provider | id | Base URL (env) | Auth | Chat | Models |
|----------|----|----------------|------|------|--------|
| Groq | `groq` | `GROQ_BASE_URL` = `https://api.groq.com/openai/v1` | `Authorization: Bearer` | `POST /chat/completions` | `GET /models` |
| OpenRouter | `openrouter` | `OPENROUTER_BASE_URL` = `https://openrouter.ai/api/v1` | Bearer + `HTTP-Referer: https://router0.local`, `X-Title: router0` | `POST /chat/completions` | `GET /models` |
| Anthropic (P2) | `anthropic` | `ANTHROPIC_BASE_URL` = `https://api.anthropic.com/v1` | `x-api-key`, `anthropic-version: 2023-06-01` | `POST /messages` | `GET /models?limit=1000` (paginate `after_id`) |
| Gemini (P2) | `google` (alias `gemini`) | `GOOGLE_BASE_URL` = `https://generativelanguage.googleapis.com` | Bearer on `/v1beta/openai/*`; `?key=` on native list | `POST /v1beta/openai/chat/completions` | `GET /v1beta/models` |
| Mesh API (P2) | `meshapi` (aliases `aifiesta`, `mesh`) | `MESHAPI_BASE_URL` = `https://api.meshapi.ai/v1` | `Authorization: Bearer rsk_…` | `POST /chat/completions` | `GET /models` |

All calls: on failure → `Classify` (error-classification.md). Response bodies of failed calls are read up to 64 KiB.

Assumed: Gemini keeps the provider id `google` so existing key rows need no data migration; the UI labels it "Gemini" and the header override accepts `gemini`.

Mesh API facts taken from the user's copy of Mesh's docs: base URL `https://api.meshapi.ai/v1`, `POST /v1/chat/completions` with `Authorization: Bearer rsk_…`, `Content-Type: application/json`, an OpenAI-style body, and vendor-prefixed model ids, e.g. `{"model":"mistral/mistral-7b-instruct-v0","messages":[{"role":"user","content":"Hello!"}]}`. Everything else below is Assumed and covered by fixtures so a wrong guess is a one-line fix.

## Business Rules

### Interface (extended, existing methods kept)

```go
type Provider interface {
    Name() string
    Complete(ctx context.Context, req *ChatRequest, apiKey string) (*ChatResponse, error)
    Stream(ctx context.Context, req *ChatRequest, apiKey string) (<-chan ChatChunk, error)
    HealthCheck(ctx context.Context, apiKey string) (*HealthResult, error)
    DefaultModels() []string
    ListModels(ctx context.Context, apiKey string) ([]ModelInfo, error)
}
type Classifier interface { Classify(status int, h http.Header, body []byte) *ProviderError }  // optional
```

`ChatRequest` gains `TopP *float64`, `ResponseFormat any`, `Seed *int`, `User string`. `Temperature` becomes `*float64`, so that `0` is forwarded and not dropped by `omitempty` (a current bug). `ChatChunk` gains `Usage *Usage`, `Err *ProviderError`, and tool-call deltas carry `Index`. `ToolCall` gains `Index *int` for streaming.

### Phase 1 — OpenAI-compatible core (`openai_compat.go`)

- All non-2xx responses go through `Classify` (adapter's `Classifier` if implemented, else generic). The current ad-hoc 429/401/5xx mapping is replaced.
- Network errors produce a `transient` `ProviderError`, not a bare `error`.
- Timeouts come from the caller's context (the executor sets them). The hard-coded `http.Client{Timeout: 15s}` is removed, and one shared `http.Transport` with keep-alive is reused across calls.
- Streaming: send `stream_options: {"include_usage": true}`; parse `usage` from the final chunk into `ChatChunk.Usage`. Parse tool-call deltas with `index`. An SSE line `data: {"error":…}` → chunk with `Err` (classified). Scanner buffer raised to 1 MiB (long tool-argument lines).
- `tools`, `tool_choice`, assistant `tool_calls`, `tool` role messages with `tool_call_id`, and `content: null` on tool-calling assistant messages are forwarded unchanged.
- `ListModels` uses the configured base URL for every provider (fixes OpenRouter's hard-coded URL and removes its `:free`-only filter).
- Registration: each adapter registers in its own file's `init()`; `ValidProviders()` returns the sorted registry keys.

### Phase 2 — Anthropic native (`anthropic.go`, `anthropic_translate.go`, `anthropic_stream.go`)

Request translation (OpenAI → Anthropic Messages):

| OpenAI | Anthropic |
|--------|-----------|
| `system` / `developer` messages | concatenated into top-level `system` (string), in order |
| `user` string content | `{"role":"user","content":[{"type":"text","text":…}]}` |
| `assistant` content + `tool_calls[]` | `{"role":"assistant","content":[{"type":"text",…}?, {"type":"tool_use","id":tc.id,"name":tc.function.name,"input":<JSON-parsed arguments>}…]}`; unparseable arguments → `400 bad_request` before calling (not failover-worthy, but failover still proceeds per brief) |
| `tool` messages (`tool_call_id`, content) | `{"type":"tool_result","tool_use_id":…,"content":…}` blocks; consecutive tool messages merged into one `user` message |
| Consecutive same-role messages | merged (Anthropic requires alternation) |
| `tools[]` (`type:function`) | `{"name","description","input_schema": parameters or {"type":"object","properties":{}}}` |
| `tool_choice` `"auto"` / `"none"` / `"required"` / `{"type":"function","function":{"name":X}}` | `{"type":"auto"}` / `{"type":"none"}` / `{"type":"any"}` / `{"type":"tool","name":X}` |
| `parallel_tool_calls: false` | `tool_choice.disable_parallel_tool_use: true` |
| `max_tokens` (absent) | required by Anthropic; default `4096`. Assumed |
| `temperature` | clamped to [0, 1]. Assumed |
| `stop` | `stop_sequences` |
| `top_p` | `top_p` |

Response translation: `text` blocks are concatenated into `message.content`; `tool_use` blocks → `tool_calls[{id, type:"function", function:{name, arguments: JSON-encoded input}}]`. `stop_reason`: `end_turn`/`stop_sequence` → `stop`, `max_tokens` → `length`, `tool_use` → `tool_calls`. `usage.input_tokens`/`output_tokens` → `prompt_tokens`/`completion_tokens`.

Streaming translation: `message_start` → first chunk with `role: assistant` (also prompt-token usage). `content_block_start` (tool_use) → `tool_calls[{index:i, id, type, function:{name, arguments:""}}]`. `content_block_delta` `text_delta` → `content`; `input_json_delta` → `tool_calls[{index:i, function:{arguments: partial_json}}]`. `message_delta` → `finish_reason` + output usage. `message_stop` → done. `ping` is ignored. `error` event → `Err`. Tool-call `index` counts tool_use blocks only, starting at 0.

### Phase 2 — Gemini (`google.go`)

Kept on Google's OpenAI-compatible endpoint (extend, not rewrite). Phase 2 adds a conformance test suite against mock and optional real keys: tools, `tool_choice`, multiple tool calls in one response, tool-result round trip, and streaming tool calls. It also adds fixes that normalise compat-layer quirks (missing tool-call ids → generate `call_<n>`; array-wrapped error bodies; `content: null`). `ListModels` uses native `GET /v1beta/models` and returns every listed model with its `supportedGenerationMethods`; the catalog decides the kind (model-catalog.md › Model kinds), so image, TTS, music, computer-use, and robotics models do not reach `/v1/models`. Assumed: Google's OpenAI-compatible endpoint supports function calling well enough; if the conformance suite finds an unfixable gap, the slice switches only the failing path to native `generateContent` translation, inside `google.go`.

### Phase 2 — Mesh API (`meshapi.go`)

Thin adapter over `openai_compat.go` in `internal/providers/meshapi.go`, self-registered under id `meshapi`; `registry.go` maps the aliases `aifiesta` and `mesh` to it (so `X-Router0-Providers: aifiesta`, `POST /api/keys {"provider":"mesh"}` work; the stored and returned id is always `meshapi`). Assumed: the aliases are a convenience; the user calls the product "AI Fiesta Agent Mesh".

- Base URL read per call from `MESHAPI_BASE_URL` (default `https://api.meshapi.ai/v1`), mirroring `GROQ_BASE_URL` / `OPENROUTER_BASE_URL`; tests point it at the `:9905` mock or an `httptest` server. Chat URL `{base}/chat/completions`; model list URL `{base}/models`.
- Auth: `Authorization: Bearer <key>`. Keys look like `rsk_…` (key hint `rsk_xxxx…`). No other required headers. Assumed: optional attribution headers are not needed; router0 sends none.
- Model ids are sent exactly as discovered (vendor-prefixed, e.g. `mistral/mistral-7b-instruct-v0`); the catalog stores that string as `provider_model_id` and the canonical name is derived by stripping the prefix and normalising (model-catalog.md fixture rows).
- `ListModels`: `GET {base}/models`, parsed as the OpenAI list shape `{"data":[{"id":…}]}`. Assumed: Mesh returns that shape; pricing/context fields are read when present and left NULL otherwise.
- Streaming, tool calls, `tool_choice`, and `stream_options.include_usage` are passed through unchanged. Assumed: Mesh forwards them to the underlying vendor like OpenRouter does. If a model behind Mesh rejects `stream_options` with a 400, the adapter retries that one attempt once without it (a `meshapi.go`-local fallback; usage is then estimated as unknown and logged `NULL`). Assumed.
- Mesh may front non-chat models; the catalog's kind classification filters them from `/v1/models`.
- Classification: generic rules, no `Classifier` override until Mesh's error shapes are verified (error-classification.md).

## Success Criteria

- [ ] Phase 1 unit (httptest): Groq and OpenRouter `Complete` and `Stream` produce the expected `ChatResponse`/chunks, including tool-call deltas with indices and the final usage chunk.
- [ ] Phase 1 unit: `temperature: 0` is present in the upstream JSON body.
- [ ] Phase 1 unit: OpenRouter `ListModels` hits `OPENROUTER_BASE_URL/models` and returns paid and `:free` models.
- [ ] Phase 1 unit: a 16 KB single SSE line (large tool arguments) is parsed.
- [ ] Phase 2 unit: Anthropic translation golden tests: (a) system + user; (b) assistant with 2 tool_calls + 2 tool results → one assistant message with 2 `tool_use` blocks and one user message with 2 `tool_result` blocks; (c) every `tool_choice` mapping; (d) response with text + tool_use → OpenAI message with `content` and `tool_calls`, `finish_reason: tool_calls`.
- [ ] Phase 2 unit: recorded Anthropic SSE fixture with text then a tool_use whose JSON arrives in 3 `input_json_delta`s → OpenAI chunks whose concatenated `arguments` parse to the original object.
- [ ] Phase 2 unit (Mesh): request goes to `POST {MESHAPI_BASE_URL}/chat/completions` with `Authorization: Bearer rsk_…` and the vendor-prefixed `model` unchanged; `ListModels` parses a fixture listing containing `mistral/mistral-7b-instruct-v0`; a streamed tool-call fixture yields indexed deltas and a final usage chunk; alias `aifiesta` resolves to `meshapi` in `providers.CanonicalName`.
- [ ] Phase 2 integration (tool calling, the user's priority): an OpenAI-SDK-style agent loop (tools → `tool_calls` → `tool` message → final answer; parallel calls; two rounds; non-stream and stream) through router0 to the mock Groq, OpenRouter, Anthropic, Gemini, and Mesh servers gives the same final OpenAI-shaped output on every flavour (`TestPhase2_AgentLoopToolCalling`, `TestPhase2_AgentLoopToolCallingStream`).
- [ ] Phase 2 smoke (optional, real keys; Gemini is the priority): the same tool-calling conversation, non-stream and stream, succeeds against real Gemini, Anthropic, and Mesh API when `TEST_ANTHROPIC_KEY` / `TEST_GEMINI_KEY` / `TEST_MESHAPI_KEY` are set.
