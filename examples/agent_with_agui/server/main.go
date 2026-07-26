package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

// Minimal HTTP agent: POST /agui accepts JSON with "prompt" or AG-UI-style "messages",
// streams agent events as Server-Sent Events (data: <json>\n\n) for AG-UI clients.
// Reasoning matches [examples/agent_with_reasoning]: Anthropic/Gemini extended thinking;
// OpenAI needs Effort set for reasoning models — see that example’s header comment.
//
// CopilotKit’s HttpAgent (@ag-ui/client ≥ 0.0.52, aligned via package overrides) maps
// REASONING_* stream events to messages with role "reasoning", which CopilotChat renders
// as the collapsible “Thinking…” block. Pin old @ag-ui/client at the app root or those
// messages never appear even though the SSE parses.

type chatRequest struct {
	Prompt   string `json:"prompt"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}

	cfg := config.LoadFromEnv()
	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("LLM client: %v", err)
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		calculator.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	agentOpts := []agent.Option{
		agent.WithName("agui-demo-agent"),
		agent.WithDescription("Streaming demo for AG-UI / CopilotKit"),
		agent.WithSystemPrompt("You are a helpful assistant. Be concise."),
		agent.WithLLMClient(llmClient),
		agent.WithLLMSampling(&agent.LLMSampling{
			MaxTokens: 4096,
			Reasoning: &interfaces.LLMReasoning{
				Enabled:      true,
				BudgetTokens: 2048,
			},
		}),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	agentOpts = append(agentOpts, config.RuntimeOption(cfg)...)
	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("agent", err))
	}
	defer a.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/agui", agentStreamr(a))

	addr := ":" + port
	log.Printf("AG-UI SSE agent listening on http://localhost%s/agui (POST JSON body)", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func agentStreamr(a *agent.Agent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}

		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		prompt := strings.TrimSpace(body.Prompt)
		for i := len(body.Messages) - 1; i >= 0; i-- {
			if strings.EqualFold(body.Messages[i].Role, "user") && strings.TrimSpace(body.Messages[i].Content) != "" {
				prompt = strings.TrimSpace(body.Messages[i].Content)
				break
			}
		}
		if prompt == "" {
			http.Error(w, `need "prompt" or a user "messages" entry`, http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()
		// runID is available synchronously before the first event; the AG-UI client sees
		// it via the RUN_STARTED event's RunID field on the stream itself.
		agentStream, err := a.Stream(ctx, prompt, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ch, err := agentStream.Events(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		for ev := range ch {
			if ev == nil {
				continue
			}
			data, err := ev.ToJSON()
			if err != nil {
				continue
			}
			//fmt.Printf("[%s] %s\n", ev.Type(), string(data))
			if shouldSkipCopilotKitEmptyDeltaContent(data) {
				continue
			}
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(data)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

// shouldSkipCopilotKitEmptyDeltaContent returns true when the payload would fail
// @ag-ui/core validation: TEXT_MESSAGE_CONTENT (and deprecated THINKING_TEXT_MESSAGE_*)
// use a non-empty delta; providers may still emit "".
func shouldSkipCopilotKitEmptyDeltaContent(data []byte) bool {
	var meta struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	switch meta.Type {
	case "TEXT_MESSAGE_CONTENT", "THINKING_TEXT_MESSAGE_CONTENT":
		return meta.Delta == ""
	default:
		return false
	}
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
}
