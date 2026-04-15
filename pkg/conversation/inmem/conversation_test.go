package inmem

import (
	"context"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestInMemoryConversation_IsDistributed(t *testing.T) {
	c := NewInMemoryConversation()
	if c.IsDistributed() {
		t.Error("in-memory should not be distributed")
	}
}

func TestNewInMemoryConversation_MaxSizeDefault(t *testing.T) {
	c := NewInMemoryConversation(WithMaxSize(0))
	if c.maxSize != 100 {
		t.Fatalf("maxSize = %d, want 100", c.maxSize)
	}
	c2 := NewInMemoryConversation(WithMaxSize(-1))
	if c2.maxSize != 100 {
		t.Fatalf("maxSize = %d, want 100", c2.maxSize)
	}
}

func TestInMemoryConversation_AddMessage_EmptyID(t *testing.T) {
	c := NewInMemoryConversation()
	ctx := context.Background()
	if err := c.AddMessage(ctx, "", interfaces.Message{Role: interfaces.MessageRoleUser, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.ListMessages(ctx, "any")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func TestInMemoryConversation_ListMessages_EmptyID(t *testing.T) {
	c := NewInMemoryConversation()
	msgs, err := c.ListMessages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func TestInMemoryConversation_TrimToMaxSize(t *testing.T) {
	c := NewInMemoryConversation(WithMaxSize(3))
	ctx := context.Background()
	id := "conv-1"
	for i := 0; i < 5; i++ {
		if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := c.ListMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("len = %d, want 3 (last three kept)", len(msgs))
	}
	if msgs[0].Content != "c" || msgs[2].Content != "e" {
		t.Fatalf("unexpected window: %#v", msgs)
	}
}

func TestInMemoryConversation_ListMessages_LimitOffset(t *testing.T) {
	c := NewInMemoryConversation()
	ctx := context.Background()
	id := "c"
	for _, ch := range []string{"a", "b", "c", "d", "e"} {
		if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: ch}); err != nil {
			t.Fatal(err)
		}
	}
	// Last 2 messages: "d", "e"
	msgs, err := c.ListMessages(ctx, id, interfaces.WithLimit(2), interfaces.WithOffset(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "d" || msgs[1].Content != "e" {
		t.Fatalf("got %#v", msgs)
	}
	// Skip 1 from end then take 2: "c", "d"
	msgs2, err := c.ListMessages(ctx, id, interfaces.WithLimit(2), interfaces.WithOffset(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 2 || msgs2[0].Content != "c" || msgs2[1].Content != "d" {
		t.Fatalf("got %#v", msgs2)
	}
}

func TestInMemoryConversation_ListMessages_RoleFilter(t *testing.T) {
	c := NewInMemoryConversation()
	ctx := context.Background()
	id := "c"
	if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleSystem, Content: "sys"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: "u"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.ListMessages(ctx, id, interfaces.WithRoles(interfaces.MessageRoleUser))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "u" {
		t.Fatalf("got %#v", msgs)
	}
}

func TestInMemoryConversation_Clear(t *testing.T) {
	c := NewInMemoryConversation()
	ctx := context.Background()
	id := "x"
	if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Clear(ctx, id); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.ListMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func TestInMemoryConversation_Clear_EmptyID(t *testing.T) {
	c := NewInMemoryConversation()
	if err := c.Clear(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}
