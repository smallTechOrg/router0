package catalog

// overrides maps {provider or "", lower(trim(id))} to a canonical name. It is
// consulted first by Canonicalize (model-catalog.md, Canonicalisation step 2).
// Phase 1 ships it empty: the generic rules cover every fixture. Add entries
// here only for ids the rules mishandle.
var overrides = map[[2]string]string{}
