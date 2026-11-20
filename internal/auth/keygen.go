package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const (
	// ServiceTokenScheme is the prefix of every newly issued router0 token.
	ServiceTokenScheme = "r0-sk-"
	// LegacyTokenScheme is the prefix of pre-router0 per-user keys (still accepted).
	LegacyTokenScheme = "zer0:sk-"

	// ServiceTokenBodyLen is the number of base58 characters after the scheme.
	ServiceTokenBodyLen = 32
	// TokenPrefixLen is the number of body characters stored/displayed as the prefix.
	TokenPrefixLen = 12

	// rejectionLimit is the largest multiple of 58 that fits in a byte (58*4).
	// Bytes >= this value are discarded so every alphabet symbol is equally likely.
	rejectionLimit = 232
)

// randomBase58 returns n characters drawn uniformly from the base58 alphabet
// using rejection sampling over crypto/rand bytes.
func randomBase58(n int) (string, error) {
	out := make([]byte, 0, n)
	buf := make([]byte, n+n/2+8)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b >= rejectionLimit {
				continue
			}
			out = append(out, base58Alphabet[int(b)%58])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// GenerateServiceToken creates a new service token: "r0-sk-" + 32 base58 chars.
// prefix is the first 12 characters after the scheme (stored for display).
func GenerateServiceToken() (fullToken, prefix string, err error) {
	body, err := randomBase58(ServiceTokenBodyLen)
	if err != nil {
		return "", "", err
	}
	return ServiceTokenScheme + body, body[:TokenPrefixLen], nil
}

// HashToken returns hex(sha256(raw)), the storage/cache key for a token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ParseServiceToken splits a presented token into its scheme and 32-char body.
// ok is false for anything that is not a well-formed r0-sk- or legacy zer0:sk- token.
func ParseServiceToken(raw string) (scheme, body string, ok bool) {
	switch {
	case strings.HasPrefix(raw, ServiceTokenScheme):
		scheme = ServiceTokenScheme
	case strings.HasPrefix(raw, LegacyTokenScheme):
		scheme = LegacyTokenScheme
	default:
		return "", "", false
	}
	body = raw[len(scheme):]
	if len(body) != ServiceTokenBodyLen {
		return "", "", false
	}
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(base58Alphabet, body[i]) < 0 {
			return "", "", false
		}
	}
	return scheme, body, true
}
