package types

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2/clientcredentials"
)

func TestMCPTransportConfig_interface(t *testing.T) {
	var (
		_ MCPTransportConfig = MCPStdio{}
		_ MCPTransportConfig = MCPStreamableHTTP{}
		_ MCPTransportConfig = MCPLoopback{}
	)
}

func TestMCPStdio_Validate_ok(t *testing.T) {
	s := MCPStdio{Command: "node", Args: []string{"server.js"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStdio_Validate_emptyCommand(t *testing.T) {
	if err := (MCPStdio{}).Validate(); err == nil {
		t.Fatal("expected error")
	}
	if err := (MCPStdio{Command: "  "}).Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPLoopback_Validate_ok(t *testing.T) {
	_, tr := sdkmcp.NewInMemoryTransports()
	lb := MCPLoopback{Transport: tr}
	if err := lb.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPLoopback_Validate_rejectsBadTransport(t *testing.T) {
	if err := (MCPLoopback{}).Validate(); err == nil {
		t.Fatal("expected error")
	}
	if err := (MCPLoopback{Transport: "not-a-transport"}).Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPStreamableHTTP_Validate_urlRequired(t *testing.T) {
	if err := (MCPStreamableHTTP{}).Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPStreamableHTTP_Validate_none(t *testing.T) {
	h := MCPStreamableHTTP{URL: "https://x/mcp"}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStreamableHTTP_Validate_bearer(t *testing.T) {
	h := MCPStreamableHTTP{URL: "https://x/mcp", Token: "t"}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStreamableHTTP_Validate_oauthClientCreds(t *testing.T) {
	h := MCPStreamableHTTP{
		URL: "https://x/mcp",
		OAuthClientCreds: &clientcredentials.Config{
			ClientID:     "id",
			ClientSecret: "sec",
			TokenURL:     "https://idp/token",
			Scopes:       []string{"a", "b"},
		},
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPStreamableHTTP_Validate_tokenAndOAuthRejects(t *testing.T) {
	h := MCPStreamableHTTP{
		URL:   "https://x/mcp",
		Token: "t",
		OAuthClientCreds: &clientcredentials.Config{
			ClientID: "id", ClientSecret: "s", TokenURL: "https://idp/token",
		},
	}
	if err := h.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPStreamableHTTP_Validate_oauthIncomplete(t *testing.T) {
	h := MCPStreamableHTTP{
		URL:              "https://x/mcp",
		OAuthClientCreds: &clientcredentials.Config{ClientID: "id"},
	}
	if err := h.Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolFilter_Validate_bothLists(t *testing.T) {
	err := MCPToolFilter{AllowTools: []string{"a"}, BlockTools: []string{"b"}}.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMCPToolFilter_Validate_ok(t *testing.T) {
	if err := (MCPToolFilter{AllowTools: []string{"a"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (MCPToolFilter{BlockTools: []string{"b"}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (MCPToolFilter{}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMCPToolFilter_Apply(t *testing.T) {
	defs := []ToolSpec{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	t.Run("inactive", func(t *testing.T) {
		got := (MCPToolFilter{}).Apply(defs)
		if len(got) != 3 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("allow", func(t *testing.T) {
		got := MCPToolFilter{AllowTools: []string{"a", "c"}}.Apply(defs)
		if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("block", func(t *testing.T) {
		got := MCPToolFilter{BlockTools: []string{"b"}}.Apply(defs)
		if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
			t.Fatalf("got %+v", got)
		}
	})
}
