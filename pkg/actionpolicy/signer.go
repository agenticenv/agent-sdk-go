package actionpolicy

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"errors"
	"fmt"
)

// Signer signs the canonical receipt payload. KeyID is stored on each receipt.
type Signer interface {
	KeyID() string
	Algorithm() string
	Sign(ctx context.Context, payload []byte) ([]byte, error)
}

type ed25519Signer struct {
	keyID string
	key   ed25519.PrivateKey
}

// NewEd25519Signer returns a signer for key. key must be a 64-byte Ed25519 private key.
// The key is copied. keyID is the id verifiers pass to a [KeyResolver].
func NewEd25519Signer(keyID string, key ed25519.PrivateKey) (Signer, error) {
	if keyID == "" {
		return nil, errors.New("actionpolicy: key id is required")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("actionpolicy: ed25519 private key must be 64 bytes")
	}
	copied := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	copy(copied, key)
	return &ed25519Signer{keyID: keyID, key: copied}, nil
}

func (s *ed25519Signer) KeyID() string     { return s.keyID }
func (s *ed25519Signer) Algorithm() string { return AlgEd25519 }

func (s *ed25519Signer) Sign(_ context.Context, payload []byte) ([]byte, error) {
	return ed25519.Sign(s.key, payload), nil
}

// KeyResolver returns the public key for a receipt key id.
type KeyResolver interface {
	PublicKey(keyID string) (crypto.PublicKey, error)
}

type staticKeys struct {
	keys map[string]crypto.PublicKey
}

// StaticKeys returns a resolver for the given public keys.
// The map is copied. A nil map resolves no keys.
func StaticKeys(keys map[string]crypto.PublicKey) KeyResolver {
	copied := make(map[string]crypto.PublicKey, len(keys))
	for id, key := range keys {
		copied[id] = key
	}
	return staticKeys{keys: copied}
}

func (s staticKeys) PublicKey(keyID string) (crypto.PublicKey, error) {
	key, ok := s.keys[keyID]
	if !ok {
		return nil, fmt.Errorf("actionpolicy: unknown key %q", keyID)
	}
	return key, nil
}
