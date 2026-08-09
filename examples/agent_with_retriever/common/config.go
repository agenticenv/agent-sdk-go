// Package common holds shared configuration and agent options for the agent_with_retriever examples.
package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// Settings holds env-driven values shared by the weaviate and pgvector example entry points.
type Settings struct {
	// RetrieverMode is agentic, prefetch, or hybrid (see agent.WithRetrieverMode).
	RetrieverMode agent.RetrieverMode

	// Weaviate
	WeaviateHost          string
	WeaviateScheme        string
	WeaviateClass         string
	WeaviateRetrieverName string
	WeaviateContentField  string
	WeaviateSourceField   string
	WeaviateTopK          int
	WeaviateMinScore      float64

	// PostgreSQL / pgvector
	PGDSN            string
	PGTable          string
	PGContentCol     string
	PGSourceCol      string
	PGEmbeddingCol   string
	PGRetrieverName  string
	PGTopK           int
	PGMinScore       float64
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

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// LoadSettings reads retriever example env vars. LLM and Temporal vars come from examples/config.LoadFromEnv.
func LoadSettings() (*Settings, error) {
	mode, err := ParseRetrieverMode(strings.TrimSpace(getEnv("RETRIEVER_MODE", "agentic")))
	if err != nil {
		return nil, err
	}
	s := &Settings{
		RetrieverMode: mode,

		WeaviateHost:          getEnv("WEAVIATE_HOST", "localhost:8081"),
		WeaviateScheme:        getEnv("WEAVIATE_SCHEME", "http"),
		WeaviateClass:         getEnv("WEAVIATE_CLASS", "Document"),
		WeaviateRetrieverName: getEnv("WEAVIATE_RETRIEVER_NAME", "weaviate-kb"),
		WeaviateContentField:  getEnv("WEAVIATE_CONTENT_FIELD", "content"),
		WeaviateSourceField:   getEnv("WEAVIATE_SOURCE_FIELD", "source"),
		WeaviateTopK:          getEnvInt("WEAVIATE_TOP_K", 0),
		WeaviateMinScore:      getEnvFloat("WEAVIATE_MIN_SCORE", 0),

		PGDSN:           strings.TrimSpace(getEnv("PGVECTOR_DSN", "")),
		PGTable:         getEnv("PGVECTOR_TABLE", "documents"),
		PGContentCol:    getEnv("PGVECTOR_CONTENT_COL", "content"),
		PGSourceCol:     getEnv("PGVECTOR_SOURCE_COL", "source"),
		PGEmbeddingCol:  getEnv("PGVECTOR_EMBEDDING_COL", "embedding"),
		PGRetrieverName: getEnv("PGVECTOR_RETRIEVER_NAME", "pgvector-kb"),
		PGTopK:          getEnvInt("PGVECTOR_TOP_K", 0),
		// Example default 0.35 — sample KB often scores 0.3–0.6 per topic; 0.5 drops secondary docs on combined queries.
		PGMinScore:       getEnvFloat("PGVECTOR_MIN_SCORE", 0.35),
		EmbeddingModel:   getEnv("EMBEDDING_OPENAI_MODEL", "text-embedding-3-small"),
		EmbeddingBaseURL: strings.TrimSpace(getEnv("EMBEDDING_OPENAI_BASEURL", "")),
		EmbeddingAPIKey:  strings.TrimSpace(getEnv("EMBEDDING_OPENAI_APIKEY", "")),
	}
	if s.EmbeddingBaseURL == "" {
		s.EmbeddingBaseURL = strings.TrimSpace(getEnv("LLM_BASEURL", "https://api.openai.com/v1"))
	}
	return s, nil
}

// ParseRetrieverMode maps env text to agent.RetrieverMode.
func ParseRetrieverMode(raw string) (agent.RetrieverMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "agentic":
		return agent.RetrieverModeAgentic, nil
	case "prefetch":
		return agent.RetrieverModePrefetch, nil
	case "hybrid":
		return agent.RetrieverModeHybrid, nil
	default:
		return "", fmt.Errorf("retriever: unknown RETRIEVER_MODE %q (use agentic, prefetch, or hybrid)", raw)
	}
}

// ModeHint returns a short phrase describing how the current mode uses retrievers.
func ModeHint(mode agent.RetrieverMode) string {
	switch mode {
	case agent.RetrieverModePrefetch:
		return "context is prefetched before the first LLM call (no retriever tools)"
	case agent.RetrieverModeHybrid:
		return "context is prefetched and retriever tools remain available"
	default:
		return "the LLM may call retriever_* tools when it needs documents"
	}
}
