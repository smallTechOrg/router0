package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/auth"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/metadata"
	"github.com/zer0-router/zer0/internal/metrics"
	"github.com/zer0-router/zer0/internal/providers"
	"github.com/zer0-router/zer0/internal/routing"
)

type InferenceHandler struct {
	selector *routing.Selector
	recorder *metadata.Recorder
	rl       *routing.RateLimiter
	keyRepo  *keys.Repository
}

func NewInferenceHandler(pg *pgxpool.Pool, r *redis.Client) *InferenceHandler {
	return &InferenceHandler{
		selector: routing.NewSelector(r, pg),
		recorder: metadata.NewRecorder(pg),
		rl:       routing.NewRateLimiter(r),
		keyRepo:  keys.NewRepository(pg),
	}
}

type chatRequest struct {
	Model       string                   `json:"model"`
	Messages    []providers.ChatMessage  `json:"messages"`
	MaxTokens   int                      `json:"max_tokens,omitempty"`
	Temperature float64                  `json:"temperature,omitempty"`
	Stream      bool                     `json:"stream,omitempty"`
	Stop        []string                 `json:"stop,omitempty"`
}

func (h *InferenceHandler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	userID := auth.UserIDFromCtx(r.Context())
	requestID := "zer0-" + newUUID()

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "validation_error", "invalid_json", "Invalid request body")
		return
	}

	provReq := &providers.ChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
		Stop:        req.Stop,
	}

	if req.Stream {
		h.handleStream(w, r, userID, requestID, provReq, start)
		return
	}

	// Non-streaming path
	scored, err := h.selector.Pool().SelectCombo(r.Context(), userID)
	if err != nil || len(scored) == 0 {
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "no_providers", "No providers available")
		return
	}

	var (
		resp     *providers.ChatResponse
		selCombo *routing.SelectedCombo
		retryCount int
		lastErr  error
	)

	tried := map[string]bool{}
	for i, sc := range scored {
		if i >= 4 || tried[sc.ProviderKeyID] {
			continue
		}
		tried[sc.ProviderKeyID] = true

		k, err := h.keyRepo.GetByID(r.Context(), sc.ProviderKeyID, userID)
		if err != nil {
			continue
		}
		decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
		if err != nil {
			continue
		}
		p, err := providers.Get(sc.Provider)
		if err != nil {
			continue
		}
		resp, err = p.Complete(r.Context(), provReq, decrypted)
		if err == nil {
			selCombo = &routing.SelectedCombo{Combo: sc.Combo, DecryptedKey: decrypted}
			break
		}
		lastErr = err
		retryCount++
		slog.Warn("inference: provider failed", "provider", sc.Provider, "error", err.Error())
		if pe, ok := err.(*providers.ProviderError); ok {
			metrics.ProviderErrors.WithLabelValues(sc.Provider, sc.Model, string(pe.Class)).Inc()
		}
	}

	latencyMs := time.Since(start).Milliseconds()

	if selCombo == nil {
		slog.Error("inference: all providers failed", "user_id", userID, "last_error", fmt.Sprint(lastErr))
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "all_providers_failed", "All available providers failed")
		return
	}

	w.Header().Set("X-Zer0-Model", selCombo.Model)
	w.Header().Set("X-Zer0-Provider", selCombo.Provider)
	w.Header().Set("X-Zer0-Latency-Ms", strconv.FormatInt(latencyMs, 10))
	w.Header().Set("X-Zer0-Request-Id", requestID)
	w.Header().Set("Content-Type", "application/json")

	slog.Info("request_completed",
		"event", "request_completed",
		"request_id", requestID,
		"user_id", userID,
		"provider", selCombo.Provider,
		"model", selCombo.Model,
		"latency_ms", latencyMs,
		"status", "success",
	)

	metrics.RequestsTotal.WithLabelValues(selCombo.Provider, selCombo.Model, "success").Inc()
	metrics.RequestDuration.WithLabelValues(selCombo.Provider, selCombo.Model).Observe(float64(latencyMs) / 1000)

	inputTokens := resp.Usage.PromptTokens
	outputTokens := resp.Usage.CompletionTokens
	cost := metadata.EstimateCost(selCombo.Provider, selCombo.Model, inputTokens, outputTokens)

	h.recorder.RecordAsync(metadata.Record{
		UserID:           userID,
		Zer0RequestID:    requestID,
		Provider:         selCombo.Provider,
		Model:            selCombo.Model,
		ProviderKeyID:    selCombo.ProviderKeyID,
		Pool:             selCombo.Pool,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		LatencyMs:        latencyMs,
		EstimatedCostUSD: cost,
		Status:           "success",
		RetryCount:       retryCount,
	})
	_ = h.rl.Increment(r.Context(), userID, selCombo.Provider, selCombo.Model)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   selCombo.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": resp.Content},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	})
}

