// Package tools provides ToolRegistry implementation and schema helpers for building
// JSON Schema parameter definitions in a type-safe way.
//
// Example:
//
//	func (t *SearchTool) Parameters() interfaces.JSONSchema {
//	    return tools.Params(
//	        map[string]interfaces.JSONSchema{
//	            "query": tools.ParamString("Search query"),
//	            "limit": tools.ParamInteger("Max results (default 10)"),
//	        },
//	        "query",
//	    )
//	}
package tools

import (
	"github.com/vvsynapse/temporal-agents-go/pkg/interfaces"
)

// ParamString returns a JSON Schema for a string parameter.
func ParamString(description string) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "string",
		"description": description,
	}
}

// ParamInteger returns a JSON Schema for an integer parameter.
func ParamInteger(description string) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "integer",
		"description": description,
	}
}

// ParamNumber returns a JSON Schema for a number parameter.
func ParamNumber(description string) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "number",
		"description": description,
	}
}

// ParamBool returns a JSON Schema for a boolean parameter.
func ParamBool(description string) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "boolean",
		"description": description,
	}
}

// ParamEnum returns a JSON Schema for an enum parameter.
func ParamEnum(description string, values ...any) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "string",
		"description": description,
		"enum":        values,
	}
}

// ParamArray returns a JSON Schema for an array parameter.
func ParamArray(description string, items interfaces.JSONSchema) interfaces.JSONSchema {
	return interfaces.JSONSchema{
		"type":        "array",
		"description": description,
		"items":       items,
	}
}

// Params builds the full parameters schema for a tool. Properties is a map of parameter name to its schema.
// Required lists parameter names that must be provided.
func Params(properties map[string]interfaces.JSONSchema, required ...string) interfaces.JSONSchema {
	schema := interfaces.JSONSchema{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
