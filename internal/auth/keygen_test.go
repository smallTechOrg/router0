package auth

import (
	"math"
	"regexp"
	"testing"
)

var tokenRe = regexp.MustCompile(`^r0-sk-[1-9A-HJ-NP-Za-km-z]{32}$`)

func TestGenerateServiceTokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		tok, prefix, err := GenerateServiceToken()
		if err != nil {
			t.Fatal(err)
		}
		if !tokenRe.MatchString(tok) {
			t.Fatalf("bad format: %q", tok)
		}
		if prefix != tok[len(ServiceTokenScheme):len(ServiceTokenScheme)+TokenPrefixLen] {
			t.Fatalf("prefix mismatch: %q vs %q", prefix, tok)
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
		if _, body, ok := ParseServiceToken(tok); !ok || len(body) != 32 {
			t.Fatalf("ParseServiceToken failed for %q", tok)
		}
	}
}

func TestRandomBase58Distribution(t *testing.T) {
	const n = 58 * 4000
	s, err := randomBase58(n)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[byte]int{}
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	if len(counts) != 58 {
		t.Fatalf("expected 58 distinct symbols, got %d", len(counts))
	}
	// Chi-square vs uniform; df=57, 99.99th percentile is ~100. A byte%58 bias
	// would not be caught at this size, but gross skew would be.
	exp := float64(n) / 58
	var chi float64
	for _, c := range counts {
		d := float64(c) - exp
		chi += d * d / exp
	}
	if chi > 110 || math.IsNaN(chi) {
		t.Fatalf("distribution not uniform: chi2=%.1f", chi)
	}
	// Symbols the biased scheme over-produces ('1'..'4' from bytes 232-255 wrap) must not exceed expectation by much.
	for _, c := range []byte("1234") {
		if float64(counts[c]) > exp*1.15 {
			t.Fatalf("symbol %c over-represented: %d vs %.0f", c, counts[c], exp)
		}
	}
}

func TestParseServiceToken(t *testing.T) {
	good := "r0-sk-" + "123456789AB123456789ABCDEFGHJKLM"
	legacy := "zer0:sk-" + "123456789AB123456789ABCDEFGHJKLM"
	for _, c := range []struct {
		in     string
		scheme string
		ok     bool
	}{
		{good, ServiceTokenScheme, true},
		{legacy, LegacyTokenScheme, true},
		{"r0-sk-short", "", false},
		{"r0-sk-" + "023456789AB123456789ABCDEFGHJKLM", "", false}, // '0' not base58
		{"sk-abc", "", false},
		{"", "", false},
	} {
		s, _, ok := ParseServiceToken(c.in)
		if ok != c.ok || s != c.scheme {
			t.Errorf("%q => (%q,%v), want (%q,%v)", c.in, s, ok, c.scheme, c.ok)
		}
	}
}

func TestHashToken(t *testing.T) {
	if got := HashToken("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256 mismatch: %s", got)
	}
}
