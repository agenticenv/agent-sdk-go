package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// ResponseFormat (interfaces.ResponseFormat + JSONSchema) is applied to every LLM call for this agent.
	rf := &interfaces.ResponseFormat{
		Type: interfaces.ResponseFormatJSON,
		Name: "FactAnswer",
		Schema: interfaces.JSONSchema{
			"type": "object",
			"properties": interfaces.JSONSchema{
				"answer": interfaces.JSONSchema{
					"type":        "string",
					"description": "Direct answer to the question",
				},
				"confidence": interfaces.JSONSchema{
					"type":        "string",
					"description": "low, medium, or high",
					"enum":        []any{"low", "medium", "high"},
				},
			},
			"required":             []any{"answer", "confidence"},
			"additionalProperties": false,
		},
	}

	opts := []agent.Option{
		agent.WithName("agent-json-response"),
		agent.WithDescription("Example agent constrained to JSON output via ResponseFormat / JSONSchema"),
		agent.WithSystemPrompt("You are a precise assistant. Respond only with JSON that matches the configured schema. No markdown fences or extra text."),
		agent.WithLLMClient(llmClient),
		agent.WithResponseFormat(rf),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Hi"
	}

	fmt.Println("user:", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("run failed: %v", err)
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		log.Fatalf("run get result: %v", err)
	}

	var verify json.RawMessage
	if err := json.Unmarshal([]byte(result.Content), &verify); err != nil {
		fmt.Println("assistant (raw):", result.Content)
		log.Fatalf("expected JSON body: %v", err)
	}
	pretty, err := json.MarshalIndent(verify, "", "  ")
	if err != nil {
		fmt.Println("assistant:", result.Content)
		return
	}
	fmt.Printf("assistant (JSON):\n%s\n", string(pretty))
	shared.PrintRunFooters(result)
}
