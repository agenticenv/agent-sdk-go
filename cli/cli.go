package main

import (
	"fmt"
	"os"

	"github.com/agenticenv/agent-sdk-go/cli/internal/command"
	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	"github.com/alecthomas/kong"
	kongyaml "github.com/alecthomas/kong-yaml"
)

// CLI is the Kong root for agctl.
type CLI struct {
	ConfigPath  string           `name:"config" short:"c" help:"Path to config YAML (partial override of XDG config)." env:"AGCTL_CONFIG"`
	VersionFlag kong.VersionFlag `name:"version" short:"v" help:"Print version and exit."`

	Chat    command.ChatCmd    `cmd:"" help:"Interactive chat session."`
	Run     command.RunCmd     `cmd:"" help:"Run a single prompt and exit."`
	Version command.VersionCmd `cmd:"" name:"version" help:"Print version."`
	Config  command.ConfigCmd  `cmd:"" help:"Show or edit configuration."`
}

// Execute parses os.Args and runs the resolved agctl subcommand.
// version and embeddedConfig are injected from main (ldflags version + embedded YAML).
func Execute(version string, embeddedConfig []byte) error {
	var cli CLI

	opts := []kong.Option{
		kong.Name("agctl"),
		kong.Description("agctl — run and manage AI agents interactively or as one-shot commands."),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true, Tree: true}),
		kong.Vars{"version": version},
		kong.DefaultEnvars("AGCTL"),
	}
	// kong-yaml: seed flag defaults from the topmost existing file (explicit, else XDG).
	if path := kongYAMLPath(); path != "" {
		opts = append(opts, kong.Configuration(kongyaml.Loader, path))
	}

	parser, err := kong.New(&cli, opts...)
	if err != nil {
		return err
	}

	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"--help"}
	}

	ctx, err := parser.Parse(args)
	parser.FatalIfErrorf(err)

	cfg, err := config.LoadConfig(cli.ConfigPath, embeddedConfig)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx.Bind(cfg)
	ctx.Bind(command.Version(version))
	ctx.Bind(command.ConfigPathFlag(cli.ConfigPath))
	ctx.Bind(command.ConfigSample(embeddedConfig))
	return ctx.Run()
}

// kongYAMLPath picks a single existing file for Kong flag defaults (not the full merge stack).
func kongYAMLPath() string {
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "--config" || os.Args[i] == "-c" {
			p := os.Args[i+1]
			if config.FileExists(p) {
				return p
			}
			return ""
		}
	}
	if p := config.ExplicitConfigPath(""); p != "" && config.FileExists(p) {
		return p
	}
	if p := config.XDGConfigPath(); config.FileExists(p) {
		return p
	}
	return ""
}
