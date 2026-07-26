package base

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	ifmocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
)

// --- FindToolByName ---

func TestFindToolByName_Found(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("my-tool").AnyTimes()

	got, ok := FindToolByName([]interfaces.Tool{tool}, "my-tool")
	require.True(t, ok)
	require.Equal(t, tool, got)
}

func TestFindToolByName_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("other").AnyTimes()

	_, ok := FindToolByName([]interfaces.Tool{tool}, "missing")
	require.False(t, ok)
}

func TestFindToolByName_EmptyList(t *testing.T) {
	_, ok := FindToolByName(nil, "any")
	require.False(t, ok)
}

// --- FormatRetrieverDocs ---

func TestFormatRetrieverDocs_Empty(t *testing.T) {
	require.Equal(t, "", FormatRetrieverDocs(nil))
	require.Equal(t, "", FormatRetrieverDocs([]interfaces.Document{}))
}

func TestFormatRetrieverDocs_SingleDoc(t *testing.T) {
	docs := []interfaces.Document{{Content: "hello", Source: "src", Score: 0.9}}
	got := FormatRetrieverDocs(docs)
	require.Contains(t, got, "[1]")
	require.Contains(t, got, "hello")
	require.Contains(t, got, "src")
	require.Contains(t, got, "0.90")
}

func TestFormatRetrieverDocs_MultipleDocs(t *testing.T) {
	docs := []interfaces.Document{
		{Content: "first", Source: "s1", Score: 0.8},
		{Content: "second", Source: "s2", Score: 0.6},
	}
	got := FormatRetrieverDocs(docs)
	require.Contains(t, got, "[1]")
	require.Contains(t, got, "[2]")
	require.Contains(t, got, "first")
	require.Contains(t, got, "second")
}

// --- MergeLLMUsage ---

func TestMergeLLMUsage_BothNil(t *testing.T) {
	require.Nil(t, MergeLLMUsage(nil, nil))
}

func TestMergeLLMUsage_AddNil(t *testing.T) {
	acc := &interfaces.LLMUsage{PromptTokens: 5}
	got := MergeLLMUsage(acc, nil)
	require.Equal(t, acc, got)
}

func TestMergeLLMUsage_AccNil(t *testing.T) {
	add := &interfaces.LLMUsage{PromptTokens: 3, TotalTokens: 3}
	got := MergeLLMUsage(nil, add)
	require.EqualValues(t, 3, got.PromptTokens)
	require.NotSame(t, add, got) // must be a copy
}

func TestMergeLLMUsage_BothNonNil(t *testing.T) {
	acc := &interfaces.LLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	add := &interfaces.LLMUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
	got := MergeLLMUsage(acc, add)
	require.EqualValues(t, 12, got.PromptTokens)
	require.EqualValues(t, 8, got.CompletionTokens)
	require.EqualValues(t, 20, got.TotalTokens)
}

// --- CloneLLMUsage ---

func TestCloneLLMUsage_Nil(t *testing.T) {
	require.Nil(t, CloneLLMUsage(nil))
}

func TestCloneLLMUsage_MutationIsolation(t *testing.T) {
	orig := &interfaces.LLMUsage{PromptTokens: 7}
	clone := CloneLLMUsage(orig)
	require.Equal(t, orig.PromptTokens, clone.PromptTokens)
	clone.PromptTokens = 99
	require.EqualValues(t, 7, orig.PromptTokens) // original unchanged
}

// --- ApplyLLMSampling ---

func TestApplyLLMSampling_Nil(t *testing.T) {
	req := &interfaces.LLMRequest{}
	ApplyLLMSampling(nil, req) // must not panic
	require.Nil(t, req.Temperature)
}

func TestApplyLLMSampling_Temperature(t *testing.T) {
	temp := 0.7
	req := &interfaces.LLMRequest{}
	ApplyLLMSampling(&types.LLMSampling{Temperature: &temp}, req)
	require.NotNil(t, req.Temperature)
	require.InDelta(t, 0.7, *req.Temperature, 0.001)
}

func TestApplyLLMSampling_AllFields(t *testing.T) {
	temp := 0.5
	topP := 0.9
	topK := 40
	req := &interfaces.LLMRequest{}
	ApplyLLMSampling(&types.LLMSampling{
		Temperature: &temp,
		MaxTokens:   512,
		TopP:        &topP,
		TopK:        &topK,
	}, req)
	require.InDelta(t, 0.5, *req.Temperature, 0.001)
	require.Equal(t, 512, req.MaxTokens)
	require.InDelta(t, 0.9, *req.TopP, 0.001)
	require.Equal(t, 40, *req.TopK)
}

func TestApplyLLMSampling_MaxTokensZeroNotApplied(t *testing.T) {
	req := &interfaces.LLMRequest{MaxTokens: 100}
	ApplyLLMSampling(&types.LLMSampling{MaxTokens: 0}, req)
	require.Equal(t, 100, req.MaxTokens) // unchanged when zero
}
