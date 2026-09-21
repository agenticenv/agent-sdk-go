package llm

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// Classify wraps a vendor Generate/stream error as [*interfaces.LLMError].
// status is the HTTP status when known (0 if not). extras are extra tokens
// (error type, code, message) used when the vendor Error() string is incomplete.
// Already-wrapped [*interfaces.LLMError] values are returned unchanged.
func Classify(err error, status int, retryAfter time.Duration, extras ...string) error {
	if err == nil {
		return nil
	}
	var existing *interfaces.LLMError
	if errors.As(err, &existing) {
		return err
	}
	msg := strings.ToLower(strings.Join(nonEmpty(extras), " "))
	if msg == "" {
		msg = strings.ToLower(safeErrorString(err))
	}
	return &interfaces.LLMError{
		Reason:     classifyReason(status, msg),
		StatusCode: status,
		RetryAfter: retryAfter,
		Err:        err,
	}
}

func safeErrorString(err error) (s string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			s = ""
		}
	}()
	return err.Error()
}

func nonEmpty(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func classifyReason(status int, msg string) interfaces.LLMFailureReason {
	if status == http.StatusTooManyRequests || strings.Contains(msg, "rate_limit") || strings.Contains(msg, "too many requests") {
		return interfaces.LLMReasonRateLimit
	}
	if isContextExceeded(msg) {
		return interfaces.LLMReasonContextExceeded
	}
	if status >= 400 {
		return interfaces.LLMReasonProvider
	}
	return interfaces.LLMReasonUnknown
}

func isContextExceeded(msg string) bool {
	for _, p := range []string{
		"context_length",
		"context length",
		"maximum context",
		"max context",
		"too many tokens",
		"token limit",
		"prompt is too long",
		"prompt too long",
		"exceeds the context",
	} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// RetryAfterFromResponse reads the Retry-After header (delta-seconds or HTTP date).
func RetryAfterFromResponse(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	return parseRetryAfter(resp.Header.Get("Retry-After"))
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := time.Until(t)
	if d < 0 {
		return 0
	}
	return d
}
