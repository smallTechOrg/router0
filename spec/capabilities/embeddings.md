# Capability: Embeddings

Phase 3.

## What It Does

Serves `POST /v1/embeddings` with the same token auth, catalog resolution, fixed priority, header override, failover, error precedence, and request logging as chat completions.

## Inputs

| Input | Source |
|-------|--------|
| `{"model","input": string | string[],"encoding_format"?,"dimensions"?}` | Backend service |
| Catalog rows with `kind='embedding'` | Model catalog |

## Outputs

| Output | Destination |
|--------|-------------|
| OpenAI embeddings response | Client |
| `request_metadata` row with `endpoint='embeddings'`, `output_tokens=0` | PostgreSQL |

## External Calls

| Provider | Endpoint | Notes |
|----------|----------|-------|
| Gemini | `POST /v1beta/openai/embeddings` | OpenAI-compatible |
| Mesh API | `POST /v1/embeddings` | Assumed available (OpenAI-compatible) |
| OpenRouter | `POST /api/v1/embeddings` | Assumed available; if the endpoint returns 404 the adapter reports no embedding models |
| NVIDIA, Together | `POST /v1/embeddings` | existing adapters, OpenAI-compatible |
| Groq, Anthropic | — | No embeddings API; not `Embedder` |

## Business Rules

- Handler `handlers/embeddings.go` reuses `routing.BuildPlan` + `routing.Executor.Run` with an embed attempt func. Only providers implementing `providers.Embedder` are planned.
- Resolution uses kind `embedding`; a model present only as `chat` → 404 `model_not_found`.
- `input` arrays are forwarded as-is (no batching or splitting). Assumed.
- `encoding_format: "base64"` is passed through when the provider supports it; otherwise router0 requests `float` and base64-encodes little-endian float32 itself.
- Embedding discovery: `internal/catalog/embedding_discovery.go` classifies listed models (Gemini `embedContent`; names containing `embed`).
- Errors and headers are identical to chat (error-classification.md).

## Success Criteria

- [ ] Mock Gemini embeddings returns a 3-dim vector → `POST /v1/embeddings` with `model: "text-embedding-004"` returns `data[0].embedding` of length 3 and `usage.prompt_tokens`.
- [ ] Mock Gemini 429 → failover to mock Mesh API embeddings; trail of 2.
- [ ] `model: "llama-3.3-70b"` on `/v1/embeddings` → 404 `model_not_found`.
- [ ] `encoding_format: "base64"` returns a base64 string that decodes to 3 float32s.
- [ ] The request log shows `endpoint: embeddings`.
