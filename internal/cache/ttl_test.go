package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}
func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func TestTTLExpiry(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewWithClock[string, int](10*time.Second, 10, clk.Now)
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("want hit 1, got %v %v", v, ok)
	}
	clk.Advance(9 * time.Second)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("should still be live at 9s")
	}
	clk.Advance(time.Second)
	if _, ok := c.Get("a"); ok {
		t.Fatal("should be expired at 10s")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry should be reaped, len=%d", c.Len())
	}
}

func TestSetRefreshesTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewWithClock[string, int](10*time.Second, 10, clk.Now)
	c.Set("a", 1)
	clk.Advance(8 * time.Second)
	c.Set("a", 2)
	clk.Advance(8 * time.Second)
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Fatalf("want 2, got %v %v", v, ok)
	}
}

func TestLRUEviction(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewWithClock[string, int](time.Minute, 2, clk.Now)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Get("a") // a is now MRU
	c.Set("c", 3)
	if _, ok := c.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should remain")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should remain")
	}
	if c.Len() != 2 {
		t.Fatalf("len=%d", c.Len())
	}
}

func TestDeleteAndPurge(t *testing.T) {
	c := New[int, string](0, 0) // never expires, unbounded
	c.Set(1, "x")
	c.Set(2, "y")
	c.Delete(1)
	if _, ok := c.Get(1); ok {
		t.Fatal("deleted")
	}
	c.Delete(99) // no-op
	c.Purge()
	if c.Len() != 0 {
		t.Fatal("purged")
	}
}

func TestSetWithTTL(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	c := NewWithClock[string, int](time.Minute, 10, clk.Now)
	c.SetWithTTL("short", 1, time.Second)
	clk.Advance(2 * time.Second)
	if _, ok := c.Get("short"); ok {
		t.Fatal("short should expire")
	}
}

func TestConcurrent(t *testing.T) {
	c := New[int, int](time.Minute, 50)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				k := (g*31 + i) % 100
				c.Set(k, i)
				c.Get(k)
				if i%7 == 0 {
					c.Delete(k)
				}
			}
		}(g)
	}
	wg.Wait()
	if c.Len() > 50 {
		t.Fatalf("exceeded max: %d", c.Len())
	}
	_ = fmt.Sprint(c.Len())
}
