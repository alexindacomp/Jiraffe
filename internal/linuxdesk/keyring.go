//go:build linux

package linuxdesk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// Secret Service item that holds the token key.
const (
	keyringService = "Jiraffe"
	keyringUser    = "config-key"
)

// blobPrefix marks blobs sealed by Keyring (format version 1).
const blobPrefix = "jk1"

// Keyring implements config.Protector: the token is sealed with AES-256-GCM
// under a random key kept in the user's Secret Service (GNOME Keyring), so
// config.json alone does not reveal it — the equivalent of DPAPI on Windows.
type Keyring struct {
	mu  sync.Mutex
	key []byte

	// get / set reach the keyring (tests replace them).
	get func() (string, error)
	set func(string) error
}

// NewKeyring returns the protector backed by the session's Secret Service.
func NewKeyring() *Keyring {
	return &Keyring{
		get: func() (string, error) { return keyring.Get(keyringService, keyringUser) },
		set: func(v string) error { return keyring.Set(keyringService, keyringUser, v) },
	}
}

// loadKey returns the key, creating it in the keyring when create and it is
// missing.
func (k *Keyring) loadKey(create bool) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key != nil {
		return k.key, nil
	}
	s, err := k.get()
	switch {
	case errors.Is(err, keyring.ErrNotFound) && create:
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := k.set(base64.StdEncoding.EncodeToString(key)); err != nil {
			return nil, fmt.Errorf("cannot store the key in the keyring: %w", err)
		}
		k.key = key
		return key, nil
	case err != nil:
		return nil, fmt.Errorf("keyring: %w", err)
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(key) != 32 {
		return nil, errors.New("keyring: the stored key is malformed")
	}
	k.key = key
	return key, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Protect seals plain: "jk1" | nonce | ciphertext.
func (k *Keyring) Protect(plain []byte) ([]byte, error) {
	key, err := k.loadKey(true)
	if err != nil {
		return nil, err
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(blobPrefix), nonce...)
	return a.Seal(out, nonce, plain, []byte(blobPrefix)), nil
}

// Unprotect opens a blob made by Protect.
func (k *Keyring) Unprotect(blob []byte) ([]byte, error) {
	if len(blob) < len(blobPrefix) || string(blob[:len(blobPrefix)]) != blobPrefix {
		return nil, errors.New("the token was not sealed on this system")
	}
	key, err := k.loadKey(false)
	if err != nil {
		return nil, err
	}
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	rest := blob[len(blobPrefix):]
	if len(rest) < a.NonceSize() {
		return nil, errors.New("the sealed token is truncated")
	}
	plain, err := a.Open(nil, rest[:a.NonceSize()], rest[a.NonceSize():], []byte(blobPrefix))
	if err != nil {
		return nil, errors.New("the token cannot be decrypted (the keyring key changed)")
	}
	return plain, nil
}
