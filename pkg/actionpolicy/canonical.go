package actionpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// CanonicalJSON encodes v with encoding/json, HTML escaping disabled, and the
// trailing newline removed. Map keys are sorted by encoding/json. Time values
// must already be UTC so the timestamp is RFC3339Nano with a Z suffix.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("actionpolicy: canonical json: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// DigestBytes is the lowercase hex SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DigestText is the lowercase hex SHA-256 of the raw string.
func DigestText(s string) string {
	return DigestBytes([]byte(s))
}

// DigestJSON is the lowercase hex SHA-256 of [CanonicalJSON] of v.
func DigestJSON(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}
