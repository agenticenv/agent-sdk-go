package actionpolicy

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Receipt types and outcome values stored in a signed receipt.
const (
	ReceiptTypeDecision = "decision"
	ReceiptTypeOutcome  = "outcome"

	OutcomeExecuted    = "executed"
	OutcomeFailed      = "failed"
	OutcomeNotExecuted = "not_executed"
	OutcomePending     = "pending"

	CausePolicyDenied        = "policy_denied"
	CauseUnknownTool         = "unknown_tool"
	CauseDeferLimit          = "defer_limit"
	CauseDeferralRejected    = "deferral_rejected"
	CauseDeferralTimeout     = "deferral_timeout"
	CauseApprovalRejected    = "approval_rejected"
	CauseApprovalTimeout     = "approval_timeout"
	CauseApprovalUnavailable = "approval_unavailable"
	CauseAuthorizationDenied = "authorization_denied"
	CauseCircuitOpen         = "circuit_open"
	CauseRecheckDenied       = "recheck_denied"
	CauseDepthLimit          = "depth_limit"

	// Resolution methods recorded on a deferral or an approval.
	MethodHuman       = "human"
	MethodAutomated   = "automated"
	MethodTimeout     = "timeout"
	MethodUnavailable = "unavailable"

	AlgEd25519 = "Ed25519"
	AlgES256   = "ES256"
)

// Receipt is the canonical payload that is signed.
// Decision receipts omit the outcome sections. Outcome receipts set Prev to the
// decision receipt's [SignedReceipt.Hash].
type Receipt struct {
	Action   ReceiptAction    `json:"action"`
	Context  ReceiptContext   `json:"context"`
	Identity ReceiptIdentity  `json:"identity"`
	Decision ReceiptDecision  `json:"decision"`
	Approval *ReceiptApproval `json:"approval,omitempty"`
	Deferral *ReceiptDeferral `json:"deferral,omitempty"`
	Outcome  *ReceiptOutcome  `json:"outcome,omitempty"`
	Child    *ReceiptChild    `json:"childSession,omitempty"`
	Ledger   *LedgerEntry     `json:"ledgerEntry,omitempty"`
	Prev     string           `json:"prev,omitempty"`
}

// ReceiptAction is the call the decision applies to.
// Args are redacted when a redactor is configured. ArgsDigest is always the
// digest of the unredacted arguments.
type ReceiptAction struct {
	ID         string         `json:"id"`
	Tool       string         `json:"tool"`
	Kind       string         `json:"kind"`
	Args       map[string]any `json:"args,omitempty"`
	ArgsDigest string         `json:"argsDigest"`
	Timestamp  time.Time      `json:"timestamp"`
}

// ReceiptContext is the session view bound into the receipt.
type ReceiptContext struct {
	SessionID        string   `json:"sessionId"`
	ConversationID   string   `json:"conversationId,omitempty"`
	IntentDigest     string   `json:"intentDigest"`
	PriorActionCount int      `json:"priorActionCount"`
	Labels           []string `json:"labels,omitempty"`
	LedgerHead       string   `json:"ledgerHead,omitempty"`
}

