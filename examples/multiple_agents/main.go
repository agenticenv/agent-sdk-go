package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentrestate "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Printf("failed to create LLM client: %v", err)
		return
	}

	// Restate: separate listen port + DeploymentURL per agent when co-located.
	agent1RuntimeOpts, agent2RuntimeOpts, err := runtimeOptsForAgents(cfg)
	if err != nil {
		log.Fatal(err)
	}

	agent1Opts := []agent.Option{
		agent.WithName("math-agent"),
		agent.WithSystemPrompt("You are a helpful math assistant. Keep answers brief."),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	agent1Opts = append(agent1Opts, agent1RuntimeOpts...)
	agent1, err := agent.NewAgent(agent1Opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent 1", err))
	}
	defer agent1.Close()

	agent2Opts := []agent.Option{
		agent.WithName("writing-agent"),
		agent.WithSystemPrompt("You are a creative writing assistant. Be expressive."),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	agent2Opts = append(agent2Opts, agent2RuntimeOpts...)
	agent2, err := agent.NewAgent(agent2Opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent 2", err))
	}
	defer agent2.Close()

	mode, prompt := parseArgs()
	if prompt == "" {
		prompt = "What is 7 times 8?"
	}

	runAgent := func(name string, a *agent.Agent, p string) {
		fmt.Printf("\n--- %s ---\n", name)
		agentRun, err := a.Run(context.Background(), p, nil)
		if err != nil {
			fmt.Printf("%s error: %v\n", name, err)
			return
		}
		result, err := agentRun.Get(context.Background())
		if err != nil {
			fmt.Printf("%s get result error: %v\n", name, err)
			return
		}
		fmt.Printf("%s: %s\n", name, result.Content)
		shared.PrintRunFooters(result)
	}

	if mode == "concurrent" {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			runAgent("Agent 1 (math)", agent1, prompt)
		}()
		go func() {
			defer wg.Done()
			runAgent("Agent 2 (creative)", agent2, prompt)
		}()
		wg.Wait()
	} else {
		// sequential (default)
		runAgent("Agent 1 (math)", agent1, prompt)
		runAgent("Agent 2 (creative)", agent2, prompt)
	}
	fmt.Println("\nDone.")
}

// runtimeOptsForAgents returns per-agent runtime options.
// Restate: math-agent uses the configured listen address; writing-agent uses the next port
// (and matching DeploymentURL) so both can register distinct AgentLoop services.
func runtimeOptsForAgents(cfg *config.Config) (agent1, agent2 []agent.Option, err error) {
	if cfg == nil || !cfg.UseRestateRuntime() {
		opts := config.RuntimeOption(cfg)
		return opts, opts, nil
	}
	listen1 := cfg.Restate.EndpointListenAddress
	listen2, err := bumpListenPort(listen1, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("restate listen address for agent-2: %w", err)
	}
	return restateRuntimeOption(cfg, listen1), restateRuntimeOption(cfg, listen2), nil
}

func restateRuntimeOption(cfg *config.Config, listen string) []agent.Option {
	deploy := cfg.Restate.DeploymentURL
	if deploy != "" {
		if port, err := listenPort(listen); err == nil {
			deploy = withURLPort(deploy, port)
		}
	}
	return []agent.Option{
		agentrestate.WithRestateConfig(&agentrestate.RestateConfig{
			Ingress: agentrestate.IngressConfig{
				URL:     cfg.Restate.IngressURL,
				AuthKey: cfg.Restate.AuthKey,
			},
			Endpoint: agentrestate.EndpointConfig{
				ListenAddress: listen,
				AdminURL:      cfg.Restate.AdminURL,
				DeploymentURL: deploy,
			},
			EventLog: agentrestate.EventLogConfig{DisableClear: true},
		}),
	}
}

func bumpListenPort(listen string, delta int) (string, error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port+delta)), nil
}

func listenPort(listen string) (int, error) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portStr)
}

func withURLPort(raw string, port int) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return u.String()
}

// parseArgs returns (mode, prompt). First arg "sequential" or "concurrent" sets mode; else default sequential.
func parseArgs() (mode, prompt string) {
	mode = "sequential"
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "sequential" || args[0] == "concurrent") {
		mode = args[0]
		args = args[1:]
	}
	prompt = strings.Join(args, " ")
	return mode, prompt
}
