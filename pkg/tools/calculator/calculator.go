package calculator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/vvsynapse/temporal-agents-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agents-go/pkg/tools"
)

var _ interfaces.Tool = (*Calculator)(nil)

// Calculator performs basic arithmetic. Use when the user needs to compute numbers.
type Calculator struct{}

// New returns a new Calculator tool.
func New() *Calculator {
	return &Calculator{}
}

func (*Calculator) Name() string { return "calculator" }

func (*Calculator) Description() string {
	return "Performs basic arithmetic: add, subtract, multiply, divide. Use when the user needs to compute or evaluate mathematical expressions."
}

func (*Calculator) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"operation": tools.ParamEnum("The arithmetic operation", "add", "subtract", "multiply", "divide"),
			"a":         tools.ParamNumber("First number"),
			"b":         tools.ParamNumber("Second number"),
		},
		"operation", "a", "b",
	)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func (*Calculator) Execute(ctx context.Context, args map[string]any) (any, error) {
	op, _ := args["operation"].(string)
	a, okA := toFloat(args["a"])
	b, okB := toFloat(args["b"])
	if !okA || !okB {
		return nil, fmt.Errorf("invalid numbers: a=%v, b=%v", args["a"], args["b"])
	}
	var result float64
	switch op {
	case "add":
		result = a + b
	case "subtract":
		result = a - b
	case "multiply":
		result = a * b
	case "divide":
		if b == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		result = a / b
	default:
		return nil, fmt.Errorf("unknown operation: %s", op)
	}
	if math.Trunc(result) == result {
		return int64(result), nil
	}
	return result, nil
}
