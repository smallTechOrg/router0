package auth

import (
	"crypto/rand"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// GenerateZer0Key generates a new Zer0 key in the format zer0:sk-<32 base58 chars>.
func GenerateZer0Key() (fullKey, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	var sb strings.Builder
	for _, byt := range b {
		sb.WriteByte(base58Alphabet[int(byt)%58])
	}
	suffix := sb.String()
	fullKey = "zer0:sk-" + suffix
	prefix = suffix[:12]
	return
}
