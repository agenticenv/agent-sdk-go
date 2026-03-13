package llm

type LLMType string

const (
	LLMTypeOpenAI    LLMType = "openai"
	LLMTypeAnthropic LLMType = "anthropic"
	LLMTypeGemini    LLMType = "gemini"
)

type LLMConfig struct {
	Type    LLMType
	APIKey  string
	Model   string
	BaseURL string
}
