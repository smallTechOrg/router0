package mockprovider

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Gemini flavour: Google's OpenAI-compatible chat at
// /v1beta/openai/chat/completions (Bearer key; quirks: tool-call ids missing,
// finish_reason "stop" with tool calls, whole-arguments stream chunks with a
// repeated index 0, array-wrapped error bodies) and the native listing
// GET /v1beta/models?key=... with supportedGenerationMethods, deliberately
// containing non-chat models.

func geminiStatusName(status int) string {
	switch status {
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 500:
		return "INTERNAL"
	case 503:
		return "UNAVAILABLE"
	case 404:
		return "NOT_FOUND"
	case 401, 403:
		return "PERMISSION_DENIED"
	}
	return "INVALID_ARGUMENT"
}

// geminiError writes Google's error shape: an array-wrapped body for most
// errors, a bare object for the billing precondition; a bad key is HTTP 400
// API_KEY_INVALID (as the real API answers).
func (m *Mock) geminiError(w http.ResponseWriter, status int, msg string) {
	switch status {
	case http.StatusUnauthorized:
		writeJSON(w, 400, []any{map[string]any{"error": map[string]any{"code": 400,
			"message": msg + " API key not valid. Please pass a valid API key.", "status": "INVALID_ARGUMENT",
			"details": []any{map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "API_KEY_INVALID", "domain": "googleapis.com"}}}}})
	case http.StatusPaymentRequired:
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": 400,
			"message": msg + " Please enable billing on your project.", "status": "FAILED_PRECONDITION"}})
	default:
		writeJSON(w, status, []any{map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatusName(status)}}})
	}
}

func geminiMethods(id string) []string {
	id = strings.ToLower(id)
	if strings.Contains(id, "embed") {
		return []string{"embedContent"}
	}
	return []string{"generateContent", "countTokens", "streamGenerateContent"}
}

func (m *Mock) geminiListModels(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		key = r.Header.Get("x-goog-api-key")
	}
	if key == "" {
		key = bearerKey(r)
	}
	m.record(Call{Key: key, Path: "/models"})
	k := m.behaviourKey(key)
	if !strings.HasPrefix(k, "mock-") || strings.HasPrefix(k, "mock-401") {
		m.geminiError(w, http.StatusUnauthorized, SentinelBody)
		return
	}
	m.mu.Lock()
	ids := append([]string(nil), m.models...)
	m.mu.Unlock()
	start := 0
	if tok := r.URL.Query().Get("pageToken"); strings.HasPrefix(tok, "p") {
		start, _ = strconv.Atoi(tok[1:])
	}
	size := m.pageSize
	if n, err := strconv.Atoi(r.URL.Query().Get("pageSize")); err == nil && n > 0 && n < size {
		size = n
	}
	end := start + size
	if end > len(ids) {
		end = len(ids)
	}
	if start > end {
		start = end
	}
	items := []map[string]any{}
	for _, id := range ids[start:end] {
		full := id
		if !strings.HasPrefix(full, "models/") {
			full = "models/" + id
		}
		items = append(items, map[string]any{"name": full, "displayName": strings.TrimPrefix(full, "models/"),
			"inputTokenLimit": 1048576, "outputTokenLimit": 65536, "supportedGenerationMethods": geminiMethods(full)})
	}
	out := map[string]any{"models": items}
	if end < len(ids) {
		out["nextPageToken"] = fmt.Sprintf("p%d", end)
	}
	writeJSON(w, http.StatusOK, out)
}
