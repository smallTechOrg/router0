package providers

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = map[string]Provider{}
)

// aliases map accepted alternative provider ids to the canonical one.
var aliases = map[string]string{
	"gemini":   "google",
	"aifiesta": "meshapi",
	"mesh":     "meshapi",
}

// Register adds a provider under its Name(). Each adapter calls this from its
// own file's init().
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	registry[p.Name()] = p
}

// CanonicalName resolves aliases (e.g. "gemini" -> "google") and lowercases.
func CanonicalName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if a, ok := aliases[n]; ok {
		return a
	}
	return n
}

// Get returns a provider by id or alias.
func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := registry[CanonicalName(name)]
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

// ValidProviders returns the sorted registered provider ids.
func ValidProviders() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
