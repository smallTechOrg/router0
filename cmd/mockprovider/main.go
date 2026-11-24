// Command mockprovider serves scripted mock providers for keyless manual
// testing:
//
//	:9901 groq        (OpenAI wire)      GROQ_BASE_URL=http://127.0.0.1:9901/v1
//	:9902 openrouter  (OpenAI wire)      OPENROUTER_BASE_URL=http://127.0.0.1:9902/v1
//	:9903 anthropic   (Messages API)     ANTHROPIC_BASE_URL=http://127.0.0.1:9903/v1
//	:9904 gemini      (Google OpenAI-compat + native listing)  GOOGLE_BASE_URL=http://127.0.0.1:9904
//	:9905 meshapi     (OpenAI wire, rsk_ keys)  MESHAPI_BASE_URL=http://127.0.0.1:9905/v1
//
// Pick behaviour with the provider key you add (mock-ok-*, mock-429-15-*,
// mock-402-*, mock-401-*, mock-500-*, mock-midstream-*; Anthropic also
// mock-credits-*, mock-529-*; Mesh keys may be prefixed, e.g. rsk_mock-ok-a).
// See internal/testutil/mockprovider for the full list, including the scripted
// tool-calling scenarios. POST a JSON array of ids to /_models on any port to
// change its model list at runtime. The OpenRouter mock also serves the
// balance API (GET /v1/key, /v1/credits); POST a JSON number to /_balance on
// :9902 to set the remaining USD (e.g. `curl -X POST localhost:9902/_balance -d 0`).
package main

import (
	"log"
	"net/http"

	"github.com/zer0-router/zer0/internal/testutil/mockprovider"
)

func main() {
	type srv struct {
		addr string
		f    mockprovider.Flavour
		env  string
	}
	all := []srv{
		{"127.0.0.1:9901", mockprovider.Groq, "GROQ_BASE_URL=http://127.0.0.1:9901/v1"},
		{"127.0.0.1:9902", mockprovider.OpenRouter, "OPENROUTER_BASE_URL=http://127.0.0.1:9902/v1"},
		{"127.0.0.1:9903", mockprovider.Anthropic, "ANTHROPIC_BASE_URL=http://127.0.0.1:9903/v1"},
		{"127.0.0.1:9904", mockprovider.Gemini, "GOOGLE_BASE_URL=http://127.0.0.1:9904"},
		{"127.0.0.1:9905", mockprovider.MeshAPI, "MESHAPI_BASE_URL=http://127.0.0.1:9905/v1"},
	}
	for i, s := range all {
		h := mockprovider.NewMock(mockprovider.Options{Flavour: s.f}).Handler()
		log.Printf("%s mock listening on %s (%s)", s.f, s.addr, s.env)
		if i == len(all)-1 {
			log.Fatal(http.ListenAndServe(s.addr, h))
		}
		go func(addr string, h http.Handler) { log.Fatal(http.ListenAndServe(addr, h)) }(s.addr, h)
	}
}
