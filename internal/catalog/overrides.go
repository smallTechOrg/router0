package catalog

import "strings"

// overrides maps {provider or "", lower(trim(id))} to a canonical name. It is
// consulted first by Canonicalize (model-catalog.md, Canonicalisation step 2).
// A value of the form "kind:<kind>" is a kind override instead (consulted by
// ClassifyKind, ignored by Canonicalize), for a model whose id accidentally
// matches a ClassifyKind pattern. The key id is compared after stripping
// "models/" and any "vendor/" prefix for kind overrides.
var overrides = map[[2]string]string{
	// Chat model whose id must stay chat even if a pattern ever matches.
	{"google", "gemini-3.1-pro-preview-customtools"}: "kind:chat",
	// Hypothetical guardian-style chat ids match the "guard" pattern.
	{"", "guardian-chat"}: "kind:chat",
}

func kindOverride(provider, id string) (string, bool) {
	for _, k := range [][2]string{{strings.ToLower(provider), id}, {"", id}} {
		if v, ok := overrides[k]; ok && strings.HasPrefix(v, "kind:") {
			return strings.TrimPrefix(v, "kind:"), true
		}
	}
	return "", false
}
