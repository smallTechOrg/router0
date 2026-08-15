package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// loadAESKey reads AES_KEY env var (64 hex chars = 32 bytes).
func loadAESKey() ([]byte, error) {
	hexKey := os.Getenv("AES_KEY")
	if len(hexKey) != 64 {
		return nil, fmt.Errorf("AES_KEY must be 64 hex chars, got %d", len(hexKey))
	}
	return hex.DecodeString(hexKey)
}

// Encrypt encrypts plaintext using AES-256-GCM with a random nonce.
// Returns ciphertext and nonce as separate byte slices.
func Encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	key, err := loadAESKey()
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	ciphertext = gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

// Decrypt decrypts ciphertext using AES-256-GCM.
func Decrypt(ciphertext, nonce []byte) (string, error) {
	key, err := loadAESKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", errors.New("invalid nonce size")
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
