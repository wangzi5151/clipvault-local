package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/scrypt"
)

// Whole-database encryption for ClipVault-Local.
//
// Algorithm (IPC.md): scrypt(N=32768, r=8, p=1) derives a 32-byte key,
// AES-256-GCM encrypts each value with a random 12-byte nonce, stored as
// nonce‖ciphertext. TEXT columns store base64(nonce‖ciphertext), BLOB
// columns store the raw bytes.
//
// Only the entries' content columns are encrypted (text, html, preview,
// image, thumb). Metadata needed for dedup/quota/tags/search stays in
// plaintext. The settings table stays plaintext so the core can boot and
// report db_password_set without the key.

const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	gcmNonceLen  = 12
	saltLen      = 16
)

var errWrongPassword = errors.New("incorrect password")

// deriveKey derives a 32-byte AES key from password+salt.
func deriveKey(password string, salt []byte) ([]byte, error) {
	if len(password) == 0 {
		return nil, errors.New("empty password")
	}
	if len(salt) != saltLen {
		return nil, fmt.Errorf("bad salt length %d", len(salt))
	}
	return scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
}

// newSalt generates a random salt.
func newSalt() ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}

// encryptValue encrypts plaintext with key, returning nonce‖ciphertext.
func encryptValue(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, gcmNonceLen+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// decryptValue decrypts nonce‖ciphertext with key.
func decryptValue(key, data []byte) ([]byte, error) {
	if len(data) < gcmNonceLen {
		return nil, errors.New("ciphertext too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, ct := data[:gcmNonceLen], data[gcmNonceLen:]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("decryption failed")
	}
	return plain, nil
}

// encryptText is encryptValue for TEXT columns (base64-encoded at rest).
func encryptText(key []byte, plaintext string) (string, error) {
	enc, err := encryptValue(key, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// decryptText reverses encryptText. Empty input stays empty (avoids
// encrypting zero-length values into non-empty ciphertext).
func decryptText(key []byte, stored string) (string, error) {
	if stored == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", err
	}
	plain, err := decryptValue(key, raw)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// decryptBlob reverses raw-bytes encryption for BLOB columns.
func decryptBlob(key, stored []byte) ([]byte, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	return decryptValue(key, stored)
}

// checkPlaintext is the fixed sentinel encrypted into crypto_check so a
// password can be verified (and set_db_password can double as unlock)
// without touching entry rows.
const checkPlaintext = "clipvault-ok"
