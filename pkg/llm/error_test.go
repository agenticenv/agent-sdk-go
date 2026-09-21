package llm

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestClassify_Nil(t *testing.T) {
	if Classify(nil, 0, 0) != nil {
		t.Fatal("nil error should stay nil")
	}
}

func TestClassify_AlreadyWrapped(t *testing.T) {
	inner := errors.New("429")
	first := Classify(inner, 429, 0)
	second := Classify(first, 500, 0)
	var llmErr *interfaces.LLMError
	if !errors.As(second, &llmErr) {
		t.Fatal("expected *LLMError")
	}
	if llmErr.Reason != interfaces.LLMReasonRateLimit || llmErr.StatusCode != 429 {
		t.Fatalf("re-wrap changed classification: %+v", llmErr)
	}
	if !errors.Is(second, inner) {
		t.Fatal("Unwrap should still reach the vendor error")
	}
}

func TestClassify_Reasons(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		extras []string
		want   interfaces.LLMFailureReason
	}{
		{"429", errors.New("quota"), 429, nil, interfaces.LLMReasonRateLimit},
		{"rate_limit extra", errors.New("api"), 400, []string{"rate_limit_error"}, interfaces.LLMReasonRateLimit},
		{"too many requests", errors.New("too many requests"), 0, nil, interfaces.LLMReasonRateLimit},
		{"context_length", errors.New("context_length_exceeded"), 400, nil, interfaces.LLMReasonContextExceeded},
		{"prompt too long", errors.New("prompt is too long"), 400, nil, interfaces.LLMReasonContextExceeded},
		{"500 provider", errors.New("internal"), 500, nil, interfaces.LLMReasonProvider},
		{"401 provider", errors.New("auth"), 401, nil, interfaces.LLMReasonProvider},
		{"network unknown", errors.New("connection refused"), 0, nil, interfaces.LLMReasonUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err, tc.status, 0, tc.extras...)
			var llmErr *interfaces.LLMError
			if !errors.As(got, &llmErr) {
				t.Fatal("expected *LLMError")
			}
			if llmErr.Reason != tc.want {
				t.Fatalf("Reason = %q, want %q", llmErr.Reason, tc.want)
			}
			if llmErr.StatusCode != tc.status {
				t.Fatalf("StatusCode = %d, want %d", llmErr.StatusCode, tc.status)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("3"); got != 3*time.Second {
		t.Fatalf("seconds = %v", got)
	}
	if got := parseRetryAfter("0"); got != 0 {
		t.Fatalf("zero seconds = %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Fatalf("empty = %v", got)
	}
	if RetryAfterFromResponse(nil) != 0 {
		t.Fatal("nil response")
	}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("Retry-After", "2")
	if got := RetryAfterFromResponse(resp); got != 2*time.Second {
		t.Fatalf("header = %v", got)
	}
}
