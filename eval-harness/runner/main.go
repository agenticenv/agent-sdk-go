package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"

	"github.com/agenticenv/agent-sdk-go/eval-harness/runner/setup"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (default: runner/config.yaml)")
	prompt := flag.String("prompt", "", "override user_prompt from config")
	runtimeFlag := flag.String("runtime", "", "override runtime: local, temporal, or restate")
	toolCount := flag.Int("tools", 0, "override agent.tool_count (0 = use config)")
	memoryStoreMode := flag.String("memory-store-mode", "", "override memory.store_mode: ondemand or always")
	memoryScenario := flag.Bool("memory", false, "enable memory store_recall scenario from config")
	flag.Parse()

	fileCfg, err := setup.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	runCfg := fileCfg.Config()
	if *prompt != "" {
		runCfg.UserPrompt = *prompt
	} else if args := flag.Args(); len(args) > 0 && args[0] != "" {
		// Promptfoo exec provider passes the rendered prompt as the first positional arg.
		runCfg.UserPrompt = args[0]
	}
	if *runtimeFlag != "" {
		runCfg.Runtime = setup.Runtime(*runtimeFlag)
	}
	if *toolCount > 0 {
		runCfg.ToolCount = *toolCount
	}
	if *memoryStoreMode != "" {
		mode, err := setup.ParseMemoryStoreMode(*memoryStoreMode)
		if err != nil {
			log.Fatalf("memory store mode: %v", err)
		}
		runCfg.Memory.StoreMode = mode
	}
	if *memoryScenario {
		runCfg.Memory.Enabled = true
		runCfg.ToolCount = 0
	}

	outcome, err := Run(context.Background(), runCfg)
	if err != nil {
		log.Fatalf("eval run failed: %v", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(OutputFromResult(outcome)); err != nil {
		log.Fatalf("encode result: %v", err)
	}
}
