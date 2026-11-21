package catalog

import "testing"

func TestClassifyKind(t *testing.T) {
	gen := []string{"generateContent", "streamGenerateContent"}
	cases := []struct {
		provider, id string
		methods      []string
		want         string
	}{
		// spec fixture rows
		{"google", "models/gemini-2.5-flash", nil, "chat"},
		{"google", "models/gemini-2.5-flash-image", nil, "image"},
		{"google", "models/gemini-2.5-flash-preview-tts", nil, "audio"},
		{"google", "models/lyria-realtime-exp", nil, "audio"},
		{"google", "models/gemini-2.5-computer-use-preview", nil, "other"},
		{"google", "models/gemini-robotics-er-1.5-preview", nil, "other"},
		{"google", "models/text-embedding-004", nil, "embedding"},
		{"groq", "whisper-large-v3", nil, "audio"},
		{"groq", "distil-whisper-large-v3-en", nil, "audio"},
		{"groq", "llama-guard-4-12b", nil, "other"},
		{"groq", "playai-tts", nil, "audio"},
		{"openrouter", "openai/gpt-image-1", nil, "image"},
		{"meshapi", "openai/dall-e-3", nil, "image"},
		{"meshapi", "mistral/mistral-7b-instruct-v0", nil, "chat"},
		{"meshapi", "meta-llama/llama-3.3-70b-instruct", nil, "chat"},
		{"meshapi", "anthropic/claude-sonnet-5.5", nil, "chat"},
		{"anthropic", "claude-sonnet-5-5", nil, "chat"},
		// live-observed Gemini ids: non-chat
		{"google", "gemini-2.5-flash-image", gen, "image"},
		{"google", "gemini-3-pro-image-preview", gen, "image"},
		{"google", "gemini-2.5-flash-preview-tts", gen, "audio"},
		{"google", "gemini-3.1-flash-tts-preview", gen, "audio"},
		{"google", "lyria-3-pro-preview", gen, "audio"},
		{"google", "gemini-2.5-computer-use-preview-10", gen, "other"},
		{"google", "gemini-robotics-er-2-preview", gen, "other"},
		{"google", "gemini-3.5-transcribe", gen, "audio"},
		{"google", "gemini-2.0-flash-live-001", gen, "audio"},
		{"google", "veo-3.0-generate-001", nil, "image"},
		// live-observed Gemini ids: chat
		{"google", "gemini-2.5-flash", gen, "chat"},
		{"google", "gemini-2.5-pro", gen, "chat"},
		{"google", "gemini-3.1-pro-preview", gen, "chat"},
		{"google", "gemini-3.1-pro-preview-customtools", gen, "chat"},
		{"google", "gemini-3.1-flash-lite", gen, "chat"},
		{"google", "gemma-4-31b", gen, "chat"},
		{"google", "gemini-flash", gen, "chat"},
		{"google", "gemini-pro", gen, "chat"},
		// No name rule matches: chat (documented decision).
		{"google", "gemini-omni-flash-preview", gen, "chat"},
		// rule 1: Gemini methods
		{"google", "gemini-x", []string{"countTokens"}, "other"},
		{"google", "gemini-x", []string{}, "other"},
		{"google", "models/embedding-001", []string{"embedContent"}, "embedding"},
		{"google", "aqa", []string{"generateAnswer"}, "other"},
		{"google", "gemini-x", []string{"streamGenerateContent"}, "chat"},
		{"groq", "llama-3.3-70b-versatile", []string{"countTokens"}, "chat"}, // methods apply to Gemini only
		// other patterns
		{"openrouter", "x/some-moderation-model", nil, "other"},
		{"openrouter", "cohere/rerank-v3", nil, "other"},
		{"openrouter", "openai/gpt-4o-audio-preview", nil, "audio"},
		{"openrouter", "x/guardian-chat", nil, "chat"}, // override
	}
	for _, c := range cases {
		if got := ClassifyKind(c.provider, c.id, c.methods); got != c.want {
			t.Errorf("ClassifyKind(%q,%q,%v)=%q want %q", c.provider, c.id, c.methods, got, c.want)
		}
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []string{"chat", "embedding", "image", "audio", "other"} {
		if !ValidKind(k) {
			t.Errorf("%s should be valid", k)
		}
	}
	if ValidKind("") || ValidKind("video") {
		t.Error("invalid kinds accepted")
	}
}
