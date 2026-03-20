package currenttime

import (
	"context"
	"time"

	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools"
)

var _ interfaces.Tool = (*CurrentTime)(nil)

// CurrentTime returns the current date and time. Useful when the user asks "what time is it" or "what's the date".
type CurrentTime struct{}

// New returns a new CurrentTime tool.
func New() *CurrentTime {
	return &CurrentTime{}
}

func (*CurrentTime) Name() string { return "current_time" }

func (*CurrentTime) Description() string {
	return "(Demo) Returns current date and time in ISO8601 (UTC). Use when the user asks about time or date."
}

func (*CurrentTime) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"timezone": tools.ParamString("Optional timezone (e.g. America/New_York). Defaults to UTC if empty."),
		},
	)
}

func (*CurrentTime) Execute(ctx context.Context, args map[string]any) (any, error) {
	tz := "UTC"
	if tzStr, ok := args["timezone"].(string); ok && tzStr != "" {
		tz = tzStr
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format(time.RFC3339), nil
}
