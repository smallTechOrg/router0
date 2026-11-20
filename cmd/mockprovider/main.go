// Command mockprovider serves scripted OpenAI-compatible mock providers for
// keyless manual testing: a Groq-flavoured mock on :9901 and an
// OpenRouter-flavoured mock on :9902. Run the gateway with
//
//	GROQ_BASE_URL=http://127.0.0.1:9901/v1 OPENROUTER_BASE_URL=http://127.0.0.1:9902/v1 make dev
//
// and pick behaviour with the provider key you add (mock-ok-*, mock-429-15-*,
// mock-402-*, mock-401-*, mock-500-*, mock-midstream-*). See
// internal/testutil/mockprovider for the full list. POST a JSON array of ids to
// /_models on either port to change its model list at runtime.
package main

import (
	"log"
	"net/http"

	"github.com/zer0-router/zer0/internal/testutil/mockprovider"
)

func main() {
	groq := mockprovider.NewMock(mockprovider.Options{Flavour: mockprovider.Groq})
	or := mockprovider.NewMock(mockprovider.Options{Flavour: mockprovider.OpenRouter})

	go func() {
		log.Println("groq mock listening on :9901 (GROQ_BASE_URL=http://127.0.0.1:9901/v1)")
		log.Fatal(http.ListenAndServe("127.0.0.1:9901", groq.Handler()))
	}()
	log.Println("openrouter mock listening on :9902 (OPENROUTER_BASE_URL=http://127.0.0.1:9902/v1)")
	log.Fatal(http.ListenAndServe("127.0.0.1:9902", or.Handler()))
}
