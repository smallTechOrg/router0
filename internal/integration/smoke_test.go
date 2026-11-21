//go:build integration && smoke

package integration

import (
	"os"
	"testing"
	"time"
)

// Real-provider smoke tests (not part of the gate). Each skips unless its
// TEST_*_KEY env var is set. They need no mock providers.

func smokeChat(t *testing.T, tn *tenant, tok, model string) {
	t.Helper()
	r := tn.chat(tok, chatOpts{model: model, prompt: "Reply with the single word: ok"})
	assertStatus(t, r, 200)
	if str(dig(r.json(), "choices", 0, "message", "content")) == "" {
		t.Fatalf("empty completion: %s", r)
	}
	s := tn.chat(tok, chatOpts{model: model, prompt: "Count: 1, 2, 3", stream: true})
	assertStatus(t, s, 200)
	if sse := parseSSE(s.body); sse.contentChunks() == 0 || !sse.done {
		t.Fatalf("stream: %s", s.body)
	}
}

func TestSmokeGroq(t *testing.T) {
	key := os.Getenv("TEST_GROQ_KEY")
	if key == "" {
		t.Skip("TEST_GROQ_KEY not set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-groq")
	tn.addKey("groq", key, "groq-real")
	tn.waitCatalogTimeout("llama-3.3-70b", 30*time.Second, "groq")
	tok, _ := tn.token("smoke")
	smokeChat(t, tn, tok, "llama-3.3-70b")
}

func TestSmokeOpenRouter(t *testing.T) {
	key := os.Getenv("TEST_OPENROUTER_KEY")
	if key == "" {
		t.Skip("TEST_OPENROUTER_KEY not set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-or")
	tn.addKey("openrouter", key, "or-real")
	model := "llama-3.3-70b:free"
	canonical := model
	tn.waitCatalogTimeout(canonical, 30*time.Second, "openrouter")
	tok, _ := tn.token("smoke")
	smokeChat(t, tn, tok, model)
}

func TestSmokeRealFailover(t *testing.T) {
	groqKey, orKey := os.Getenv("TEST_GROQ_KEY"), os.Getenv("TEST_OPENROUTER_KEY")
	if groqKey == "" || orKey == "" {
		t.Skip("TEST_GROQ_KEY and TEST_OPENROUTER_KEY must both be set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-failover")
	realID := tn.addKey("groq", groqKey, "groq-real")
	tn.addKey("openrouter", orKey, "or-real")
	tn.waitCatalogTimeout("llama-3.3-70b", 30*time.Second, "groq")
	badID := tn.addKey("groq", "gsk_invalid_router0_smoke", "groq-invalid")
	// make the invalid key first in order (position 0)
	r := tn.api("PUT", "/api/routing/priority", J{"providers": []string{"groq", "openrouter"}, "key_order": J{"groq": []string{badID, realID}}})
	assertStatus(t, r, 200)
	tok, _ := tn.token("smoke")

	resp := tn.chat(tok, chatOpts{prompt: "Reply with the single word: ok"})
	assertStatus(t, resp, 200)
	row := tn.waitLog(resp.header.Get("X-Router0-Request-Id"), 2)
	if str(dig(row, "trail", 0, "class")) != "auth" || str(dig(row, "trail", 1, "outcome")) != "success" {
		t.Fatalf("expected auth then success, got trail %v", row["trail"])
	}
}
