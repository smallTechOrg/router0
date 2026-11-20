package catalog

import "testing"

func TestCanonicalize(t *testing.T) {
	cases := []struct{ provider, id, want string }{
		{"groq", "llama-3.3-70b-versatile", "llama-3.3-70b"},
		{"openrouter", "meta-llama/llama-3.3-70b-instruct", "llama-3.3-70b"},
		{"openrouter", "meta-llama/llama-3.3-70b-instruct:free", "llama-3.3-70b:free"},
		{"groq", "llama-3.1-8b-instant", "llama-3.1-8b"},
		{"openrouter", "meta-llama/llama-3.1-8b-instruct", "llama-3.1-8b"},
		{"groq", "mixtral-8x7b-32768", "mixtral-8x7b"},
		{"groq", "gemma2-9b-it", "gemma2-9b"},
		{"anthropic", "claude-sonnet-5-5-20261001", "claude-sonnet-5-5"},
		{"openrouter", "anthropic/claude-sonnet-5.5", "claude-sonnet-5-5"},
		{"google", "models/gemini-2.5-flash", "gemini-2.5-flash"},
		{"google", "gemini-1.5-flash-001", "gemini-1.5-flash"},
		{"", "Llama-3.3-70B-Versatile", "llama-3.3-70b"},
		// extra rule coverage
		{"", "  GPT-4o-2024-08-06 ", "gpt-4o"},
		{"together", "meta-llama-3.1-8b-instruct", "llama-3.1-8b"},
		{"openrouter", "anthropic/claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"", "claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"", "my-custom-model", "my-custom-model"},
	}
	for _, c := range cases {
		if got := Canonicalize(c.provider, c.id); got != c.want {
			t.Errorf("Canonicalize(%q,%q)=%q want %q", c.provider, c.id, got, c.want)
		}
	}
}

func TestCanonicalizeCallerMatchesListing(t *testing.T) {
	a := Canonicalize("openrouter", "meta-llama/llama-3.3-70b-instruct")
	b := Canonicalize("", "meta-llama/llama-3.3-70b-instruct")
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}
