package handlers

import (
	"bufio"
	"context"
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
	selector    *routing.Selector
	recorder    *metadata.Recorder
	rl          *routing.RateLimiter
	rlObserver  *routing.RateLimitObserver
	remediator  *routing.Remediator
	keyRepo     *keys.Repository
}

func NewInferenceHandler(pg *pgxpool.Pool, r *redis.Client) *InferenceHandler {
	sel := routing.NewSelector(r, pg)
	return &InferenceHandler{
		selector:    sel,
		recorder:    metadata.NewRecorder(pg),
		rl:          routing.NewRateLimiter(r),
		rlObserver:  routing.NewRateLimitObserver(pg, r),
		remediator:  routing.NewRemediator(pg, sel.Pool()),
		keyRepo:     keys.NewRepository(pg),
	}
}

type chatRequest struct {
	Model          string                  `json:"model"`
	Messages       []providers.ChatMessage `json:"messages"`
	MaxTokens      int                     `json:"max_tokens,omitempty"`
	Temperature    float64                 `json:"temperature,omitempty"`
	Stream         bool                    `json:"stream,omitempty"`
	Stop           []string                `json:"stop,omitempty"`
	Tools          []providers.Tool        `json:"tools,omitempty"`
	ToolChoice     any                     `json:"tool_choice,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"`
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
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	}

	if req.Stream {
		h.handleStream(w, r, userID, requestID, req.ConversationID, provReq, start)
		return
	}

	tier := routing.TierFromModel(req.Model)

	// Non-streaming path
	scored, err := h.selector.Pool().SelectComboForSession(r.Context(), userID, req.ConversationID, tier)
	if err != nil || len(scored) == 0 {
		metrics.RequestsTotal.WithLabelValues("none", "none", "error").Inc()
		h.recorder.RecordAsync(metadata.Record{
			UserID:        userID,
			Zer0RequestID: requestID,
			Provider:      "none",
			Model:         req.Model,
			Pool:          tier.String(),
			LatencyMs:     time.Since(start).Milliseconds(),
			Status:        "no_providers",
		})
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "no_providers", "No providers available")
		return
	}

	var (
		resp       *providers.ChatResponse
		selCombo   *routing.SelectedCombo
		retryCount int
		lastErr    error
	)

	const attemptTimeout = 15 * time.Second

	// Collect per-attempt error details for diagnostics
	var attemptErrors []string

	// Sequential attempts through round-robin ordered candidates.
	attemptOrder := 0
	for _, sc := range scored {
		if retryCount >= 6 {
			break
		}
		attemptStart := time.Now()
		k, err := h.keyRepo.GetByID(r.Context(), sc.ProviderKeyID, userID)
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s/%s: key_lookup_failed", sc.Provider, sc.Model))
			h.recorder.RecordAttemptAsync(metadata.Attempt{
				Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
				ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
				Status: "error", ErrorClass: "internal", ErrorMessage: "key_lookup_failed",
				LatencyMs: time.Since(attemptStart).Milliseconds(),
			})
			attemptOrder++
			continue
		}
		decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s/%s: key_decrypt_failed", sc.Provider, sc.Model))
			attemptOrder++
			continue
		}
		p, err := providers.Get(sc.Provider)
		if err != nil {
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s/%s: unknown_provider", sc.Provider, sc.Model))
			attemptOrder++
			continue
		}
		comboReq := *provReq
		comboReq.Model = sc.Model
		attemptCtx, cancelAttempt := context.WithTimeout(r.Context(), attemptTimeout)
		resp, err = p.Complete(attemptCtx, &comboReq, decrypted)
		cancelAttempt()
		attemptLatency := time.Since(attemptStart).Milliseconds()
		if err == nil {
			selCombo = &routing.SelectedCombo{Combo: sc.Combo, DecryptedKey: decrypted}
			h.recorder.RecordAttemptAsync(metadata.Attempt{
				Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
				ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
				Status: "success", LatencyMs: attemptLatency,
			})
			if resp.RateLimit != nil {
				h.rlObserver.Observe(r.Context(), sc.Provider, sc.Model, resp.RateLimit)
			}
			break
		}
		lastErr = err
		retryCount++

		// Record the attempt
		var errClass, errMsg string
		var httpStatus int
		if pe, ok := err.(*providers.ProviderError); ok {
			errClass = string(pe.Class)
			errMsg = pe.Message
			httpStatus = pe.HTTP
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s/%s: %s (HTTP %d)", sc.Provider, sc.Model, pe.Class, pe.HTTP))
		} else {
			errClass = "unknown"
			errMsg = err.Error()
			attemptErrors = append(attemptErrors, fmt.Sprintf("%s/%s: %s", sc.Provider, sc.Model, err.Error()))
		}
		h.recorder.RecordAttemptAsync(metadata.Attempt{
			Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
			ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
			Status: "error", ErrorClass: errClass, ErrorMessage: errMsg, HTTPStatus: httpStatus,
			LatencyMs: attemptLatency,
		})
		attemptOrder++

		slog.Warn("inference: provider failed", "provider", sc.Provider, "model", sc.Model, "error", err.Error())

		// Auto-remediation based on error class
		if pe, ok := err.(*providers.ProviderError); ok {
			metrics.ProviderErrors.WithLabelValues(sc.Provider, sc.Model, string(pe.Class)).Inc()
			switch pe.Class {
			case providers.ErrorClassAuthError:
				go h.remediator.HandleAuthError(context.Background(), userID, sc.Provider, sc.ProviderKeyID)
			case providers.ErrorClassUpstream:
				if pe.HTTP == 404 {
					go h.remediator.HandleNotFound(context.Background(), sc.Provider, sc.Model)
				} else {
					h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
				}
			case providers.ErrorClassRateLimit:
				h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
			case providers.ErrorClassTimeout, providers.ErrorClassUnavailable:
				h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
			}
		} else {
			h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
		}
	}

	recordFailure := func(lastErr error) {
		latMs := time.Since(start).Milliseconds()
		errStatus := "all_failed"
		errClass := ""
		if lastErr != nil {
			if pe, ok := lastErr.(*providers.ProviderError); ok {
				errStatus = "all_failed"
				errClass = string(pe.Class)
			}
		}
		errMsg := strings.Join(attemptErrors, "; ")
		if len(errMsg) > 1000 {
			errMsg = errMsg[:1000]
		}
		if len(attemptErrors) == 0 {
			errMsg = "no candidates attempted"
		}
		metrics.RequestsTotal.WithLabelValues("none", "none", "error").Inc()
		h.recorder.RecordAsync(metadata.Record{
			UserID:        userID,
			Zer0RequestID: requestID,
			Provider:      "none",
			Model:         req.Model,
			Pool:          tier.String(),
			LatencyMs:     latMs,
			Status:        errStatus,
			RetryCount:    retryCount,
			ErrorClass:    errClass,
			ErrorMessage:  errMsg,
		})
	}

	latencyMs := time.Since(start).Milliseconds()

	if selCombo == nil {
		slog.Error("inference: all providers failed", "user_id", userID, "last_error", fmt.Sprint(lastErr))
		recordFailure(lastErr)
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
	h.selector.Pool().IncrementUsage(r.Context(), userID, tier, selCombo.Provider, selCombo.Model, selCombo.ProviderKeyID)

	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	assistantMsg := map[string]interface{}{"role": "assistant", "content": resp.Content}
	if len(resp.ToolCalls) > 0 {
		assistantMsg["tool_calls"] = resp.ToolCalls
		finishReason = "tool_calls"
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":      resp.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   selCombo.Model,
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       assistantMsg,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]interface{}{
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		},
	})
}

