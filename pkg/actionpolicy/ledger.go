package actionpolicy

import "time"

// LedgerEntry is one tool call in a session chain.
// Hash is SHA-256 over the canonical JSON of the entry with Hash left empty.
type LedgerEntry struct {
	Seq        int       `json:"seq"`
	ActionID   string    `json:"actionId"`
	Tool       string    `json:"tool"`
	Kind       string    `json:"kind"`
	ArgsDigest string    `json:"argsDigest"`
	Decision   Result    `json:"decision"`
	Outcome    string    `json:"outcome"`
	Labels     []string  `json:"labels,omitempty"`
	At         time.Time `json:"at"`
	PrevHash   string    `json:"prevHash,omitempty"`
	Hash       string    `json:"hash,omitempty"`
}

// SessionState is the accumulated session passed between runs.
// Deferrals is the total across runs. The per-run cap is [Config.MaxDeferrals].
// Recent holds at most [Config.ContextWindow] entries, oldest first.
type SessionState struct {
	ID        string        `json:"id"`
	Head      string        `json:"head,omitempty"`
	Count     int           `json:"count"`
	Deferrals int           `json:"deferrals"`
	Labels    []string      `json:"labels,omitempty"`
	Recent    []LedgerEntry `json:"recent,omitempty"`
}

type ledgerHashBody struct {
	Seq        int       `json:"seq"`
	ActionID   string    `json:"actionId"`
	Tool       string    `json:"tool"`
	Kind       string    `json:"kind"`
	ArgsDigest string    `json:"argsDigest"`
	Decision   Result    `json:"decision"`
	Outcome    string    `json:"outcome"`
	Labels     []string  `json:"labels,omitempty"`
	At         time.Time `json:"at"`
	PrevHash   string    `json:"prevHash,omitempty"`
}

// ComputeHash returns the chain hash of e, ignoring e.Hash.
// At is normalized to UTC.
func (e LedgerEntry) ComputeHash() (string, error) {
	body := ledgerHashBody{
		Seq:        e.Seq,
		ActionID:   e.ActionID,
		Tool:       e.Tool,
		Kind:       e.Kind,
		ArgsDigest: e.ArgsDigest,
		Decision:   e.Decision,
		Outcome:    e.Outcome,
		Labels:     copyStrings(e.Labels),
		At:         e.At.UTC(),
		PrevHash:   e.PrevHash,
	}
	return DigestJSON(body)
}

// AppendEntry links entry onto state and returns the advanced state and the stored entry.
// entry.Seq, PrevHash, Hash, and At are set here. window bounds Recent. A window of zero keeps every entry.
// Labels on the entry are added to the session label set. Deferrals is left unchanged.
func AppendEntry(state SessionState, entry LedgerEntry, window int) (SessionState, LedgerEntry, error) {
	entry.Seq = state.Count + 1
	entry.PrevHash = state.Head
	entry.At = entry.At.UTC()
	entry.Hash = ""
	entry.Labels = copyStrings(entry.Labels)
	sum, err := entry.ComputeHash()
	if err != nil {
		return SessionState{}, LedgerEntry{}, err
	}
	entry.Hash = sum
	state.Head = sum
	state.Count = entry.Seq
	state.Labels = UnionLabels(state.Labels, entry.Labels)
	state.Recent = append(append([]LedgerEntry(nil), state.Recent...), entry)
	if window > 0 && len(state.Recent) > window {
		state.Recent = state.Recent[len(state.Recent)-window:]
	}
	return state, entry, nil
}

// PriorActions returns the recent window as policy context, oldest first.
func (s SessionState) PriorActions() []PriorAction {
	if len(s.Recent) == 0 {
		return nil
	}
	out := make([]PriorAction, len(s.Recent))
	for i, entry := range s.Recent {
		out[i] = PriorAction{
			Seq:        entry.Seq,
			Tool:       entry.Tool,
			Kind:       entry.Kind,
			ArgsDigest: entry.ArgsDigest,
			Decision:   entry.Decision,
			Outcome:    entry.Outcome,
			Labels:     copyStrings(entry.Labels),
		}
	}
	return out
}
