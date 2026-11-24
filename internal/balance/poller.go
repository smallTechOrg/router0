// Package balance polls provider balance APIs for org keys and marks keys that
// are out of credits before a request fails on them (credit-tracking.md).
// Only OpenRouter documents a per-key balance API; other providers are reported
// as "unsupported" by the keys API without being polled.
package balance

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zer0-router/zer0/internal/keys"
	"github.com/zer0-router/zer0/internal/providers"
)

// Reason is the provider_key_state.reason the poller sets and clears.
const Reason = "balance_poll"

// Status values stored in provider_key_balances.status.
const (
	StatusOK          = "ok"
	StatusExhausted   = "exhausted"
	StatusError       = "error"
	StatusUnsupported = "unsupported"
)

// Supported reports whether router0 can poll the provider's balance.
func Supported(provider string) bool { return provider == "openrouter" }

// CreditsState is the keystate subset the poller writes (keystate.PGStore).
type CreditsState interface {
	SetCredits(ctx context.Context, keyID, reason string, ttl time.Duration) error
	ClearCredits(ctx context.Context, keyID, onlyReason string) error
}

// DB is the subset of *pgxpool.Pool the poller uses.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Fetcher reads an OpenRouter key's balance (providers.FetchOpenRouterBalance).
type Fetcher func(ctx context.Context, apiKey string) (*providers.OpenRouterBalance, error)

// Options tune the poller. Zero values take defaults.
type Options struct {
	Interval time.Duration // ROUTER0_BALANCE_POLL_SECONDS, default 600 s
	Fetch    Fetcher       // default providers.FetchOpenRouterBalance
	Decrypt  func(ciphertext, nonce []byte) (string, error)
	Logger   *slog.Logger
}

// Poller polls active OpenRouter keys.
type Poller struct {
	db       DB
	states   CreditsState
	interval time.Duration
	fetch    Fetcher
	decrypt  func(ciphertext, nonce []byte) (string, error)
	log      *slog.Logger
	running  chan struct{} // one poll pass at a time (cap 1)
}

// ErrBusy is returned by TryPollNow while another poll pass is running.
var ErrBusy = errors.New("balance: a poll is already running")

// New builds a poller.
func New(db DB, states CreditsState, o Options) *Poller {
	if o.Interval <= 0 {
		o.Interval = 10 * time.Minute
		if n, err := strconv.Atoi(os.Getenv("ROUTER0_BALANCE_POLL_SECONDS")); err == nil && n > 0 {
			o.Interval = time.Duration(n) * time.Second
		}
	}
	if o.Fetch == nil {
		o.Fetch = providers.FetchOpenRouterBalance
	}
	if o.Decrypt == nil {
		o.Decrypt = keys.Decrypt
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Poller{db: db, states: states, interval: o.Interval, fetch: o.Fetch, decrypt: o.Decrypt, log: o.Logger,
		running: make(chan struct{}, 1)}
}

// Interval is the polling period.
func (p *Poller) Interval() time.Duration { return p.interval }

// Start polls once immediately, then every interval until ctx ends.
func (p *Poller) Start(ctx context.Context) {
	go func() {
		p.pollLogged(ctx, "")
		t := time.NewTicker(p.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.pollLogged(ctx, "")
			}
		}
	}()
}

func (p *Poller) pollLogged(ctx context.Context, orgID string) {
	n, err := p.PollNow(ctx, orgID)
	if err != nil {
		p.log.Warn("balance: poll failed", "error", err.Error())
		return
	}
	if n > 0 {
		p.log.Info("balance: polled keys", "count", n)
	}
}

type keyRow struct {
	id, orgID         string
	ciphertext, nonce []byte
}

// PollNow polls every active OpenRouter key of orgID ("" = all orgs) and returns
// how many keys were polled. Per-key failures are recorded as status "error"
// (last values kept, key state untouched) and do not fail the pass.
//
// It waits for a running pass to finish, giving up when ctx ends.
func (p *Poller) PollNow(ctx context.Context, orgID string) (int, error) {
	select {
	case p.running <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-p.running }()
	return p.poll(ctx, orgID)
}

