package providers

import (
	"fmt"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = map[string]Provider{}
)

func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	registry[p.Name()] = p
}

func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
	return p, nil
}

func All() map[string]Provider {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]Provider, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

func ValidProviders() []string {
	return []string{"google", "groq", "openrouter", "nvidia", "together", "baseten"}
}

func init() {
	Register(&GroqProvider{})
	Register(&GoogleProvider{})
	Register(&OpenRouterProvider{})
	Register(&NvidiaProvider{})
	Register(&TogetherProvider{})
	Register(&BasetenProvider{})
}
