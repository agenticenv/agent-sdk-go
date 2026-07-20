package agent

import (
	"fmt"

	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/random"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/search"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/weather"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/wikipedia"
)

// builtinTool is a named constructor for a built-in CLI tool.
type builtinTool struct {
	name string
	new  func() interfaces.Tool
}

// builtinCatalog is the dynamic source of built-in tools. Registration walks
// this list and only adds entries enabled in config.
var builtinCatalog = []builtinTool{
	{config.ToolEcho, func() interfaces.Tool { return echo.New() }},
	{config.ToolCurrentTime, func() interfaces.Tool { return currenttime.New() }},
	{config.ToolRandom, func() interfaces.Tool { return random.New() }},
	{config.ToolCalculator, func() interfaces.Tool { return calculator.New() }},
	{config.ToolWeather, func() interfaces.Tool { return weather.New() }},
	{config.ToolWikipedia, func() interfaces.Tool { return wikipedia.New() }},
	{config.ToolSearch, func() interfaces.Tool { return search.New() }},
}

// registerConfiguredTools builds the tool registry from config toggles.
func registerConfiguredTools(reg sdkagent.ToolRegistry, cfg *config.Config) error {
	if reg == nil {
		return fmt.Errorf("tool registry is nil")
	}
	tools := make([]interfaces.Tool, 0, len(builtinCatalog))
	for _, entry := range builtinCatalog {
		if !cfg.ToolEnabled(entry.name) {
			continue
		}
		tools = append(tools, entry.new())
	}
	if len(tools) == 0 {
		return nil
	}
	if err := sdkagent.RegisterTools(reg, tools...); err != nil {
		return err
	}
	return nil
}
