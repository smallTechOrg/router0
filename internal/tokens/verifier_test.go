package tokens

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zer0-router/zer0/internal/auth"
)

type fakeStore struct {
	mu      sync.Mutex
	byHash  map[string]Record
	legacy  map[string][]Record
	lookups int
	legacyQ int
	touches map[string]int
	err     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{byHash: map[string]Record{}, legacy: map[string][]Record{}, touches: map[string]int{}}
}

func (f *fakeStore) FindByHash(_ context.Context, h string) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.err != nil {
		return Record{}, f.err
	}
	r, ok := f.byHash[h]
	if !ok {
		return Record{}, ErrInvalid
	}
	return r, nil
}
func (f *fakeStore) LegacyByPrefix(_ context.Context, p string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.legacyQ++
	return f.legacy[p], nil
}
func (f *fakeStore) TouchLastUsed(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches[id]++
	return nil
}
func (f *fakeStore) touchCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.touches[id]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestVerifierCacheHitDoesNoDBQuery(t *testing.T) {
	st := newFakeStore()
	tok, _, _ := auth.GenerateServiceToken()
	st.byHash[auth.HashToken(tok)] = Record{ID: "t1", OrgID: "o1", Name: "svc"}
	clk := &clock{t: time.Unix(1000, 0)}
	v := NewVerifierWithClock(st, 30*time.Second, 100, clk.Now)

	for i := 0; i < 5; i++ {
		p, err := v.Verify(context.Background(), tok)
		if err != nil || p.OrgID != "o1" || p.TokenID != "t1" || p.TokenName != "svc" {
			t.Fatalf("verify: %+v %v", p, err)
		}
	}
	if st.lookups != 1 {
		t.Fatalf("expected 1 DB lookup, got %d", st.lookups)
	}

	// TTL expiry re-queries.
	clk.Add(31 * time.Second)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if st.lookups != 2 {
		t.Fatalf("expected re-query after TTL, lookups=%d", st.lookups)
	}
}

func TestVerifierInvalidAndRevoked(t *testing.T) {
	st := newFakeStore()
	tok, _, _ := auth.GenerateServiceToken()
	st.byHash[auth.HashToken(tok)] = Record{ID: "t1", OrgID: "o1", Name: "svc", Revoked: true}
	v := NewVerifier(st, 0, 0)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want ErrRevoked, got %v", err)
	}
	other, _, _ := auth.GenerateServiceToken()
	if _, err := v.Verify(context.Background(), other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	before := st.lookups
	if _, err := v.Verify(context.Background(), "garbage"); !errors.Is(err, ErrInvalid) || st.lookups != before {
		t.Fatalf("malformed token must fail without a DB query (err=%v)", err)
	}
	st.err = errors.New("db down")
	third, _, _ := auth.GenerateServiceToken()
	if _, err := v.Verify(context.Background(), third); err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrRevoked) {
		t.Fatalf("infra error must not look like invalid token: %v", err)
	}
}

func TestVerifierLegacyBcryptPath(t *testing.T) {
	st := newFakeStore()
	body := "123456789ABCDEFGHJKLMNPQRSTUVWXY"
	raw := auth.LegacyTokenScheme + body
	hash, _ := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	st.legacy[body[:12]] = []Record{
		{ID: "other", OrgID: "oX", Name: "legacy-default", Hash: "$2a$04$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidi"},
		{ID: "leg1", OrgID: "o1", Name: "legacy-default", Hash: string(hash)},
	}
	v := NewVerifier(st, 0, 0)
	p, err := v.Verify(context.Background(), raw)
	if err != nil || p.TokenID != "leg1" || p.OrgID != "o1" {
		t.Fatalf("legacy verify: %+v %v", p, err)
	}
	// cached: second call must not hit bcrypt/DB again
	if _, err := v.Verify(context.Background(), raw); err != nil || st.legacyQ != 1 {
		t.Fatalf("legacy result should be cached (queries=%d err=%v)", st.legacyQ, err)
	}
	// wrong body with same prefix fails
	bad := auth.LegacyTokenScheme + body[:12] + "ZZZZZZZZZZZZZZZZZZZZ"
	if _, err := v.Verify(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	// an r0-sk- token with the same body must not take the legacy path
	if _, err := v.Verify(context.Background(), auth.ServiceTokenScheme+body); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scheme confusion: %v", err)
	}
	// revoked legacy
	st2 := newFakeStore()
	st2.legacy[body[:12]] = []Record{{ID: "leg1", OrgID: "o1", Revoked: true, Hash: string(hash)}}
	if _, err := NewVerifier(st2, 0, 0).Verify(context.Background(), raw); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want ErrRevoked, got %v", err)
	}
}

func TestVerifierEvictOnRevoke(t *testing.T) {
	st := newFakeStore()
	tok, _, _ := auth.GenerateServiceToken()
	st.byHash[auth.HashToken(tok)] = Record{ID: "t1", OrgID: "o1", Name: "svc"}
	v := NewVerifier(st, 30*time.Second, 100)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	// revoke in the store, then evict; next call must see the revocation immediately
	r := st.byHash[auth.HashToken(tok)]
	r.Revoked = true
	st.byHash[auth.HashToken(tok)] = r
	v.Evict("t1")
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrRevoked) {
		t.Fatalf("want ErrRevoked right after Evict, got %v", err)
	}
}

func TestVerifierLastUsedThrottle(t *testing.T) {
	st := newFakeStore()
	tok, _, _ := auth.GenerateServiceToken()
	st.byHash[auth.HashToken(tok)] = Record{ID: "t1", OrgID: "o1", Name: "svc"}
	clk := &clock{t: time.Unix(1000, 0)}
	v := NewVerifierWithClock(st, time.Hour, 100, clk.Now)
	for i := 0; i < 10; i++ {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return st.touchCount("t1") >= 1 })
	time.Sleep(30 * time.Millisecond)
	if n := st.touchCount("t1"); n != 1 {
		t.Fatalf("expected 1 touch within a minute, got %d", n)
	}
	clk.Add(61 * time.Second)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return st.touchCount("t1") == 2 })
}
