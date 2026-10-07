package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	salt, err := newSalt()
	if err != nil {
		t.Fatal(err)
	}
	key, err := deriveKey("correct-horse", salt)
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range [][]byte{
		[]byte("hello 剪贴板"),
		{},
		bytes.Repeat([]byte("x"), 100000),
	} {
		enc, err := encryptValue(key, pt)
		if err != nil {
			t.Fatal(err)
		}
		if len(enc) < 12+16 {
			t.Fatalf("ciphertext too short: %d", len(enc))
		}
		dec, err := decryptValue(key, enc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(dec, pt) {
			t.Fatalf("roundtrip mismatch for len %d", len(pt))
		}
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	salt, _ := newSalt()
	k1, _ := deriveKey("password-one", salt)
	k2, _ := deriveKey("password-two", salt)
	enc, _ := encryptValue(k1, []byte("secret"))
	if _, err := decryptValue(k2, enc); err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	salt, _ := newSalt()
	key, _ := deriveKey("pw", salt)
	enc, _ := encryptValue(key, []byte("secret"))
	enc[len(enc)-1] ^= 0xFF
	if _, err := decryptValue(key, enc); err == nil {
		t.Fatal("decrypt of tampered data should fail")
	}
}

func TestTextBase64Roundtrip(t *testing.T) {
	salt, _ := newSalt()
	key, _ := deriveKey("pw", salt)
	for _, s := range []string{"", "密码 123", "multi\nline\r\ntext"} {
		enc, err := encryptText(key, s)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := decryptText(key, enc)
		if err != nil {
			t.Fatal(err)
		}
		if dec != s {
			t.Fatalf("mismatch: %q != %q", dec, s)
		}
	}
}

// TestStorePasswordFlow exercises whole-DB encryption via the Store:
// set password -> verify encrypted at rest -> wrong password rejected ->
// unlock via set_db_password -> decrypt back to plaintext.
func TestStorePasswordFlow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clipvault.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.AddEntry(&Entry{Kind: KindText, Text: "secret 密码 text", Preview: "secret 密码 text", SHA256: "aabb", TextLen: 13})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetPassword("s3cr3t"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetSettings()
	if !st.DBPasswordSet {
		t.Fatal("db_password_set should be true")
	}
	// At rest, text column must not contain plaintext.
	var raw string
	if err := s.db.QueryRow(`SELECT text FROM entries WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "secret 密码 text" {
		t.Fatal("text stored in plaintext despite password")
	}
	// Read path decrypts transparently while key is loaded.
	e, err := s.GetEntry(id)
	if err != nil {
		t.Fatal(err)
	}
	if e.Text != "secret 密码 text" {
		t.Fatalf("decrypted text mismatch: %q", e.Text)
	}

	// Simulate restart: new Store has salt but no key -> locked.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !s2.IsLocked() {
		t.Fatal("fresh store should be locked")
	}
	if _, err := s2.GetEntry(id); err != ErrLocked {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	// Wrong password rejected.
	if err := s2.SetPassword("wrong"); err == nil {
		t.Fatal("wrong password should be rejected")
	}
	// Correct password unlocks (idempotent set_db_password).
	if err := s2.SetPassword("s3cr3t"); err != nil {
		t.Fatal(err)
	}
	if s2.IsLocked() {
		t.Fatal("should be unlocked now")
	}
	e, err = s2.GetEntry(id)
	if err != nil || e.Text != "secret 密码 text" {
		t.Fatalf("unlock read failed: %v %q", err, e.Text)
	}
	// Decrypt back to plaintext.
	if err := s2.SetPassword(""); err != nil {
		t.Fatal(err)
	}
	st, _ = s2.GetSettings()
	if st.DBPasswordSet {
		t.Fatal("db_password_set should be false after decrypt")
	}
	if err := s2.db.QueryRow(`SELECT text FROM entries WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "secret 密码 text" {
		t.Fatalf("expected plaintext at rest, got %q", raw)
	}
}