// TryPollNow is PollNow without waiting: it returns ErrBusy at once if a pass
// (for any org) is running. The on-demand HTTP endpoint uses it so a click
// never queues behind a slow background pass.
func (p *Poller) TryPollNow(ctx context.Context, orgID string) (int, error) {
	select {
	case p.running <- struct{}{}:
	default:
		return 0, ErrBusy
	}
	defer func() { <-p.running }()
	return p.poll(ctx, orgID)
}

func (p *Poller) poll(ctx context.Context, orgID string) (int, error) {
	rows, err := p.db.Query(ctx,
		`SELECT id::text, org_id::text, ciphertext, nonce FROM provider_keys
		  WHERE provider = 'openrouter' AND status = 'active' AND ($1 = '' OR org_id::text = $1)`, orgID)
	if err != nil {
		return 0, err
	}
	var ks []keyRow
	for rows.Next() {
		var k keyRow
		if err := rows.Scan(&k.id, &k.orgID, &k.ciphertext, &k.nonce); err != nil {
			rows.Close()
			return 0, err
		}
		ks = append(ks, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, k := range ks {
		p.pollKey(ctx, k)
	}
	return len(ks), nil
}

func (p *Poller) pollKey(ctx context.Context, k keyRow) {
	plain, err := p.decrypt(k.ciphertext, k.nonce)
	if err != nil {
		p.markError(ctx, k)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	b, err := p.fetch(cctx, plain)
	cancel()
	if err != nil {
		// Never log err text: provider messages could echo key material.
		p.log.Warn("balance: fetch failed", "key_id", k.id)
		p.markError(ctx, k)
		return
	}
	status := StatusOK
	if b.RemainingUSD != nil && *b.RemainingUSD <= 0 {
		status = StatusExhausted
	}
	if _, err := p.db.Exec(ctx, upsertSQL, k.id, k.orgID, b.RemainingUSD, b.LimitUSD, b.UsageUSD, status); err != nil {
		p.log.Warn("balance: store failed", "key_id", k.id, "error", err.Error())
	}
	if status == StatusExhausted {
		// Lasts one poll interval plus a minute, so a missed poll lets the key retry.
		if err := p.states.SetCredits(ctx, k.id, Reason, p.interval+time.Minute); err != nil {
			p.log.Warn("balance: set credits state failed", "key_id", k.id, "error", err.Error())
		}
		return
	}
	// Clear only a state this poller set; a live 402 is left to expire.
	if err := p.states.ClearCredits(ctx, k.id, Reason); err != nil {
		p.log.Warn("balance: clear credits state failed", "key_id", k.id, "error", err.Error())
	}
}

const upsertSQL = `INSERT INTO provider_key_balances (provider_key_id, org_id, remaining_usd, limit_usd, usage_usd, status, polled_at)
 VALUES ($1, $2, $3, $4, $5, $6, NOW())
 ON CONFLICT (provider_key_id) DO UPDATE SET
   remaining_usd = EXCLUDED.remaining_usd, limit_usd = EXCLUDED.limit_usd, usage_usd = EXCLUDED.usage_usd,
   status = EXCLUDED.status, polled_at = NOW()`

// markError records a failed poll, keeping the last values.
func (p *Poller) markError(ctx context.Context, k keyRow) {
	_, err := p.db.Exec(ctx, `INSERT INTO provider_key_balances (provider_key_id, org_id, status, polled_at)
	 VALUES ($1, $2, 'error', NOW())
	 ON CONFLICT (provider_key_id) DO UPDATE SET status = 'error', polled_at = NOW()`, k.id, k.orgID)
	if err != nil {
		p.log.Warn("balance: store failed", "key_id", k.id, "error", err.Error())
	}
}
