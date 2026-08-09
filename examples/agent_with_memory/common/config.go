// Package common holds shared configuration and agent options for the agent_with_memory examples.
package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/memory"
)

// Settings holds env-driven values shared by the weaviate and pgvector example entry points.
type Settings struct {
	UserID         string
	StoreMode      memory.StoreMode
	RecallEnabled  bool
	RecallLimit    int
	RecallMinScore float32

	// Weaviate
	WeaviateHost        string
	WeaviateScheme      string
	WeaviateClass       string
	WeaviateMemoryClass string

	// PostgreSQL / pgvector
	PGDSN            string
	PGMemoryTable    string
	PGEmbeddingCol   string
	EmbeddingModel   string
	EmbeddingBaseURL string
	EmbeddingAPIKey  string
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// ParseStoreMode reads MEMORY_STORE_MODE (always | ondemand). Empty defaults to ondemand.
func ParseStoreMode(raw string) (memory.StoreMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "ondemand", "on-demand", "on_demand":
		return memory.StoreModeOnDemand, nil
	case "always":
		return memory.StoreModeAlways, nil
	default:
		return "", fmt.Errorf("MEMORY_STORE_MODE must be always or ondemand, got %q", raw)
	}
}

// StoreModeHint returns a one-line demo hint for the configured store mode.
func StoreModeHint(mode memory.StoreMode) string {
	if mode == memory.StoreModeAlways {
		return "no args runs run-end store (run 1) then recall (run 2); pass one prompt for a single run"
	}
	return "no args runs save_memory (run 1) then recall (run 2); pass one prompt for a single run"
}

// LoadSettings reads memory example env vars. LLM and Temporal vars come from examples/config.LoadFromEnv.
func LoadSettings() (*Settings, error) {
	storeMode, err := ParseStoreMode(getEnv("MEMORY_STORE_MODE", "ondemand"))
	if err != nil {
		return nil, err
	}
	s := &Settings{
		UserID:              getEnv("MEMORY_USER_ID", "demo-user"),
		StoreMode:           storeMode,
		RecallEnabled:       getEnvBool("MEMORY_RECALL_ENABLED", true),
		RecallLimit:         getEnvInt("MEMORY_RECALL_LIMIT", 10),
		RecallMinScore:      float32(getEnvFloat("MEMORY_RECALL_MIN_SCORE", 0.35)),
		WeaviateHost:        getEnv("WEAVIATE_HOST", "localhost:8081"),
		WeaviateScheme:      getEnv("WEAVIATE_SCHEME", "http"),
		WeaviateClass:       getEnv("WEAVIATE_CLASS", "Document"),
		WeaviateMemoryClass: getEnv("WEAVIATE_MEMORY_CLASS", "AgentMemory"),
		PGDSN:               strings.TrimSpace(getEnv("PGVECTOR_DSN", "")),
		PGMemoryTable:       getEnv("PGVECTOR_MEMORY_TABLE", "agent_memories"),
		PGEmbeddingCol:      getEnv("PGVECTOR_EMBEDDING_COL", "embedding"),
		EmbeddingModel:      getEnv("EMBEDDING_OPENAI_MODEL", "text-embedding-3-small"),
		EmbeddingBaseURL:    strings.TrimSpace(getEnv("EMBEDDING_OPENAI_BASEURL", "")),
		EmbeddingAPIKey:     strings.TrimSpace(getEnv("EMBEDDING_OPENAI_APIKEY", "")),
	}
	if s.EmbeddingBaseURL == "" {
		s.EmbeddingBaseURL = strings.TrimSpace(getEnv("LLM_BASEURL", "https://api.openai.com/v1"))
	}
	if s.RecallLimit <= 0 {
		return nil, fmt.Errorf("MEMORY_RECALL_LIMIT must be positive, got %d", s.RecallLimit)
	}
	return s, nil
}
