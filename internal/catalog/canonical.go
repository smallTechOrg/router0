// Package catalog maintains the per-org model catalog: canonical names,
// provider discovery, and request-time resolution.
package catalog

import (
	"regexp"
	"strings"
)

var (
	dateDashRe   = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)
	dateCompact  = regexp.MustCompile(`-\d{8}$`)
	digitsSegRe  = regexp.MustCompile(`-\d{3,5}$`)
	bareVersion  = regexp.MustCompile(`-v\d+$`) // "-v0", "-v2"; "-v0.1" does not match
	multiDashRe  = regexp.MustCompile(`-{2,}`)
	trailingSufs = []string{"-latest", "-versatile", "-instruct", "-instant", "-chat", "-it", "-hf"}
)

// Canonicalize maps a provider-specific model id (or a caller-supplied name
// when provider is "") to its canonical name. Deterministic; see
// spec/capabilities/model-catalog.md.
func Canonicalize(provider, modelID string) string {
	s := strings.ToLower(strings.TrimSpace(modelID))
	for _, k := range [][2]string{{strings.ToLower(provider), s}, {"", s}} {
		if v, ok := overrides[k]; ok && !strings.HasPrefix(v, "kind:") {
			return v
		}
	}
	s = strings.TrimPrefix(s, "models/")

	// Variant tag (":free", ":beta", ...) is re-attached at the end.
	variant := ""
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s, variant = s[:i], s[i:]
	}
	// Vendor prefix.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasPrefix(s, "meta-llama-") {
		s = "llama-" + strings.TrimPrefix(s, "meta-llama-")
	}
	// Trailing noise tokens.
	for {
		before := s
		s = dateDashRe.ReplaceAllString(s, "")
		s = dateCompact.ReplaceAllString(s, "")
		s = digitsSegRe.ReplaceAllString(s, "")
		s = bareVersion.ReplaceAllString(s, "")
		for _, suf := range trailingSufs {
			if strings.HasSuffix(s, suf) {
				s = strings.TrimSuffix(s, suf)
			}
		}
		if s == before {
			break
		}
	}
	if strings.HasPrefix(s, "claude-") {
		s = strings.ReplaceAll(s, ".", "-")
	}
	s = multiDashRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		// Degenerate input: fall back to the trimmed lowercase id.
		return strings.ToLower(strings.TrimSpace(modelID))
	}
	return s + variant
}
