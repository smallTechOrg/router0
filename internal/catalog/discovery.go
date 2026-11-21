package catalog

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
)

// ErrInProgress means a refresh for the (org, provider) is already running in this process.
var ErrInProgress = errors.New("catalog: refresh in progress")

// ErrNoKeys means the org has no usable key for the provider.
var ErrNoKeys = errors.New("catalog: no active key for provider")

// Discoverer refreshes an org's catalog for one provider.
type Discoverer interface {
	RefreshProvider(ctx context.Context, orgID, provider string) (int, error)
}

// ActiveKey is a decrypted provider key.
type ActiveKey struct{ ID, APIKey string }

// KeySource supplies the org's active keys for a provider in position order
// and lets discovery flag a key that failed authentication.
type KeySource interface {
	ActiveKeys(ctx context.Context, orgID, provider string) ([]ActiveKey, error)
	MarkAuthError(ctx context.Context, keyID string) error
}

// KeyRepo is the subset of *keys.Repository (slice 1D) the catalog needs.
type KeyRepo interface {
	ActiveByOrgProvider(ctx context.Context, orgID, provider string) ([]*keys.ProviderKey, error)
	MarkAuthError(ctx context.Context, keyID string) error
}

type repoKeySource struct{ r KeyRepo }

// NewKeySource adapts a keys repository, decrypting with keys.Decrypt.
func NewKeySource(r KeyRepo) KeySource { return repoKeySource{r} }

func (s repoKeySource) ActiveKeys(ctx context.Context, orgID, provider string) ([]ActiveKey, error) {
	ks, err := s.r.ActiveByOrgProvider(ctx, orgID, provider)
	if err != nil {
		return nil, err
	}
	out := make([]ActiveKey, 0, len(ks))
	for _, k := range ks {
		plain, err := keys.Decrypt(k.Ciphertext, k.Nonce)
		if err != nil {
			slog.Warn("catalog: key decrypt failed", "key_id", k.ID)
			continue
		}
		out = append(out, ActiveKey{ID: k.ID, APIKey: plain})
	}
	return out, nil
}

func (s repoKeySource) MarkAuthError(ctx context.Context, id string) error {
	return s.r.MarkAuthError(ctx, id)
}

// DefaultRefreshTimeout caps a single provider refresh.
const DefaultRefreshTimeout = 30 * time.Second

// Discovery implements Discoverer.
type Discovery struct {
	repo  *Repository
	keys  KeySource
	get   func(name string) (providers.Provider, error)
	locks sync.Map // "org|provider" -> *sync.Mutex
}

// NewDiscovery builds a Discovery using the global providers registry.
func NewDiscovery(repo *Repository, ks KeySource) *Discovery {
	return &Discovery{repo: repo, keys: ks, get: providers.Get}
}

// WithProviderLookup overrides provider lookup (tests).
func (d *Discovery) WithProviderLookup(f func(string) (providers.Provider, error)) *Discovery {
	d.get = f
	return d
}

// RefreshProvider lists models using the org's first working key and stores them.
// Returns ErrInProgress if a refresh for the pair is already running.
func (d *Discovery) RefreshProvider(ctx context.Context, orgID, provider string) (int, error) {
	return d.refresh(ctx, orgID, provider, "discovered")
}

func (d *Discovery) refresh(ctx context.Context, orgID, provider, source string) (int, error) {
	mi, _ := d.locks.LoadOrStore(orgID+"|"+provider, &sync.Mutex{})
	mu := mi.(*sync.Mutex)
	if !mu.TryLock() {
		return 0, ErrInProgress
	}
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, DefaultRefreshTimeout)
	defer cancel()

	prov, err := d.get(provider)
	if err != nil {
		return 0, err
	}
	ks, err := d.keys.ActiveKeys(ctx, orgID, provider)
	if err != nil {
		return 0, err
	}
	if len(ks) == 0 {
		return 0, ErrNoKeys
	}

	var lastErr error
	for _, k := range ks {
		infos, err := prov.ListModels(ctx, k.APIKey)
		if err != nil {
			lastErr = err
			var pe *providers.ProviderError
			if errors.As(err, &pe) && pe.Class == providers.ErrorClassAuth {
				if merr := d.keys.MarkAuthError(ctx, k.ID); merr != nil {
					slog.Warn("catalog: mark key auth error failed", "key_id", k.ID)
				}
				continue // try the provider's next key
			}
			break // non-auth failure: another key will not help
		}
		models := make([]Model, 0, len(infos))
		for _, mi := range infos {
			var methods []string
			if v, ok := mi.Extra["methods"].([]string); ok {
				methods = v
			}
			kind := ClassifyKind(provider, mi.ID, methods)
			// A provider that declares each model's type itself (e.g. Mesh API's
			// model_type) is authoritative over name-pattern rules.
			if k, ok := mi.Extra["kind"].(string); ok && ValidKind(k) {
				kind = k
			}
			models = append(models, Model{
				ProviderModelID:   mi.ID,
				Kind:              kind,
				ContextWindow:     mi.ContextWindow,
				InputCostPerMTok:  mi.InputCostPerMTok,
				OutputCostPerMTok: mi.OutputCostPerMTok,
			})
		}
		return d.repo.ApplyListing(ctx, orgID, provider, source, models)
	}
	class := "transient"
	var pe *providers.ProviderError
	if errors.As(lastErr, &pe) {
		class = string(pe.Class)
	}
	if rerr := d.repo.RecordRefreshError(context.WithoutCancel(ctx), orgID, provider, class); rerr != nil {
		slog.Warn("catalog: record refresh error failed", "org_id", orgID, "provider", provider)
	}
	return 0, lastErr
}
