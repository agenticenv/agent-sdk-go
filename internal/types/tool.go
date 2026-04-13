package types

import "encoding/json"

// ToolSpec is the schema sent to the LLM for tool selection. Convert from Tool via ToolToSpec.
type ToolSpec struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

type JSONSchema map[string]any

func (s JSONSchema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any(s))
}
