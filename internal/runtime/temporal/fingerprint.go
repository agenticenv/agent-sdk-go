package temporal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
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

	// A2AFingerprint is the pkg/agent A2A wiring digest over server URLs, auth type (no secrets),
	// header keys, timeouts, skill filters, and extra A2A client names. Omitted when empty.
	A2AFingerprint string `json:"a2a_fingerprint,omitempty"`

	// ObservabilityFingerprint is the pkg/agent digest from [WithObservabilityConfig] (OTLP endpoint
	// and trace/metrics disable flags). Omitted when empty.
	ObservabilityFingerprint string `json:"observability_fingerprint,omitempty"`

	// AgentMode is the execution mode (e.g. interactive vs autonomous); must match pkg/agent WithAgentMode on caller and worker.
	AgentMode string `json:"agent_mode"`

	// AgentToolExecutionMode is the tool execution mode (e.g. sequential vs parallel); must match pkg/agent WithAgentToolExecutionMode on caller and worker.
	AgentToolExecutionMode string `json:"agent_tool_execution_mode"`

	// RetrieverFingerprint is the pkg/agent digest of retriever mode and registered retriever names.
	// Omitted when empty. Must match pkg/agent [retrieverConfigFingerprint] on caller and worker.
	RetrieverFingerprint string `json:"retriever_fingerprint,omitempty"`

	// HooksFingerprint is the pkg/agent digest of registered hook group names (sorted).
	// Omitted when empty. Must match pkg/agent [hookGroupsFingerprint] on caller and worker.
	HooksFingerprint string `json:"hooks_fingerprint,omitempty"`

	Sampling *sdkruntime.LLMSampling `json:"sampling,omitempty"`

	SessionSize int `json:"session_size"`

	MaxIterations     int   `json:"max_iterations"`
	TimeoutNs         int64 `json:"timeout_ns"`
	ApprovalTimeoutNs int64 `json:"approval_timeout_ns"`

	// BudgetMaxTokens, BudgetMaxCostUSD, BudgetOnExceeded capture the per-run budget
	// configuration for fingerprinting. Zero values mean no budget enforcement.
	BudgetMaxTokens            int64   `json:"budget_max_tokens,omitempty"`
	BudgetMaxCostUSD           float64 `json:"budget_max_cost_usd,omitempty"`
	BudgetOnExceeded           string  `json:"budget_on_exceeded,omitempty"`
	BudgetApprovalExtraTokens  int64   `json:"budget_approval_extra_tokens,omitempty"`
	BudgetApprovalExtraCostUSD float64 `json:"budget_approval_extra_cost_usd,omitempty"`
}

// ComputeAgentFingerprint returns a stable SHA-256 hex digest of the payload (identity, prompts, tools,
// sampling, limits, policy, optional MCP, A2A, and observability wiring). Use the same digests
// from pkg/agent on both the process that issues runs and the worker process.
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
	a2aFingerprint string,
	observabilityFingerprint string,
	agentMode string,
	agentToolExecutionMode types.AgentToolExecutionMode,
	retrieverFingerprint string,
	hooksFingerprint string,
) AgentFingerprintPayload {
	names := append([]string(nil), toolNames...)
	sort.Strings(names)
	mode := agentMode
	if mode == "" {
		mode = string(types.AgentModeInteractive)
	}
	toolExecutionMode := agentToolExecutionMode
	if toolExecutionMode == "" {
		toolExecutionMode = types.AgentToolExecutionModeParallel
	}
	m := AgentFingerprintPayload{
		Name:                     spec.Name,
		Description:              spec.Description,
		SystemPrompt:             spec.SystemPrompt,
		ToolNames:                names,
		PolicyFingerprint:        policyFingerprint,
		MCPFingerprint:           mcpFingerprint,
		A2AFingerprint:           a2aFingerprint,
		ObservabilityFingerprint: observabilityFingerprint,
		AgentMode:                mode,
		AgentToolExecutionMode:   string(toolExecutionMode),
		RetrieverFingerprint:     retrieverFingerprint,
		HooksFingerprint:         hooksFingerprint,
		Sampling:                 cloneLLMSampling(sampling),
		SessionSize:              sessionSize,
		MaxIterations:            limits.MaxIterations,
		TimeoutNs:                limits.Timeout.Nanoseconds(),
		ApprovalTimeoutNs:        limits.ApprovalTimeout.Nanoseconds(),
	}
	if limits.Budget != nil {
		m.BudgetMaxTokens = limits.Budget.MaxTokens
		m.BudgetMaxCostUSD = limits.Budget.MaxCostUSD
		m.BudgetOnExceeded = string(limits.Budget.OnExceeded)
		m.BudgetApprovalExtraTokens = limits.Budget.ApprovalExtraTokens
		m.BudgetApprovalExtraCostUSD = limits.Budget.ApprovalExtraCostUSD
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

func computeAgentFingerprintFromRuntime(rt *TemporalRuntime, tools []interfaces.Tool) string {
	if rt == nil {
		return ""
	}
	mat := BuildAgentFingerprintPayload(
		rt.AgentSpec,
		ToolNamesFromTools(tools),
		rt.policyFingerprint,
		rt.AgentConfig.LLM.Sampling,
		rt.AgentConfig.Session.ConversationSize,
		rt.AgentConfig.Limits,
		rt.mcpFingerprint,
		rt.a2aFingerprint,
		rt.observabilityFingerprint,
		rt.agentMode,
		rt.ToolExecutionMode,
		rt.retrieverFingerprint,
		rt.hooksFingerprint,
	)
	return ComputeAgentFingerprint(mat)
}
