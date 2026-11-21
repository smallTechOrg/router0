//go:build integration && smoke

package integration

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zer0-router/zer0/internal/testutil/agentloop"
)

// Phase 2 real-provider smoke tests (not part of the gate). Each skips unless
// its TEST_*_KEY env var is set. No keys are stored in the repo.

// smokeModels waits until /v1/models (service token) lists at least one model
// accepted by pick and returns all ids plus the picked one.
func smokeModels(t *testing.T, e *env, tok string, pick func(id string) bool) (all []string, picked string) {
	t.Helper()
	waitFor(t, 60*time.Second, "chat models discovered", func() bool {
		all, picked = nil, ""
		r := e.do("GET", "/v1/models", tok, nil, nil)
		if r.status != 200 {
			return false
		}
		for _, d := range list(r.json()["data"]) {
			id := str(dig(d, "id"))
			all = append(all, id)
			if picked == "" && pick(id) {
				picked = id
			}
		}
		return picked != ""
	})
	return all, picked
}

func smokeLoop(t *testing.T, e *env, tok, model string) {
	t.Helper()
	for _, stream := range []bool{false, true} {
		r := agentloop.Run(t, agentloop.Config{BaseURL: e.ts.URL, Token: tok, Model: model, Stream: stream})
		t.Logf("model=%s stream=%v rounds=%d final=%q", model, stream, len(r.Rounds), r.FinalText)
	}
}

func TestSmokeGeminiTools(t *testing.T) {
	key := os.Getenv("TEST_GEMINI_KEY")
	if key == "" {
		t.Skip("TEST_GEMINI_KEY not set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-gemini")
	tn.addKey("google", key, "gemini-real")
	tok, _ := tn.token("smoke")
	all, _ := smokeModels(t, e, tok, func(id string) bool { return id == "gemini-2.5-flash" })
	for _, id := range all {
		for _, bad := range []string{"image", "tts", "lyria", "music", "computer-use", "robotics", "embed", "imagen", "veo"} {
			if strings.Contains(id, bad) {
				t.Fatalf("/v1/models lists non-chat model %q", id)
			}
		}
	}
	smokeLoop(t, e, tok, "gemini-2.5-flash")
}

func TestSmokeAnthropicTools(t *testing.T) {
	key := os.Getenv("TEST_ANTHROPIC_KEY")
	if key == "" {
		t.Skip("TEST_ANTHROPIC_KEY not set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-anthropic")
	tn.addKey("anthropic", key, "anthropic-real")
	tok, _ := tn.token("smoke")
	_, model := smokeModels(t, e, tok, func(id string) bool { return strings.HasPrefix(id, "claude-") })
	smokeLoop(t, e, tok, model)
	smokeChat(t, tn, tok, model)
}

func TestSmokeMeshAPI(t *testing.T) {
	key := os.Getenv("TEST_MESHAPI_KEY")
	if key == "" {
		t.Skip("TEST_MESHAPI_KEY not set")
	}
	e := newRealEnv(t)
	tn := e.newTenant("smoke-mesh")
	tn.addKey("meshapi", key, "mesh-real") // rsk_...
	tok, _ := tn.token("smoke")
	_, model := smokeModels(t, e, tok, func(id string) bool { return id == "mistral-7b" })
	smokeChat(t, tn, tok, model)
}
