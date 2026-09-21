package interfaces

import (
	"errors"
	"testing"
)

func TestLLMError_AsAndUnwrap(t *testing.T) {
	inner := errors.New("429 too many requests")
	err := error(&LLMError{Reason: LLMReasonRateLimit, StatusCode: 429, Err: inner})

	var llmErr *LLMError
	if !errors.As(err, &llmErr) {
		t.Fatal("errors.As should find *LLMError")
	}
	if llmErr.Reason != LLMReasonRateLimit || llmErr.StatusCode != 429 {
		t.Fatalf("got %+v", llmErr)
	}
	if !errors.Is(err, inner) {
		t.Fatal("Unwrap should expose the vendor error")
	}
}
