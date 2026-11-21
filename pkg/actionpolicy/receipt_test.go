package actionpolicy

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLedgerChainAndWindow(t *testing.T) {
	state := SessionState{ID: "s"}
	var err error
	state, _, err = AppendEntry(state, sampleEntry("a1", "executed"), 2)
	if err != nil {
		t.Fatal(err)
	}
	state, second, err := AppendEntry(state, sampleEntry("a2", "executed"), 2)
	if err != nil {
		t.Fatal(err)
	}
	state, third, err := AppendEntry(state, sampleEntry("a3", OutcomeNotExecuted), 2)
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 3 || len(state.Recent) != 2 || state.Recent[0].ActionID != "a2" {
		t.Fatalf("window = %+v", state.Recent)
	}
	if second.PrevHash == "" || third.PrevHash != second.Hash || state.Head != third.Hash {
		t.Fatalf("chain head broke: %+v %+v", second, third)
	}
	tampered := third
	tampered.Outcome = OutcomeExecuted
	sum, err := tampered.ComputeHash()
	if err != nil {
		t.Fatal(err)
	}
	if sum == third.Hash {
		t.Fatal("tampered entry kept its hash")
	}
	if got := state.PriorActions(); len(got) != 2 || got[1].Outcome != OutcomeNotExecuted {
		t.Fatalf("prior = %+v", got)
	}
}

