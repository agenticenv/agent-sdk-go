package random

import (
	"context"
	"fmt"
	"math/rand"

	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools"
)

var _ interfaces.Tool = (*Random)(nil)

// Random generates a random integer in a range. Useful for games, sampling, or when the user asks for randomness.
type Random struct{}

// New returns a new Random tool.
func New() *Random {
	return &Random{}
}

func (*Random) Name() string { return "random" }

func (*Random) Description() string {
	return "(Demo) Generates a random integer between min and max. Use for testing or dice roll."
}

func (*Random) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"min": tools.ParamInteger("Minimum value (inclusive)"),
			"max": tools.ParamInteger("Maximum value (inclusive)"),
		},
		"min", "max",
	)
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

func (*Random) Execute(ctx context.Context, args map[string]any) (any, error) {
	min, okMin := toInt(args["min"])
	max, okMax := toInt(args["max"])
	if !okMin || !okMax {
		return nil, fmt.Errorf("invalid range: min=%v, max=%v", args["min"], args["max"])
	}
	if min > max {
		return nil, fmt.Errorf("min must be <= max")
	}
	return rand.Intn(max-min+1) + min, nil
}
