package routing

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
)

// SelectedCombo is the result of combo selection, including the decrypted key.
type SelectedCombo struct {
	Combo
	DecryptedKey string
}

// Selector selects combos and decrypts keys for use.
type Selector struct {
	pool    *Pool
	pgPool  *pgxpool.Pool
	keyRepo *keys.Repository
}

func NewSelector(r *redis.Client, pg *pgxpool.Pool) *Selector {
	return &Selector{
		pool:    NewPool(r),
		pgPool:  pg,
		keyRepo: keys.NewRepository(pg),
	}
}

func (s *Selector) Pool() *Pool {
	return s.pool
}

// Select picks the best combo and returns it with the decrypted API key.
func (s *Selector) Select(ctx context.Context, userID string) (*SelectedCombo, []ScoredCombo, error) {
	scored, err := s.pool.SelectCombo(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if len(scored) == 0 {
		return nil, nil, fmt.Errorf("no combos available")
	}
	// Try to decrypt the top combo's key
	for _, sc := range scored {
		key, err := s.keyRepo.GetByID(ctx, sc.ProviderKeyID, userID)
		if err != nil {
			slog.Warn("selector: key not found", "key_id", sc.ProviderKeyID)
			continue
		}
		decrypted, err := keys.Decrypt(key.Ciphertext, key.Nonce)
		if err != nil {
			slog.Warn("selector: key decrypt failed", "key_id", sc.ProviderKeyID)
			continue
		}
		return &SelectedCombo{
			Combo:        sc.Combo,
			DecryptedKey: decrypted,
		}, scored, nil
	}
	return nil, nil, fmt.Errorf("no usable key in pool")
}

// SelectN picks up to n combos (for hedging) excluding already-tried keyIDs.
func (s *Selector) SelectN(ctx context.Context, userID string, n int, exclude map[string]bool) ([]*SelectedCombo, error) {
	scored, err := s.pool.SelectCombo(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []*SelectedCombo
	for _, sc := range scored {
		if exclude[sc.ProviderKeyID] {
			continue
		}
		key, err := s.keyRepo.GetByID(ctx, sc.ProviderKeyID, userID)
		if err != nil {
			continue
		}
		decrypted, err := keys.Decrypt(key.Ciphertext, key.Nonce)
		if err != nil {
			continue
		}
		out = append(out, &SelectedCombo{
			Combo:        sc.Combo,
			DecryptedKey: decrypted,
		})
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// Execute attempts the inference with adaptive retry (sequential → parallel hedge).
func (s *Selector) Execute(ctx context.Context, userID string, req *providers.ChatRequest) (*providers.ChatResponse, *SelectedCombo, int, error) {
	scored, err := s.pool.SelectCombo(ctx, userID)
	if err != nil {
		return nil, nil, 0, err
	}

	tried := map[string]bool{}
	retryCount := 0

	for i, sc := range scored {
		if i >= 4 {
			break
		}
		if tried[sc.ProviderKeyID] {
			continue
		}
		tried[sc.ProviderKeyID] = true

		key, err := s.keyRepo.GetByID(ctx, sc.ProviderKeyID, userID)
		if err != nil {
			continue
		}
		decrypted, err := keys.Decrypt(key.Ciphertext, key.Nonce)
		if err != nil {
			continue
		}

		p, err := providers.Get(sc.Provider)
		if err != nil {
			continue
		}

		resp, err := p.Complete(ctx, req, decrypted)
		if err == nil {
			sel := &SelectedCombo{Combo: sc.Combo, DecryptedKey: decrypted}
			return resp, sel, retryCount, nil
		}
		retryCount++
		slog.Warn("provider attempt failed", "provider", sc.Provider, "model", sc.Model, "error", err.Error())

		// After second failure, try hedging remaining 2
		if i >= 1 {
			break
		}
	}

	// Hedge: try remaining combos in parallel
	remaining, _ := s.SelectN(ctx, userID, 2, tried)
	if len(remaining) > 0 {
		type result struct {
			resp *providers.ChatResponse
			sel  *SelectedCombo
		}
		ch := make(chan result, len(remaining))
		hedgeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		for _, sel := range remaining {
			sel := sel
			go func() {
				p, err := providers.Get(sel.Provider)
				if err != nil {
					ch <- result{}
					return
				}
				resp, err := p.Complete(hedgeCtx, req, sel.DecryptedKey)
				if err != nil {
					ch <- result{}
					return
				}
				ch <- result{resp: resp, sel: sel}
			}()
		}
		for range remaining {
			r := <-ch
			if r.resp != nil {
				cancel()
				return r.resp, r.sel, retryCount + 1, nil
			}
		}
	}

	return nil, nil, retryCount, fmt.Errorf("all providers failed")
}
