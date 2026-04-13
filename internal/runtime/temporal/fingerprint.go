package temporal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

const agentFingerprintVersion = 1

// AgentFingerprintPayload is the JSON-serializable snapshot hashed by [ComputeAgentFingerprint].
// Agent callers and Temporal workers must supply the same fields so client and worker digests match.
type AgentFingerprintPayload struct {
	Version int `json:"v"`

	Name           string `json:"name"`
	Description    string `json:"description"`
	SystemPrompt   string `json:"system_prompt"`
	ResponseFormat *struct {
		Type   string         `json:"type"`
		Name   string         `json:"name,omitempty"`
		Schema map[string]any `json:"schema,omitempty"`
	} `json:"response_format,omitempty"`

	ToolNames []string `json:"tool_names"`

	// PolicyFingerprint is an opaque string from pkg/agent (same on caller and worker Temporal config).
	PolicyFingerprint string `json:"policy_fp"`

	// MCPFingerprint is the pkg/agent MCP wiring digest over transports (no secrets), timeouts, filters,
	// and extra MCP client names. Tool names already appear in ToolNames; this catches same tools
	// pointing at a different endpoint or policy. Omitted when empty.
	MCPFingerprint string `json:"mcp_fingerprint,omitempty"`

	Sampling *sdkruntime.LLMSampling `json:"sampling,omitempty"`

	SessionSize int `json:"session_size"`

	MaxIterations     int   `json:"max_iterations"`
	TimeoutNs         int64 `json:"timeout_ns"`
	ApprovalTimeoutNs int64 `json:"approval_timeout_ns"`
}

// ComputeAgentFingerprint returns a stable SHA-256 hex digest of the payload (identity, prompts, tools,
// sampling, limits, policy, optional MCP wiring). Use the same toolPolicyFingerprint and MCP fingerprint
// inputs from pkg/agent on both the process that issues runs and the worker process.
func ComputeAgentFingerprint(m AgentFingerprintPayload) string {
	m.Version = agentFingerprintVersion
	if m.ToolNames != nil {
		sort.Strings(m.ToolNames)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildAgentFingerprintPayload builds a payload from spec and execution fields shared by caller and worker.
func BuildAgentFingerprintPayload(
	spec sdkruntime.AgentSpec,
	toolNames []string,
	policyFingerprint string,
	sampling *sdkruntime.LLMSampling,
	sessionSize int,
	limits sdkruntime.AgentLimits,
	mcpFingerprint string,
) AgentFingerprintPayload {
	names := append([]string(nil), toolNames...)
	sort.Strings(names)

	m := AgentFingerprintPayload{
		Name:              spec.Name,
		Description:       spec.Description,
		SystemPrompt:      spec.SystemPrompt,
		ToolNames:         names,
		PolicyFingerprint: policyFingerprint,
		MCPFingerprint:    mcpFingerprint,
		Sampling:          cloneLLMSampling(sampling),
		SessionSize:       sessionSize,
		MaxIterations:     limits.MaxIterations,
		TimeoutNs:         limits.Timeout.Nanoseconds(),
		ApprovalTimeoutNs: limits.ApprovalTimeout.Nanoseconds(),
	}
	if spec.ResponseFormat != nil {
		rf := spec.ResponseFormat
		m.ResponseFormat = &struct {
			Type   string         `json:"type"`
			Name   string         `json:"name,omitempty"`
			Schema map[string]any `json:"schema,omitempty"`
		}{
			Type: string(rf.Type),
			Name: rf.Name,
		}
		if rf.Schema != nil {
			m.ResponseFormat.Schema = map[string]any(rf.Schema)
		}
	}
	return m
}

func cloneLLMSampling(s *sdkruntime.LLMSampling) *sdkruntime.LLMSampling {
	if s == nil {
		return nil
	}
	c := *s
	if s.Temperature != nil {
		t := *s.Temperature
		c.Temperature = &t
	}
	if s.TopP != nil {
		p := *s.TopP
		c.TopP = &p
	}
	if s.TopK != nil {
		k := *s.TopK
		c.TopK = &k
	}
	if s.Reasoning != nil {
		r := *s.Reasoning
		c.Reasoning = &r
	}
	return &c
}

// ToolNamesFromTools returns sorted tool names for fingerprinting.
func ToolNamesFromTools(tools []interfaces.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		names = append(names, t.Name())
	}
	sort.Strings(names)
	return names
}

func computeAgentFingerprintFromRuntimeConfig(c *TemporalRuntimeConfig) string {
	mat := BuildAgentFingerprintPayload(
		c.AgentSpec,
		ToolNamesFromTools(c.AgentExecution.Tools.Tools),
		c.PolicyFingerprint,
		c.AgentExecution.LLM.Sampling,
		c.AgentExecution.Session.ConversationSize,
		c.AgentExecution.Limits,
		c.MCPFingerprint,
	)
	return ComputeAgentFingerprint(mat)
}
