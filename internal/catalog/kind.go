package catalog

import "strings"

// Model kinds stored in org_model_catalog.kind.
const (
	KindChat      = "chat"
	KindEmbedding = "embedding"
	KindImage     = "image"
	KindAudio     = "audio"
	KindOther     = "other"
)

// ValidKind reports whether k is one of the five stored kinds.
func ValidKind(k string) bool {
	switch k {
	case KindChat, KindEmbedding, KindImage, KindAudio, KindOther:
		return true
	}
	return false
}

// ClassifyKind decides a listed model's kind (model-catalog.md, Model kinds).
// provider is the router0 provider id; id is the provider's raw model id; methods
// is Gemini's supportedGenerationMethods (nil when the listing has none).
// Rules are ordered; first match wins. Deterministic.
func ClassifyKind(provider, id string, methods []string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	s = strings.TrimPrefix(s, "models/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if v, ok := kindOverride(provider, s); ok {
		return v
	}

	// 1. Gemini only: methods present without a generate method.
	if (provider == "google" || provider == "gemini") && methods != nil {
		hasGen, hasEmbed := false, false
		for _, m := range methods {
			switch m {
			case "generateContent", "streamGenerateContent":
				hasGen = true
			case "embedContent":
				hasEmbed = true
			}
		}
		if !hasGen {
			if hasEmbed {
				return KindEmbedding
			}
			return KindOther
		}
	}
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(s, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("embed"):
		return KindEmbedding
	case has("image", "imagen", "dall-e", "veo", "video"):
		return KindImage
	case has("tts", "transcribe", "whisper", "lyria", "music", "speech", "audio", "-live"):
		return KindAudio
	case has("computer-use", "robotics", "guard", "moderation", "rerank"):
		return KindOther
	}
	return KindChat
}
