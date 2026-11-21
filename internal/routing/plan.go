// Package routing turns a requested model into an ordered candidate plan and
// executes it with failover. It holds no Redis or per-user pool state: key
// cooldowns live in keystate (Postgres), the plan is rebuilt for every request.
package routing

import (
	"sort"
	"strings"

	"github.com/zer0-router/zer0/internal/catalog"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
)

// Candidate is one (provider, provider model, key) the executor may try.
type Candidate struct{ Provider, ProviderModelID, KeyID, KeyLabel string }

// ParseProviderOverride parses the X-Router0-Providers header value: comma list,
// trimmed, lowercased, aliases mapped (gemini -> google), de-duplicated keeping
// the first occurrence. Unknown is the first provider id that is not a
// registered adapter (the caller answers 400 invalid_provider_override).
// An empty/whitespace header yields (nil, "").
func ParseProviderOverride(header string) (override []string, unknown string) {
	seen := map[string]bool{}
	for _, part := range strings.Split(header, ",") {
		p := providers.CanonicalName(part)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := providers.Get(p); err != nil {
			if unknown == "" {
				unknown = p
			}
			continue
		}
		override = append(override, p)
	}
	return override, unknown
}

// BuildPlan builds the ordered candidate list.
//
//   - override non-empty: the order is exactly the override list (restricting and
//     reordering); providers not offering the model or without keys are skipped.
//     Override ids that are not offered by any entry are ignored.
//   - otherwise: providerOrder first, then remaining providers with entries
//     alphabetically.
//
// Within a provider, keys keep their given (position) order. Keys with a status
// other than active are skipped. Moving transient-cooldown keys to the end is
// done by the executor, which owns the key-state lookup.
func BuildPlan(entries []catalog.Entry, providerOrder, override []string, keysByProvider map[string][]*keys.ProviderKey) []Candidate {
	entryBy := map[string]catalog.Entry{}
	for _, e := range entries {
		p := providers.CanonicalName(e.Provider)
		if _, ok := entryBy[p]; !ok {
			entryBy[p] = e
		}
	}
	var order []string
	if len(override) > 0 {
		order = normalize(override)
	} else {
		order = normalize(providerOrder)
		have := map[string]bool{}
		for _, p := range order {
			have[p] = true
		}
		var rest []string
		for p := range entryBy {
			if !have[p] {
				rest = append(rest, p)
			}
		}
		sort.Strings(rest)
		order = append(order, rest...)
	}
	var plan []Candidate
	for _, p := range order {
		e, ok := entryBy[p]
		if !ok {
			continue
		}
		for _, k := range keysByProvider[p] {
			if k == nil || (k.Status != "" && k.Status != "active") {
				continue
			}
			plan = append(plan, Candidate{Provider: p, ProviderModelID: e.ProviderModelID, KeyID: k.ID, KeyLabel: k.Label})
		}
	}
	return plan
}

func normalize(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = providers.CanonicalName(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
