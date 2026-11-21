package actionpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ReceiptSink stores signed receipts. Write may be called more than once for the
// same id when a step is retried. The latest write is the one verification uses.
// An append-only sink keeps every write; [VerifyLedger] keeps the last record per id.
type ReceiptSink interface {
	Write(ctx context.Context, receipt SignedReceipt) error
}

// MemorySink keeps the latest receipt for each id.
type MemorySink struct {
	mu    sync.Mutex
	order []string
	byID  map[string]SignedReceipt
}

// NewMemorySink returns an empty in-memory sink.
func NewMemorySink() *MemorySink {
	return &MemorySink{byID: make(map[string]SignedReceipt)}
}

// Write stores receipt, replacing an earlier write with the same id.
func (s *MemorySink) Write(_ context.Context, receipt SignedReceipt) error {
	if receipt.ID == "" {
		return errors.New("actionpolicy: receipt id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[receipt.ID]; !ok {
		s.order = append(s.order, receipt.ID)
	}
	s.byID[receipt.ID] = receipt
	return nil
}

// Receipts returns the latest receipt for each id, in first-seen order.
func (s *MemorySink) Receipts() []SignedReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SignedReceipt, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id])
	}
	return out
}

type jsonlSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLSink appends one JSON object per line. Writes are serialized.
// The writer must stay open for the life of the agent. Closing it is the caller's job.
func NewJSONLSink(w io.Writer) ReceiptSink {
	return &jsonlSink{w: w}
}

func (s *jsonlSink) Write(_ context.Context, receipt SignedReceipt) error {
	if s.w == nil {
		return errors.New("actionpolicy: jsonl sink has no writer")
	}
	if receipt.ID == "" {
		return errors.New("actionpolicy: receipt id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.w)
	enc.SetEscapeHTML(false)
	return enc.Encode(receipt)
}

// ReadJSONL reads every receipt line written by [NewJSONLSink].
// Verification should pass the result to [VerifyLedger], which keeps the last line per id.
func ReadJSONL(r io.Reader) ([]SignedReceipt, error) {
	dec := json.NewDecoder(r)
	var out []SignedReceipt
	for {
		var receipt SignedReceipt
		err := dec.Decode(&receipt)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, receipt)
	}
}
