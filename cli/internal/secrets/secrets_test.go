package secrets

import "testing"

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("AGCTL_LLM_APIKEY", "from-env")
	if got := ResolveAPIKey("from-file"); got != "from-env" {
		t.Fatalf("env should win: %q", got)
	}

	t.Setenv("AGCTL_LLM_APIKEY", "")
	if got := ResolveAPIKey("from-file"); got != "from-file" {
		t.Fatalf("file fallback: %q", got)
	}
}
