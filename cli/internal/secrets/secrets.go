// Package secrets resolves sensitive values (e.g. LLM API keys) with the
// environment taking precedence over config-file values. The lookup is kept
// behind a small interface so a future OS keychain backend can be added
// without touching call sites.
package secrets

import "os"

// Resolver looks up a secret by its environment variable key.
type Resolver interface {
	Lookup(key string) (string, bool)
}

// EnvResolver reads secrets from the process environment.
type EnvResolver struct{}

func (EnvResolver) Lookup(key string) (string, bool) {
	v := os.Getenv(key)
	if v == "" {
		return "", false
	}
	return v, true
}

func defaultResolver() Resolver {
	return EnvResolver{}
}

// ResolveAPIKey returns the LLM API key from AGCTL_LLM_APIKEY if set,
// otherwise falls back to fileValue (the value loaded from the config file).
func ResolveAPIKey(fileValue string) string {
	if v, ok := defaultResolver().Lookup("AGCTL_LLM_APIKEY"); ok {
		return v
	}
	return fileValue
}