// ReceiptIdentity is the agent and the caller bound into the receipt.
type ReceiptIdentity struct {
	AgentID    string   `json:"agentId"`
	AgentName  string   `json:"agentName"`
	KeyID      string   `json:"keyId"`
	Principal  string   `json:"principal,omitempty"`
	Service    string   `json:"service,omitempty"`
	Roles      []string `json:"roles,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	Verified   bool     `json:"verified"`
	Delegation []Hop    `json:"delegation,omitempty"`
}

// ReceiptDecision is the policy result bound into the receipt.
// EvaluatedResult is set when the recorded result differs from the result the
// policy returned, for example when a deferral over the run limit is recorded as DENY.
type ReceiptDecision struct {
	Result          Result `json:"result"`
	EvaluatedResult Result `json:"evaluatedResult,omitempty"`
	PolicyID        string `json:"policyId"`
	PolicyVersion   string `json:"policyVersion"`
	PolicyHash      string `json:"policyHash"`
	Rule            string `json:"rule,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// ReceiptApproval records who resolved a step-up or a deferral.
type ReceiptApproval struct {
	Kind      string    `json:"kind,omitempty"`
	Status    string    `json:"status"`
	Resolver  string    `json:"resolver,omitempty"`
	Method    string    `json:"method,omitempty"`
	Note      string    `json:"note,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// ReceiptDeferral records how a deferred call was closed.
type ReceiptDeferral struct {
	Reason     string    `json:"reason,omitempty"`
	Method     string    `json:"method,omitempty"`
	ResolvedAt time.Time `json:"resolvedAt,omitempty"`
}

// ReceiptOutcome is what happened after the decision.
type ReceiptOutcome struct {
	Status             string `json:"status"`
	Cause              string `json:"cause,omitempty"`
	ExecutedArgsDigest string `json:"executedArgsDigest,omitempty"`
	ResultDigest       string `json:"resultDigest,omitempty"`
	DeliveredDigest    string `json:"deliveredDigest,omitempty"`
	Error              string `json:"error,omitempty"`
	RecheckResult      Result `json:"recheckResult,omitempty"`
}

// ReceiptChild is the delegated session summary recorded on the parent outcome.
type ReceiptChild struct {
	ID    string `json:"id"`
	Head  string `json:"head,omitempty"`
	Count int    `json:"count"`
}

// SignedReceipt is one signed decision or outcome.
// Payload is the canonical JSON of a [Receipt]. Signature is over those bytes.
type SignedReceipt struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Payload   []byte `json:"payload"`
	Alg       string `json:"alg"`
	KeyID     string `json:"keyId"`
	Signature []byte `json:"signature"`
}

// Hash is the lowercase hex SHA-256 of the signed payload bytes.
func (r SignedReceipt) Hash() string {
	return DigestBytes(r.Payload)
}

// ReceiptID is the stable id of one decision or outcome: SHA-256 of runID|actionID|typ.
func ReceiptID(runID, actionID, typ string) string {
	return DigestText(runID + "|" + actionID + "|" + typ)
}

// PrepareReceipt normalizes timestamps to UTC so two attempts produce the same bytes.
func PrepareReceipt(r Receipt) Receipt {
	r.Action.Timestamp = r.Action.Timestamp.UTC()
	r.Action.Args = cloneArgs(r.Action.Args)
	r.Context.Labels = copyStrings(r.Context.Labels)
	if r.Approval != nil {
		approval := *r.Approval
		approval.Timestamp = approval.Timestamp.UTC()
		r.Approval = &approval
	}
	if r.Deferral != nil {
		deferral := *r.Deferral
		if !deferral.ResolvedAt.IsZero() {
			deferral.ResolvedAt = deferral.ResolvedAt.UTC()
		}
		r.Deferral = &deferral
	}
	if r.Ledger != nil {
		entry := *r.Ledger
		entry.At = entry.At.UTC()
		entry.Labels = copyStrings(entry.Labels)
		r.Ledger = &entry
	}
	if r.Identity.Delegation != nil {
		r.Identity.Delegation = append([]Hop(nil), r.Identity.Delegation...)
	}
	r.Identity.Roles = copyStrings(r.Identity.Roles)
	r.Identity.Scopes = copyStrings(r.Identity.Scopes)
	return r
}

// Sign canonicalizes body and signs it with signer.
func Sign(ctx context.Context, signer Signer, id, typ string, body Receipt) (SignedReceipt, error) {
	if signer == nil {
		return SignedReceipt{}, errors.New("actionpolicy: signer is required")
	}
	payload, err := CanonicalJSON(PrepareReceipt(body))
	if err != nil {
		return SignedReceipt{}, err
	}
	sig, err := signer.Sign(ctx, payload)
	if err != nil {
		return SignedReceipt{}, err
	}
	return SignedReceipt{
		ID:        id,
		Type:      typ,
		Payload:   payload,
		Alg:       signer.Algorithm(),
		KeyID:     signer.KeyID(),
		Signature: sig,
	}, nil
}

// VerifyReceipt checks the signature over the stored payload bytes, then parses them.
func VerifyReceipt(r SignedReceipt, keys KeyResolver) (*Receipt, error) {
	if err := verifySignature(r, keys); err != nil {
		return nil, err
	}
	var body Receipt
	if err := json.Unmarshal(r.Payload, &body); err != nil {
		return nil, fmt.Errorf("actionpolicy: receipt payload: %w", err)
	}
	return &body, nil
}

// VerifyAction checks that outcome is the signed outcome for decision.
func VerifyAction(decision, outcome SignedReceipt, keys KeyResolver) error {
	if decision.Type != ReceiptTypeDecision {
		return fmt.Errorf("actionpolicy: decision receipt has type %q", decision.Type)
	}
	if outcome.Type != ReceiptTypeOutcome {
		return fmt.Errorf("actionpolicy: outcome receipt has type %q", outcome.Type)
	}
	dec, err := VerifyReceipt(decision, keys)
	if err != nil {
		return err
	}
	out, err := VerifyReceipt(outcome, keys)
	if err != nil {
		return err
	}
	if out.Prev != decision.Hash() {
		return errors.New("actionpolicy: outcome is not bound to the decision")
	}
	if dec.Action.ID == "" || dec.Action.ID != out.Action.ID {
		return errors.New("actionpolicy: decision and outcome name different calls")
	}
	return nil
}

// VerifyLedger checks the latest receipt for each id and the outcome hash chain.
// A decision with no outcome returns an error naming that call as not completed.
// head is the hash of the last ledger entry.
func VerifyLedger(receipts []SignedReceipt, keys KeyResolver) (string, error) {
	latest := latestByID(receipts)
	decisions := make(map[string]SignedReceipt)
	var outcomes []parsedOutcome
	seenDecisionAction := make(map[string]struct{})
	for _, receipt := range latest {
		body, err := VerifyReceipt(receipt, keys)
		if err != nil {
			return "", err
		}
		switch receipt.Type {
		case ReceiptTypeDecision:
			if _, dup := seenDecisionAction[body.Action.ID]; dup {
				return "", fmt.Errorf("actionpolicy: duplicate decision for call %s", body.Action.ID)
			}
			seenDecisionAction[body.Action.ID] = struct{}{}
			decisions[body.Action.ID] = receipt
		case ReceiptTypeOutcome:
			if body.Ledger == nil {
				return "", fmt.Errorf("actionpolicy: outcome for call %s has no ledger entry", body.Action.ID)
			}
			sum, err := body.Ledger.ComputeHash()
			if err != nil {
				return "", err
			}
			if sum != body.Ledger.Hash {
				return "", fmt.Errorf("actionpolicy: ledger hash mismatch for call %s", body.Action.ID)
			}
			outcomes = append(outcomes, parsedOutcome{receipt: receipt, body: body})
		default:
			return "", fmt.Errorf("actionpolicy: unknown receipt type %q", receipt.Type)
		}
	}
	sortOutcomes(outcomes)
	var head string
	for i, outcome := range outcomes {
		entry := outcome.body.Ledger
		if i == 0 && entry.Seq == 1 && entry.PrevHash != "" {
			return "", fmt.Errorf("actionpolicy: ledger chain break at call %s", outcome.body.Action.ID)
		}
		if i > 0 {
			if entry.PrevHash != head {
				return "", fmt.Errorf("actionpolicy: ledger chain break at call %s", outcome.body.Action.ID)
			}
			if entry.Seq != outcomes[i-1].body.Ledger.Seq+1 {
				return "", fmt.Errorf("actionpolicy: ledger sequence break at call %s", outcome.body.Action.ID)
			}
		}
		decision, ok := decisions[outcome.body.Action.ID]
		if !ok {
			return "", fmt.Errorf("actionpolicy: outcome for call %s has no decision", outcome.body.Action.ID)
		}
		if outcome.body.Prev != decision.Hash() {
			return "", fmt.Errorf("actionpolicy: outcome for call %s is not bound to its decision", outcome.body.Action.ID)
		}
		delete(decisions, outcome.body.Action.ID)
		head = entry.Hash
	}
	for actionID := range decisions {
		return "", fmt.Errorf("actionpolicy: action %s not completed", actionID)
	}
	return head, nil
}

type parsedOutcome struct {
	receipt SignedReceipt
	body    *Receipt
}

func latestByID(receipts []SignedReceipt) []SignedReceipt {
	index := make(map[string]int, len(receipts))
	var out []SignedReceipt
	for _, receipt := range receipts {
		if at, ok := index[receipt.ID]; ok {
			out[at] = receipt
			continue
		}
		index[receipt.ID] = len(out)
		out = append(out, receipt)
	}
	return out
}

func sortOutcomes(outcomes []parsedOutcome) {
	for i := 1; i < len(outcomes); i++ {
		entry := outcomes[i]
		j := i
		for j > 0 && outcomes[j-1].body.Ledger.Seq > entry.body.Ledger.Seq {
			outcomes[j] = outcomes[j-1]
			j--
		}
		outcomes[j] = entry
	}
}

func verifySignature(r SignedReceipt, keys KeyResolver) error {
	if keys == nil {
		return errors.New("actionpolicy: key resolver is required")
	}
	if len(r.Payload) == 0 || len(r.Signature) == 0 {
		return errors.New("actionpolicy: receipt payload and signature are required")
	}
	pub, err := keys.PublicKey(r.KeyID)
	if err != nil {
		return err
	}
	switch r.Alg {
	case AlgEd25519:
		key, ok := pub.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("actionpolicy: key %s is not Ed25519", r.KeyID)
		}
		if !ed25519.Verify(key, r.Payload, r.Signature) {
			return errors.New("actionpolicy: invalid signature")
		}
		return nil
	case AlgES256:
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() {
			return fmt.Errorf("actionpolicy: key %s is not ECDSA P-256", r.KeyID)
		}
		sum := sha256.Sum256(r.Payload)
		if !ecdsa.VerifyASN1(key, sum[:], r.Signature) {
			return errors.New("actionpolicy: invalid signature")
		}
		return nil
	default:
		return fmt.Errorf("actionpolicy: unsupported algorithm %q", r.Alg)
	}
}

// RedactedArgs returns the arguments stored in a receipt.
// A nil redactor stores args unchanged. The digest is always computed from args.
func RedactedArgs(redactor func(tool string, args map[string]any) map[string]any, tool string, args map[string]any) (map[string]any, string, error) {
	digest, err := DigestJSON(args)
	if err != nil {
		return nil, "", err
	}
	stored := args
	if redactor != nil {
		stored = redactor(tool, args)
	}
	return cloneArgs(stored), digest, nil
}
