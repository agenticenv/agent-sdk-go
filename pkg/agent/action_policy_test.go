package agent

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
)

func testActionPolicy(t *testing.T, version string) actionpolicy.Config {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := actionpolicy.NewEd25519Signer("k1", priv)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := actionpolicy.NewRuleSet("p", version, actionpolicy.ResultDeny)
	if err != nil {
		t.Fatal(err)
	}
	return actionpolicy.Config{Policy: policy, AgentID: "agent", Signer: signer, Sink: actionpolicy.NewMemorySink()}
}

func TestWithActionPolicy_RequiresFields(t *testing.T) {
	_, err := buildAgentConfig([]Option{
		WithName("ap"),
		WithLLMClient(testLLM(t)),
		WithActionPolicy(actionpolicy.Config{}),
	})
	if err == nil || !strings.Contains(err.Error(), "actionpolicy: policy is required") {
		t.Fatalf("got %v", err)
	}
}

func TestWithActionPolicy_SubAgentMustHaveOne(t *testing.T) {
	child, err := NewAgent(WithName("child"), WithLLMClient(testLLM(t)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildAgentConfig([]Option{
		WithName("parent"),
		WithLLMClient(testLLM(t)),
		WithActionPolicy(testActionPolicy(t, "1")),
		WithSubAgents(child),
	})
	if err == nil || !strings.Contains(err.Error(), "has no action policy") {
		t.Fatalf("got %v", err)
	}
}

func TestActionPolicyFingerprint_ChangesWithVersion(t *testing.T) {
	a := testActionPolicy(t, "1")
	b := testActionPolicy(t, "2")
	if actionPolicyFingerprint(&a) == actionPolicyFingerprint(&b) {
		t.Fatal("version must change the fingerprint")
	}
	if actionPolicyFingerprint(nil) != "" {
		t.Fatal("no policy must leave the fingerprint empty")
	}
}