func TestReceiptSignVerifyAndLedger(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewEd25519Signer("agent-key", priv)
	if err != nil {
		t.Fatal(err)
	}
	resolver := StaticKeys(map[string]crypto.PublicKey{"agent-key": pub})

	decisionBody, decision := signSample(t, signer, "run", "call-1", ReceiptTypeDecision, "")
	outcomeBody := decisionBody
	outcomeBody.Prev = decision.Hash()
	outcomeBody.Outcome = &ReceiptOutcome{Status: OutcomeExecuted, ResultDigest: DigestText("ok")}
	entry := LedgerEntry{ActionID: "call-1", Tool: "echo", Kind: "native", ArgsDigest: decisionBody.Action.ArgsDigest, Decision: ResultAllow, Outcome: OutcomeExecuted, At: decisionBody.Action.Timestamp}
	state, stored, err := AppendEntry(SessionState{}, entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcomeBody.Ledger = &stored
	outcome, err := Sign(context.Background(), signer, ReceiptID("run", "call-1", ReceiptTypeOutcome), ReceiptTypeOutcome, outcomeBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAction(decision, outcome, resolver); err != nil {
		t.Fatal(err)
	}
	head, err := VerifyLedger([]SignedReceipt{decision, outcome}, resolver)
	if err != nil || head != state.Head {
		t.Fatalf("ledger head = %s err=%v", head, err)
	}

	tampered := decision
	tampered.Payload = append([]byte{}, decision.Payload...)
	tampered.Payload[0] ^= 0x1
	if _, err := VerifyReceipt(tampered, resolver); err == nil {
		t.Fatal("expected tampered payload to fail")
	}
	if _, err := VerifyLedger([]SignedReceipt{decision}, resolver); err == nil || !strings.Contains(err.Error(), "not completed") {
		t.Fatalf("dangling decision = %v", err)
	}

	esPub, esPriv := mustP256(t)
	payload := decision.Payload
	sum := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, esPriv, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	esReceipt := decision
	esReceipt.Alg = AlgES256
	esReceipt.KeyID = "p256"
	esReceipt.Signature = sig
	if _, err := VerifyReceipt(esReceipt, StaticKeys(map[string]crypto.PublicKey{"p256": esPub})); err != nil {
		t.Fatal(err)
	}
}

func TestSinksAndSessionStore(t *testing.T) {
	sink := NewMemorySink()
	first := SignedReceipt{ID: "r1", Type: ReceiptTypeDecision, Payload: []byte(`{"n":1}`)}
	second := SignedReceipt{ID: "r1", Type: ReceiptTypeDecision, Payload: []byte(`{"n":2}`)}
	if err := sink.Write(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(context.Background(), SignedReceipt{ID: "r2", Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	got := sink.Receipts()
	if len(got) != 2 || string(got[0].Payload) != `{"n":2}` {
		t.Fatalf("memory sink = %+v", got)
	}

	var buf bytes.Buffer
	js := NewJSONLSink(&buf)
	if err := js.Write(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := js.Write(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	read, err := ReadJSONL(&buf)
	if err != nil || len(read) != 2 || string(read[1].Payload) != `{"n":2}` {
		t.Fatalf("jsonl = %+v %v", read, err)
	}

	store := NewMemorySessionStore()
	loaded, err := store.Load(context.Background(), "conv")
	if err != nil || loaded != nil {
		t.Fatalf("empty load = %+v %v", loaded, err)
	}
	state := SessionState{ID: "conv", Head: "h1", Count: 1}
	if err := store.Save(context.Background(), "conv", "", &state); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), "conv", "", &state); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("conflict = %v", err)
	}
	loaded, err = store.Load(context.Background(), "conv")
	if err != nil || loaded == nil || loaded.Head != "h1" {
		t.Fatalf("loaded = %+v %v", loaded, err)
	}
	loaded.Head = "changed-local-copy"
	again, err := store.Load(context.Background(), "conv")
	if err != nil || again.Head != "h1" {
		t.Fatalf("store was mutated: %+v", again)
	}
}

func TestCanonicalDigestAndConfig(t *testing.T) {
	left, err := DigestJSON(map[string]any{"b": 1, "a": 2})
	if err != nil {
		t.Fatal(err)
	}
	right, err := DigestJSON(map[string]any{"a": 2, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("map order changed digest: %s vs %s", left, right)
	}
	raw, err := CanonicalJSON(map[string]any{"html": "<b>"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `\u003c`) || strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("canonical = %q", raw)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = pub
	signer, err := NewEd25519Signer("k", priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Policy: mustPolicy(t), AgentID: "agent", Signer: signer, Sink: NewMemorySink()}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved := cfg.Resolved()
	if resolved.EvaluateTimeout != DefaultEvaluateTimeout || resolved.MaxDeferrals != DefaultMaxDeferrals || resolved.ContextWindow != DefaultContextWindow {
		t.Fatalf("defaults = %+v", resolved)
	}
	cfg.Policy = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing policy")
	}
}

func sampleEntry(id, outcome string) LedgerEntry {
	return LedgerEntry{ActionID: id, Tool: "echo", Kind: "native", ArgsDigest: DigestText(id), Decision: ResultAllow, Outcome: outcome, At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Labels: []string{"public"}}
}

func signSample(t *testing.T, signer Signer, runID, actionID, typ, prev string) (Receipt, SignedReceipt) {
	t.Helper()
	body := Receipt{
		Action: ReceiptAction{
			ID: actionID, Tool: "echo", Kind: "native",
			Args: map[string]any{"text": "hi"}, ArgsDigest: DigestText("hi"),
			Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("PST", -8*3600)),
		},
		Context:  ReceiptContext{SessionID: "s", IntentDigest: DigestText("hello"), PriorActionCount: 0},
		Identity: ReceiptIdentity{AgentID: "agent", AgentName: "Agent", KeyID: "agent-key", Verified: true},
		Decision: ReceiptDecision{Result: ResultAllow, PolicyID: "p", PolicyVersion: "1", PolicyHash: "abc"},
		Prev:     prev,
	}
	signed, err := Sign(context.Background(), signer, ReceiptID(runID, actionID, typ), typ, body)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := json.Marshal(body.Action.Timestamp.UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(signed.Payload, bytes.Trim(parsed, `"`)) {
		t.Fatalf("timestamp was not UTC: %s", signed.Payload)
	}
	return body, signed
}

func mustPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := NewRuleSet("p", "1", ResultDeny)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func mustP256(t *testing.T) (*ecdsa.PublicKey, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &key.PublicKey, key
}
