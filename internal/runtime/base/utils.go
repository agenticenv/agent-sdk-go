package base

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// SubAgentQuery extracts the query string from a sub-agent tool call's args map.
func SubAgentQuery(args map[string]any) string {
	if args == nil {
		return ""
	}
	q, _ := args[runtime.SubAgentToolParamQuery].(string)
	return q
}

// SubAgentScope derives memory scope for a delegated sub-agent from the parent run scope.
func SubAgentScope(parent interfaces.MemoryScope, subAgentID string) interfaces.MemoryScope {
	subAgentID = strings.TrimSpace(subAgentID)
	scope := interfaces.MemoryScope{
		TenantID: parent.TenantID,
		UserID:   parent.UserID,
		AgentID:  subAgentID,
	}
	if parent.AgentID != "" || len(parent.Tags) > 0 {
		tags := make(map[string]string, len(parent.Tags)+1)
		for key, value := range parent.Tags {
			tags[key] = value
		}
		if parent.AgentID != "" {
			tags[scopeKeyParentAgentID] = parent.AgentID
		}
		scope.Tags = tags
	}
	return scope
}

// FindToolByName returns the first tool whose Name() matches toolName.
func FindToolByName(tools []interfaces.Tool, toolName string) (interfaces.Tool, bool) {
	for _, t := range tools {
		if t.Name() == toolName {
			return t, true
		}
	}
	return nil, false
}

// FormatRetrieverDocs formats a list of documents for injection into the LLM system prompt.
// Each entry is rendered as "[N] content\n(source: s, score: 0.XX)\n\n".
func FormatRetrieverDocs(docs []interfaces.Document) string {
	if len(docs) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, doc := range docs {
		fmt.Fprintf(&sb, types.RetrieverDocFormat, i+1, doc.Content, doc.Source, doc.Score)
	}
	return sb.String()
}

// MergeLLMUsage accumulates add into acc and returns the result.
// Either argument may be nil; when both are nil, nil is returned.
func MergeLLMUsage(acc, add *interfaces.LLMUsage) *interfaces.LLMUsage {
	if add == nil {
		return acc
	}
	if acc == nil {
		return CloneLLMUsage(add)
	}
	return &interfaces.LLMUsage{
		PromptTokens:       acc.PromptTokens + add.PromptTokens,
		CompletionTokens:   acc.CompletionTokens + add.CompletionTokens,
		TotalTokens:        acc.TotalTokens + add.TotalTokens,
		CachedPromptTokens: acc.CachedPromptTokens + add.CachedPromptTokens,
		ReasoningTokens:    acc.ReasoningTokens + add.ReasoningTokens,
	}
}

// CloneLLMUsage returns a shallow copy of u, or nil when u is nil.
func CloneLLMUsage(u *interfaces.LLMUsage) *interfaces.LLMUsage {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// ApplyLLMSampling copies non-zero sampling fields from s onto req.
// A nil sampling value is a no-op.
func ApplyLLMSampling(s *types.LLMSampling, req *interfaces.LLMRequest) {
	if s == nil {
		return
	}
	if s.Temperature != nil {
		req.Temperature = s.Temperature
	}
	if s.MaxTokens > 0 {
		req.MaxTokens = s.MaxTokens
	}
	if s.TopP != nil {
		req.TopP = s.TopP
	}
	if s.TopK != nil {
		req.TopK = s.TopK
	}
	if s.Reasoning != nil {
		r := *s.Reasoning
		req.Reasoning = &r
	}
}

func NewAgentTelemetry(startedAt time.Time) *types.AgentTelemetry {
	return &types.AgentTelemetry{
		Run: types.RunTelemetry{
			StartedAt:    startedAt,
			FinishReason: types.FinishReasonComplete,
		},
		Tools: types.ToolTelemetry{
			Breakdown:       make(map[string]int64),
			FailedBreakdown: make(map[string]int64),
		},
	}
}
