package handlers

import (
	"encoding/json"
	"net/http"
)

// writeError writes the dashboard/admin error shape {"error":{message,type,code}}.
func writeError(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOpenAIError writes the OpenAI-shaped error used on /v1/* so SDKs can parse
// it. param may be empty (serialised as null). router0 is the optional extension
// object (request id + attempt trail); nil omits it.
func writeOpenAIError(w http.ResponseWriter, status int, errType, code, message, param string, router0 map[string]any) {
	body := map[string]any{
		"message": message,
		"type":    errType,
		"param":   nil,
		"code":    code,
	}
	if param != "" {
		body["param"] = param
	}
	if router0 != nil {
		body["router0"] = router0
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}
