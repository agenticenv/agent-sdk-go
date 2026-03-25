package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vvsynapse/temporal-agent-sdk-go/examples"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/agent"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools/calculator"
)

// Coordinator uses the default tool approval policy (RequireAll): delegating to the
// math specialist requires approval (stdin y/n). The specialist uses AutoToolApprovalPolicy
// so its own tools (e.g. calculator) do not prompt.
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	baseQueue := cfg.TaskQueue
	mathQueue := baseQueue + "-math-specialist"
	coordQueue := baseQueue + "-coordinator"

	mathReg := tools.NewRegistry()
	mathReg.Register(calculator.New())

	mathSpecialist, err := agent.NewAgent(
		agent.WithName("MathSpecialist"),
		agent.WithDescription("Arithmetic specialist with calculator tool."),
		agent.WithSystemPrompt("You are a math specialist. Use the calculator tool for arithmetic. Reply with a short final answer."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: mathQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(mathReg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("math specialist agent: %v", err)
	}
	defer mathSpecialist.Close()

	coordinator, err := agent.NewAgent(
		agent.WithName("Coordinator"),
		agent.WithDescription("General assistant."),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: coordQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithSubAgents(mathSpecialist),
		agent.WithMaxSubAgentDepth(2),
		// Default RequireAllToolApprovalPolicy: sub-agent delegation follows same rules as any tool.
		agent.WithApprovalHandler(makeToolApprovalHandler(lineCh)),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	)
	if err != nil {
		log.Fatalf("coordinator agent: %v", err)
	}
	defer coordinator.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 987 multiplied by 654?"
	}

	fmt.Println("user:", prompt)
	fmt.Println("Coordinator tool calls (including delegation to the specialist) ask for approval: type y or n.")
	resp, err := coordinator.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("coordinator:", resp.Content)
}

func makeToolApprovalHandler(lineCh <-chan string) agent.ApprovalHandler {
	return func(ctx context.Context, req *agent.ApprovalRequest) {
		argsJSON, _ := json.MarshalIndent(req.Args, "", "  ")
		title := "Tool approval"
		if req.Kind == agent.ToolApprovalKindDelegation {
			title = "Delegate to specialist"
		}
		fmt.Printf("\n--- %s ---\nAgent: %s\n", title, req.AgentName)
		if req.DelegateToName != "" {
			fmt.Printf("Delegate to: %s\n", req.DelegateToName)
		}
		fmt.Printf("Args:\n%s\nApprove delegation? (y/n): ", string(argsJSON))
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lineCh:
			if ok && strings.TrimSpace(strings.ToLower(line)) == "y" {
				_ = req.Respond(agent.ApprovalStatusApproved)
			} else if ok {
				_ = req.Respond(agent.ApprovalStatusRejected)
			}
		}
	}
}
