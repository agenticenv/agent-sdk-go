package redis

import (
	"context"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func newTestConversation(t *testing.T, opts ...Option) (*RedisConversation, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	base := []Option{WithAddr(s.Addr())}
	c, err := NewRedisConversation(append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, s
}

func TestNewRedisConversation_RequiresAddr(t *testing.T) {
	_, err := NewRedisConversation()
	if err == nil || err.Error() != "addr is required when not using WithClient" {
		t.Fatalf("got %v", err)
	}
}

func TestNewRedisConversation_WithClient_CloseDoesNotOwn(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	c, err := NewRedisConversation(WithClient(rdb))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Client still usable.
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRedisConversation_IsDistributed(t *testing.T) {
	c, _ := newTestConversation(t)
	if !c.IsDistributed() {
		t.Error("expected distributed")
	}
}

func TestRedisConversation_AddMessage_EmptyID(t *testing.T) {
	c, _ := newTestConversation(t)
	err := c.AddMessage(context.Background(), "", interfaces.Message{Role: interfaces.MessageRoleUser, Content: "x"})
	if err == nil || err.Error() != "id is required" {
		t.Fatalf("got %v", err)
	}
}

func TestRedisConversation_ListAndClear_EmptyID(t *testing.T) {
	c, _ := newTestConversation(t)
	ctx := context.Background()
	_, err := c.ListMessages(ctx, "")
	if err == nil || err.Error() != "id is required" {
		t.Fatalf("ListMessages: %v", err)
	}
	if err := c.Clear(ctx, ""); err == nil || err.Error() != "id is required" {
		t.Fatalf("Clear: %v", err)
	}
}

func TestRedisConversation_AddListClear(t *testing.T) {
	c, _ := newTestConversation(t)
	ctx := context.Background()
	id := "conv-1"
	msg := interfaces.Message{Role: interfaces.MessageRoleUser, Content: "hello"}
	if err := c.AddMessage(ctx, id, msg); err != nil {
		t.Fatal(err)
	}
	msgs, err := c.ListMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello" {
		t.Fatalf("msgs = %#v", msgs)
	}
	if err := c.Clear(ctx, id); err != nil {
		t.Fatal(err)
	}
	msgs2, err := c.ListMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 0 {
		t.Fatalf("len = %d", len(msgs2))
	}
}

func TestRedisConversation_MaxSizeTrim(t *testing.T) {
	c, _ := newTestConversation(t, WithMaxSize(3))
	ctx := context.Background()
	id := "trim"
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
		t.Fatalf("len = %d, want 3", len(msgs))
	}
	if msgs[0].Content != "c" || msgs[2].Content != "e" {
		t.Fatalf("window = %#v", msgs)
	}
}

func TestRedisConversation_ListMessages_LimitOffset(t *testing.T) {
	c, _ := newTestConversation(t)
	ctx := context.Background()
	id := "lo"
	for _, ch := range []string{"a", "b", "c", "d", "e"} {
		if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: ch}); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := c.ListMessages(ctx, id, interfaces.WithLimit(2), interfaces.WithOffset(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "d" || msgs[1].Content != "e" {
		t.Fatalf("got %#v", msgs)
	}
	msgs2, err := c.ListMessages(ctx, id, interfaces.WithLimit(2), interfaces.WithOffset(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 2 || msgs2[0].Content != "c" || msgs2[1].Content != "d" {
		t.Fatalf("got %#v", msgs2)
	}
}

func TestRedisConversation_ListMessages_RoleFilter(t *testing.T) {
	c, _ := newTestConversation(t)
	ctx := context.Background()
	id := "roles"
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

func TestRedisConversation_WithKeyPrefix(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c, err := NewRedisConversation(WithAddr(s.Addr()), WithKeyPrefix("myapp"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	id := "k1"
	if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	key := "myapp:k1:messages"
	n, err := rdb.LLen(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("llen = %d", n)
	}
}

func TestRedisConversation_ListMessages_UnmarshalError(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	c, err := NewRedisConversation(WithClient(rdb))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	key := "conversation:bad:messages"
	if err := rdb.RPush(ctx, key, "not-json").Err(); err != nil {
		t.Fatal(err)
	}
	_, err = c.ListMessages(ctx, "bad")
	if err == nil || err.Error() == "" {
		t.Fatal("expected unmarshal error")
	}
}

func TestRedisConversation_WithMaxSizeZeroUsesDefault(t *testing.T) {
	s := miniredis.RunT(t)
	c, err := NewRedisConversation(WithAddr(s.Addr()), WithMaxSize(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.maxSize != 100 {
		t.Fatalf("maxSize = %d", c.maxSize)
	}
}

func TestRoleIn(t *testing.T) {
	if !roleIn(interfaces.MessageRoleUser, []interfaces.MessageRole{interfaces.MessageRoleUser, interfaces.MessageRoleAssistant}) {
		t.Fatal("user should match")
	}
	if roleIn(interfaces.MessageRoleSystem, []interfaces.MessageRole{interfaces.MessageRoleUser}) {
		t.Fatal("system should not match")
	}
}

func TestRedisConversation_TTLExpiresKey(t *testing.T) {
	s := miniredis.RunT(t)
	c, err := NewRedisConversation(WithAddr(s.Addr()), WithTTL(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	id := "ttl"
	if err := c.AddMessage(ctx, id, interfaces.Message{Role: interfaces.MessageRoleUser, Content: "x"}); err != nil {
		t.Fatal(err)
	}
	s.FastForward(11 * time.Minute)
	msgs, err := c.ListMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected key expired, got %d msgs", len(msgs))
	}
}
