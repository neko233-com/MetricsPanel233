package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/argon2"
)

// Bound memory across catalogs and concurrent HTTP requests. Argon2 work is
// cancellable while queued; no password work is performed while a DB is locked.
var passwordWork = make(chan struct{}, 1)

const passwordFormat = "argon2id-v19-m65536-t3-p4"
const sessionCredentialPrefix = "mp233_session_"
const serviceCredentialPrefix = "mp233_sa_"

func passwordKey(ctx context.Context, password string, salt []byte) ([]byte, error) {
	if len(password) > 1024 || len(salt) != 16 {
		return nil, ErrUnauthenticated
	}
	select {
	case passwordWork <- struct{}{}:
		defer func() { <-passwordWork }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32)
	if err := ctx.Err(); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

func makePassword(ctx context.Context, password string) ([]byte, []byte, error) {
	if len(password) < 8 || len(password) > 1024 {
		return nil, nil, errors.New("password must contain 8 to 1024 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	key, err := passwordKey(ctx, password, salt)
	return salt, key, err
}

func verifyPassword(ctx context.Context, password string, salt, expected []byte, format string) (bool, error) {
	valid := len(salt) == 16 && len(expected) == 32 && format == passwordFormat
	if !valid {
		// Unknown accounts and damaged credentials still perform the same bounded
		// key derivation, without accepting an attacker-controlled hash policy.
		salt = make([]byte, 16)
		expected = make([]byte, 32)
	}
	key, err := passwordKey(ctx, password, salt)
	if err != nil {
		return false, err
	}
	defer clear(key)
	return subtle.ConstantTimeCompare(key, expected) == 1 && valid, nil
}

func newOpaqueCredential(prefix string) (string, []byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, err
	}
	key := prefix + base64.RawURLEncoding.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(key))
	return key, digest[:], nil
}

func credentialDigest(key, prefix string) ([]byte, error) {
	if len(key) != len(prefix)+43 || key[:len(prefix)] != prefix {
		return nil, ErrUnauthenticated
	}
	random, err := base64.RawURLEncoding.DecodeString(key[len(prefix):])
	if err != nil || len(random) != 32 || base64.RawURLEncoding.EncodeToString(random) != key[len(prefix):] {
		return nil, ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(key))
	return digest[:], nil
}
