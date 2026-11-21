package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Mesh's real GET /v1/models returns a bare JSON array (not OpenAI's {"data":[...]}).
// This test caught the production bug where every Mesh key test failed as "transient".
func TestMeshAPIListModelsBareArray(t *testing.T) {
	body, err := os.ReadFile("testdata/meshapi/models_array.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotVersion, gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion, gotAuth, gotPath = r.Header.Get("X-Mesh-Version"), r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	t.Setenv("MESHAPI_BASE_URL", srv.URL+"/v1")

	p, err := Get("meshapi")
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.ListModels(context.Background(), "rsk_test")
	if err != nil {
		t.Fatalf("ListModels on a bare-array body must succeed: %v", err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer rsk_test" {
		t.Fatalf("request: path=%q auth=%q", gotPath, gotAuth)
	}
	if gotVersion != "2026-08" {
		t.Fatalf("X-Mesh-Version = %q, want pinned 2026-08", gotVersion)
	}
	kinds := map[string]string{}
	for _, m := range models {
		kinds[m.ID], _ = m.Extra["kind"].(string)
	}
	if len(models) != 7 {
		t.Fatalf("want 7 models (empty id skipped), got %d: %v", len(models), kinds)
	}
	want := map[string]string{
		"mistral/mistral-7b-instruct-v0": "chat",
		"openai/gpt-5-pro":               "other", // text but not chat-completions capable
		"openai/text-embedding-3-large":  "embedding",
		"openai/dall-e-3":                "image",
		"google/veo-3":                   "image",
		"openai/whisper-1":               "audio",
		"mesh/judge-1":                   "other",
	}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("kind[%s] = %q, want %q", id, kinds[id], k)
		}
	}
	for _, m := range models {
		if m.ID == "openai/dall-e-3" && m.ContextWindow != 4000 {
			t.Errorf("context_window fallback: got %d", m.ContextWindow)
		}
		if m.ID == "mistral/mistral-7b-instruct-v0" && (m.ContextWindow != 32768 || m.DisplayName != "Mistral 7B Instruct") {
			t.Errorf("mistral fields: %+v", m)
		}
	}
}

// The pin header must go out on chat requests too, not only listings.
func TestMeshAPIChatSendsVersionPin(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("X-Mesh-Version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	t.Setenv("MESHAPI_BASE_URL", srv.URL+"/v1")
	p, _ := Get("meshapi")
	if _, err := p.Complete(context.Background(), &ChatRequest{Model: "mistral/mistral-7b-instruct-v0", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}, "rsk_test"); err != nil {
		t.Fatal(err)
	}
	if gotVersion != "2026-08" {
		t.Fatalf("X-Mesh-Version on chat = %q", gotVersion)
	}
}

func TestMeshAPIListModelsUnparseable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`"not a list"`))
	}))
	defer srv.Close()
	t.Setenv("MESHAPI_BASE_URL", srv.URL+"/v1")
	p, _ := Get("meshapi")
	_, err := p.ListModels(context.Background(), "rsk_test")
	pe, ok := err.(*ProviderError)
	if !ok || pe.Class != ErrorClassTransient || pe.Code != "invalid_response" {
		t.Fatalf("want transient invalid_response, got %#v", err)
	}
}
