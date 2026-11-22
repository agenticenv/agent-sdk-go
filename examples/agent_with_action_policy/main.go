// Action policy example.
//
// shell is denied. refund is stepped up when it is labeled restricted.
// Every other tool call is allowed. Each run writes a fresh receipts.jsonl.
// AutoToolApprovalPolicy disables the default require-all tool gate so Run
// only pauses for action-policy step-up (via WithApprovalHandler).
//
//	go run ./agent_with_action_policy
//	go run ./agent_with_action_policy verify receipts.jsonl
package main

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		if len(os.Args) < 3 {
			log.Fatal("usage: verify receipts.jsonl")
		}
		if err := verify(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := run(); err != nil {
		if errors.Is(err, agent.ErrActionPolicyUnavailable) || errors.Is(err, agent.ErrSessionConflict) || errors.Is(err, agent.ErrIdentityUnverified) {
			log.Fatalf("action policy: %v", err)
		}
		log.Fatal(err)
	}
}

func run() error {
	cfg := config.LoadFromEnv()
	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile("action-policy.pub", []byte(hex.EncodeToString(pub)), 0o600); err != nil {
		return err
	}
	signer, err := actionpolicy.NewEd25519Signer("key-1", priv)
	if err != nil {
		return err
	}
	policy, err := actionpolicy.NewRuleSet("shop", "1", actionpolicy.ResultAllow,
		actionpolicy.Rule{
			ID: "no-shell", Class: actionpolicy.ClassForbidden,
			Match:    actionpolicy.Tool("shell"),
			Decision: actionpolicy.Deny("shell is not allowed"),
		},
		actionpolicy.Rule{
			ID: "restricted-refund", Class: actionpolicy.ClassContextual, Priority: 10,
			Match:    actionpolicy.And(actionpolicy.Tool("refund"), actionpolicy.HasLabel("restricted")),
			Decision: actionpolicy.StepUp("a person must approve a restricted refund"),
		},
	)
	if err != nil {
		return err
	}
	// Truncate so each run's key matches a fresh ledger (verify fails on mixed keys).
	receipts, err := os.OpenFile("receipts.jsonl", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = receipts.Close() }()

	opts := []agent.Option{
		agent.WithName("shop-agent"),
		agent.WithSystemPrompt("You can refund an order or refuse a shell command. Be concise."),
		agent.WithLLMClient(llmClient),
		agent.WithTools(tool{name: "shell"}, tool{name: "refund"}),
		// Default is RequireAllToolApprovalPolicy; auto-approve so only StepUp pauses.
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithApprovalHandler(makeApprovalHandler()),
		agent.WithActionPolicy(actionpolicy.Config{
			Policy:  policy,
			AgentID: "shop-agent",
			Signer:  signer,
			Sink:    actionpolicy.NewJSONLSink(receipts),
			Classifier: actionpolicy.NewClassifier("shop", actionpolicy.ClassifierConfig{
				Tools: map[string][]string{"refund": {"restricted"}},
			}),
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	a, err := agent.NewAgent(opts...)
	if err != nil {
		return err
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Refund order 17, and do not use the shell."
	}
	fmt.Printf("user: %s\n", prompt)
	run, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		return err
	}
	result, err := run.Get(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("assistant: %s\n", result.Content)
	if result.Actions != nil {
		fmt.Printf("actions: %d denied=%d head=%s\n", result.Actions.Count, result.Actions.Denied, result.Actions.LedgerHead)
	}
	fmt.Println("receipts: receipts.jsonl")
	fmt.Println("verify: go run ./agent_with_action_policy verify receipts.jsonl")
	return nil
}

func verify(path string) error {
	raw, err := os.ReadFile("action-policy.pub")
	if err != nil {
		return err
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	receipts, err := actionpolicy.ReadJSONL(f)
	if err != nil {
		return err
	}
	head, err := actionpolicy.VerifyLedger(receipts, actionpolicy.StaticKeys(map[string]crypto.PublicKey{
		"key-1": ed25519.PublicKey(pub),
	}))
	if err != nil {
		return err
	}
	fmt.Printf("verified %d receipts, head %s\n", len(receipts), head)
	return nil
}

func makeApprovalHandler() agent.ApprovalHandler {
	autoApprove := strings.EqualFold(strings.TrimSpace(os.Getenv("EXAMPLES_AUTO_APPROVE")), "true")
	return func(_ context.Context, req *agent.ApprovalRequest) {
		v, err := agent.ParseToolApproval(req)
		if err != nil {
			_ = req.Respond(agent.ApprovalStatusRejected)
			return
		}
		label := v.ToolDisplayName
		if label == "" {
			label = v.ToolName
		}
		fmt.Printf("[action-policy] step-up for %s\n", label)
		if autoApprove {
			fmt.Println("[action-policy] EXAMPLES_AUTO_APPROVE=true — approving")
			_ = req.Respond(agent.ApprovalStatusApproved)
			return
		}
		fmt.Print("[action-policy] approve? (y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			_ = req.Respond(agent.ApprovalStatusRejected)
			return
		}
		if strings.TrimSpace(strings.ToLower(line)) == "y" {
			_ = req.Respond(agent.ApprovalStatusApproved)
			return
		}
		_ = req.Respond(agent.ApprovalStatusRejected)
	}
}

type tool struct{ name string }

func (t tool) Name() string                      { return t.name }
func (t tool) DisplayName() string               { return t.name }
func (t tool) Description() string               { return t.name }
func (t tool) Parameters() interfaces.JSONSchema { return nil }
func (t tool) Execute(context.Context, map[string]any) (any, error) {
	return t.name + " completed", nil
}
func (t tool) ApprovalRequired() bool { return false }