func (h *InferenceHandler) handleStream(w http.ResponseWriter, r *http.Request, userID, requestID string, req *providers.ChatRequest, start time.Time) {
	// Find combo
	scored, err := h.selector.Pool().SelectCombo(r.Context(), userID)
	if err != nil || len(scored) == 0 {
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "no_providers", "No providers available")
		return
	}

	var (
		ch       <-chan providers.ChatChunk
		selCombo *routing.SelectedCombo
		retryCount int
	)

	tried := map[string]bool{}
	for i, sc := range scored {
		if i >= 4 || tried[sc.ProviderKeyID] {
			continue
		}
		tried[sc.ProviderKeyID] = true

		k, err := h.keyRepo.GetByID(r.Context(), sc.ProviderKeyID, userID)
		if err != nil {
			continue
		}
		decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
		if err != nil {
			continue
		}
		p, err := providers.Get(sc.Provider)
		if err != nil {
			continue
		}
		ch, err = p.Stream(r.Context(), req, decrypted)
		if err == nil {
			selCombo = &routing.SelectedCombo{Combo: sc.Combo, DecryptedKey: decrypted}
			break
		}
		retryCount++
		slog.Warn("inference: stream provider failed", "provider", sc.Provider, "error", err.Error())
	}

	if selCombo == nil {
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "all_providers_failed", "All available providers failed")
		return
	}

	latencyMs := time.Since(start).Milliseconds()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Zer0-Model", selCombo.Model)
	w.Header().Set("X-Zer0-Provider", selCombo.Provider)
	w.Header().Set("X-Zer0-Latency-Ms", strconv.FormatInt(latencyMs, 10))
	w.Header().Set("X-Zer0-Request-Id", requestID)

	flusher, canFlush := w.(http.Flusher)
	bw := bufio.NewWriter(w)

	var inputTokens, outputTokens int
	chunkCount := 0

	for chunk := range ch {
		if chunk.Done {
			fmt.Fprint(bw, "data: [DONE]\n\n")
			bw.Flush()
			if canFlush {
				flusher.Flush()
			}
			break
		}
		// Pass through the raw SSE line if available
		if chunk.Raw != "" {
			fmt.Fprintf(bw, "%s\n\n", chunk.Raw)
		} else {
			// Build SSE data
			data := map[string]interface{}{
				"id":     chunk.ID,
				"object": "chat.completion.chunk",
				"model":  selCombo.Model,
				"choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]string{"content": chunk.Content}},
				},
			}
			b, _ := json.Marshal(data)
			fmt.Fprintf(bw, "data: %s\n\n", string(b))
		}
		bw.Flush()
		if canFlush {
			flusher.Flush()
		}
		chunkCount++
		outputTokens += countTokensApprox(chunk.Content)
	}

	// Estimate input tokens from messages
	for _, msg := range req.Messages {
		inputTokens += countTokensApprox(msg.Content)
	}

	totalLatency := time.Since(start).Milliseconds()
	cost := metadata.EstimateCost(selCombo.Provider, selCombo.Model, inputTokens, outputTokens)

	slog.Info("request_completed",
		"event", "stream_completed",
		"request_id", requestID,
		"user_id", userID,
		"provider", selCombo.Provider,
		"model", selCombo.Model,
		"latency_ms", totalLatency,
		"chunks", chunkCount,
		"status", "success",
	)

	metrics.RequestsTotal.WithLabelValues(selCombo.Provider, selCombo.Model, "success").Inc()
	metrics.RequestDuration.WithLabelValues(selCombo.Provider, selCombo.Model).Observe(float64(totalLatency) / 1000)

	h.recorder.RecordAsync(metadata.Record{
		UserID:           userID,
		Zer0RequestID:    requestID,
		Provider:         selCombo.Provider,
		Model:            selCombo.Model,
		ProviderKeyID:    selCombo.ProviderKeyID,
		Pool:             selCombo.Pool,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		LatencyMs:        totalLatency,
		EstimatedCostUSD: cost,
		Status:           "success",
		RetryCount:       retryCount,
	})
	_ = h.rl.Increment(r.Context(), userID, selCombo.Provider, selCombo.Model)
}

func countTokensApprox(s string) int {
	// Rough approximation: 1 token ≈ 4 chars
	return (len(strings.TrimSpace(s)) + 3) / 4
}

func newUUID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// ListModels handles GET /v1/models — returns the single Zer0 model identity.
// Clients that require a model list (e.g. Hermes) will see "zer0-0.1"; the
// router ignores the model field in requests and routes via the pool instead.
func ListModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "zer0-0.1",
				"object":   "model",
				"created":  1700000000,
				"owned_by": "zer0-router",
			},
		},
	})
}
