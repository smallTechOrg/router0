package mockprovider

import (
	"net/http"
	"strings"
)

// mockBalanceLimit is the per-key limit OpenRouter's GET /key reports.
const mockBalanceLimit = 100.0

// SetBalance sets the remaining USD that GET /key reports (OpenRouter flavour).
func (m *Mock) SetBalance(usd float64) {
	m.mu.Lock()
	m.balance = usd
	m.mu.Unlock()
}

// BalanceCalls counts GET /key requests for a key ("" = any key).
func (m *Mock) BalanceCalls(key string) int { return m.count(key, "/key") }

func (m *Mock) balanceAuth(w http.ResponseWriter, r *http.Request, path string) bool {
	key := bearerKey(r)
	m.record(Call{Key: key, Path: path})
	key = m.behaviourKey(key)
	if !strings.HasPrefix(key, "mock-") || strings.HasPrefix(key, "mock-401") {
		m.writeError(w, http.StatusUnauthorized, "invalid_api_key", "authentication_error", 0, "")
		return false
	}
	return true
}

// openRouterKey answers GET /api/v1/key like OpenRouter: a key with a $100
// limit whose remaining amount is the mock balance.
func (m *Mock) openRouterKey(w http.ResponseWriter, r *http.Request) {
	if !m.balanceAuth(w, r, "/key") {
		return
	}
	m.mu.Lock()
	rem := m.balance
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"label": "mock", "limit": mockBalanceLimit, "usage": mockBalanceLimit - rem, "limit_remaining": rem,
		"is_free_tier": false,
	}})
}

// openRouterCredits answers GET /api/v1/credits (account totals).
func (m *Mock) openRouterCredits(w http.ResponseWriter, r *http.Request) {
	if !m.balanceAuth(w, r, "/credits") {
		return
	}
	m.mu.Lock()
	rem := m.balance
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"total_credits": mockBalanceLimit, "total_usage": mockBalanceLimit - rem,
	}})
}
