package agent

import (
	"testing"

	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
)

func TestBuildConversationInmem(t *testing.T) {
	cfg := &config.Config{
		Conversation: &config.ConversationConfig{
			Store:   "inmem",
			MaxSize: 50,
			Size:    10,
		},
	}
	convCfg, closer, err := buildConversation(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if closer != nil {
		t.Fatal("inmem should not need a closer")
	}
	if convCfg.Conversation == nil || convCfg.Size != 10 {
		t.Fatalf("unexpected conversation config: %+v", convCfg)
	}
}

func TestBuildConversationUnknownStore(t *testing.T) {
	_, _, err := buildConversation(&config.Config{
		Conversation: &config.ConversationConfig{Store: "sqlite"},
	})
	if err == nil {
		t.Fatal("expected error for unknown store")
	}
}