func (h *InferenceHandler) handleStream(w http.ResponseWriter, r *http.Request, userID, requestID, conversationID string, req *providers.ChatRequest, start time.Time) {
	tier := routing.TierFromModel(req.Model)
	scored, err := h.selector.Pool().SelectComboForSession(r.Context(), userID, conversationID, tier)
	if err != nil || len(scored) == 0 {
		metrics.RequestsTotal.WithLabelValues("none", "none", "error").Inc()
		h.recorder.RecordAsync(metadata.Record{
			UserID:        userID,
			Zer0RequestID: requestID,
			Provider:      "none",
			Model:         req.Model,
			Pool:          tier.String(),
			LatencyMs:     time.Since(start).Milliseconds(),
			Status:        "no_providers",
		})
		writeError(w, http.StatusServiceUnavailable, "gateway_error", "no_providers", "No providers available")
		return
	}

	const streamFirstByteTimeout = 10 * time.Second

	var (
		ch       <-chan providers.ChatChunk
		selCombo *routing.SelectedCombo
		retryCount int
	)

	var streamAttemptErrors []string

	triedCombos := map[string]bool{}
	allExhaustedS := true
	for _, sc := range scored {
		if !h.rlObserver.IsKeyExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model) {
			allExhaustedS = false
			break
		}
	}
	attemptOrder := 0
	for _, sc := range scored {
		comboID := sc.Provider + ":" + sc.Model + ":" + sc.ProviderKeyID
		if triedCombos[comboID] || retryCount >= 6 {
			continue
		}
		if !allExhaustedS && h.rlObserver.IsKeyExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model) {
			continue
		}
		triedCombos[comboID] = true
		attemptStart := time.Now()

		k, err := h.keyRepo.GetByID(r.Context(), sc.ProviderKeyID, userID)
		if err != nil {
			streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: key_lookup_failed", sc.Provider, sc.Model))
			h.recorder.RecordAttemptAsync(metadata.Attempt{
				Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
				ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
				Status: "error", ErrorClass: "internal", ErrorMessage: "key_lookup_failed",
				LatencyMs: time.Since(attemptStart).Milliseconds(),
			})
			attemptOrder++
			continue
		}
		decrypted, err := keys.Decrypt(k.Ciphertext, k.Nonce)
		if err != nil {
			streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: key_decrypt_failed", sc.Provider, sc.Model))
			attemptOrder++
			continue
		}
		p, err := providers.Get(sc.Provider)
		if err != nil {
			streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: unknown_provider", sc.Provider, sc.Model))
			attemptOrder++
			continue
		}
		comboReq := *req
		comboReq.Model = sc.Model

		attemptCtx, cancelAttempt := context.WithCancel(r.Context())
		candidateCh, err := p.Stream(attemptCtx, &comboReq, decrypted)
		if err != nil {
			cancelAttempt()
			retryCount++
			attemptLatency := time.Since(attemptStart).Milliseconds()

			var errClass, errMsg string
			var httpStatus int
			if pe, ok := err.(*providers.ProviderError); ok {
				errClass = string(pe.Class)
				errMsg = pe.Message
				httpStatus = pe.HTTP
				streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: %s (HTTP %d)", sc.Provider, sc.Model, pe.Class, pe.HTTP))
			} else {
				errClass = "unknown"
				errMsg = err.Error()
				streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: %s", sc.Provider, sc.Model, err.Error()))
			}
			h.recorder.RecordAttemptAsync(metadata.Attempt{
				Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
				ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
				Status: "error", ErrorClass: errClass, ErrorMessage: errMsg, HTTPStatus: httpStatus,
				LatencyMs: attemptLatency,
			})
			attemptOrder++

			slog.Warn("inference: stream provider failed", "provider", sc.Provider, "model", sc.Model, "error", err.Error())

			if pe, ok := err.(*providers.ProviderError); ok {
				metrics.ProviderErrors.WithLabelValues(sc.Provider, sc.Model, string(pe.Class)).Inc()
				switch pe.Class {
				case providers.ErrorClassAuthError:
					go h.remediator.HandleAuthError(context.Background(), userID, sc.Provider, sc.ProviderKeyID)
				case providers.ErrorClassUpstream:
					if pe.HTTP == 404 {
						go h.remediator.HandleNotFound(context.Background(), sc.Provider, sc.Model)
					} else {
						h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
					}
				case providers.ErrorClassRateLimit, providers.ErrorClassTimeout, providers.ErrorClassUnavailable:
					h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
				}
			} else {
				h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
			}
			continue
		}

		firstByteTimer := time.NewTimer(streamFirstByteTimeout)
		var firstChunk *providers.ChatChunk
		select {
		case fc, ok := <-candidateCh:
			firstByteTimer.Stop()
			if !ok {
				cancelAttempt()
				retryCount++
				streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: stream_closed_immediately", sc.Provider, sc.Model))
				h.recorder.RecordAttemptAsync(metadata.Attempt{
					Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
					ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
					Status: "error", ErrorClass: "stream_closed", ErrorMessage: "stream_closed_immediately",
					LatencyMs: time.Since(attemptStart).Milliseconds(),
				})
				attemptOrder++
				h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
				continue
			}
			firstChunk = &fc
		case <-firstByteTimer.C:
			cancelAttempt()
			retryCount++
			streamAttemptErrors = append(streamAttemptErrors, fmt.Sprintf("%s/%s: first_byte_timeout_%.0fs", sc.Provider, sc.Model, streamFirstByteTimeout.Seconds()))
			h.recorder.RecordAttemptAsync(metadata.Attempt{
				Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
				ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
				Status: "error", ErrorClass: "timeout", ErrorMessage: fmt.Sprintf("first_byte_timeout_%.0fs", streamFirstByteTimeout.Seconds()),
				LatencyMs: time.Since(attemptStart).Milliseconds(),
			})
			attemptOrder++
			slog.Warn("inference: stream first-byte timeout", "provider", sc.Provider, "model", sc.Model,
				"timeout_s", streamFirstByteTimeout.Seconds())
			h.rlObserver.MarkExhausted(r.Context(), sc.ProviderKeyID, sc.Provider, sc.Model)
			continue
		case <-r.Context().Done():
			firstByteTimer.Stop()
			cancelAttempt()
			return
		}

		h.recorder.RecordAttemptAsync(metadata.Attempt{
			Zer0RequestID: requestID, UserID: userID, Provider: sc.Provider, Model: sc.Model,
			ProviderKeyID: sc.ProviderKeyID, AttemptOrder: attemptOrder,
			Status: "success", LatencyMs: time.Since(attemptStart).Milliseconds(),
		})

		mergedCh := make(chan providers.ChatChunk, 64)
		go func() {
			defer close(mergedCh)
			defer cancelAttempt()
			mergedCh <- *firstChunk
			for chunk := range candidateCh {
				mergedCh <- chunk
			}
		}()
		ch = mergedCh
		selCombo = &routing.SelectedCombo{Combo: sc.Combo, DecryptedKey: decrypted}
		break
	}

	if selCombo == nil {
		errMsg := strings.Join(streamAttemptErrors, "; ")
		if len(errMsg) > 1000 {
			errMsg = errMsg[:1000]
		}
		if len(streamAttemptErrors) == 0 {
			errMsg = "no candidates attempted"
		}
		metrics.RequestsTotal.WithLabelValues("none", "none", "error").Inc()
		h.recorder.RecordAsync(metadata.Record{
			UserID:        userID,
			Zer0RequestID: requestID,
			Provider:      "none",
			Model:         req.Model,
			Pool:          tier.String(),
			LatencyMs:     time.Since(start).Milliseconds(),
			Status:        "all_failed",
			RetryCount:    retryCount,
			ErrorMessage:  errMsg,
		})
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
	h.selector.Pool().IncrementUsage(r.Context(), userID, tier, selCombo.Provider, selCombo.Model, selCombo.ProviderKeyID)
}


func countTokensApprox(s string) int {
	// Rough approximation: 1 token ≈ 4 chars
	return (len(strings.TrimSpace(s)) + 3) / 4
}

func newUUID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// ListModels handles GET /v1/models — returns all Zer0 tier models.
func ListModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "zer0-0.1-pro",
				"object":   "model",
				"created":  1700000000,
				"owned_by": "zer0-router",
			},
			{
				"id":       "zer0-0.1-flash",
				"object":   "model",
				"created":  1700000000,
				"owned_by": "zer0-router",
			},
			{
				"id":       "zer0-0.1-flash-lite",
				"object":   "model",
				"created":  1700000000,
				"owned_by": "zer0-router",
			},
		},
	})
}
