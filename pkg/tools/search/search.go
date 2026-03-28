package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools"
)

var _ interfaces.Tool = (*Search)(nil)
var _ interfaces.ToolApproval = (*Search)(nil)

// Search performs Google search via Serper API. Requires SERPER_API_KEY.
type Search struct {
	client  *http.Client
	apiKey  string
	baseURL string
}

// Option configures Search.
type Option func(*Search)

// WithAPIKey sets the Serper API key. Defaults to SERPER_API_KEY env.
func WithAPIKey(key string) Option {
	return func(s *Search) {
		if key != "" {
			s.apiKey = key
		}
	}
}

// New returns a new Search tool. Uses SERPER_API_KEY from env if not set.
func New(opts ...Option) *Search {
	s := &Search{
		client:  &http.Client{},
		apiKey:  os.Getenv("SERPER_API_KEY"),
		baseURL: "https://google.serper.dev/search",
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (*Search) Name() string { return "search" }

func (*Search) Description() string {
	return "Searches the web via Google. Use when the user needs current information, news, facts, or links. Requires SERPER_API_KEY (2,500 free queries at serper.dev)."
}

func (*Search) ApprovalRequired() bool { return true }

func (*Search) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"query": tools.ParamString("Search query"),
			"num":   tools.ParamInteger("Number of results (1-10, default 5)"),
		},
		"query",
	)
}

type serperReq struct {
	Q   string `json:"q"`
	Num int    `json:"num,omitempty"`
}

type serperOrg struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

type serperResp struct {
	Organic []serperOrg `json:"organic"`
}

func (s *Search) Execute(ctx context.Context, args map[string]any) (any, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("SERPER_API_KEY is not set; get a free key at https://serper.dev")
	}
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	num := 5
	if n, ok := toInt(args["num"]); ok && n >= 1 && n <= 10 {
		num = n
	}

	body, _ := json.Marshal(serperReq{Q: query, Num: num})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", s.apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned %s", resp.Status)
	}

	var out serperResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("search decode: %w", err)
	}

	results := make([]map[string]any, 0, len(out.Organic))
	for _, o := range out.Organic {
		results = append(results, map[string]any{
			"title":   o.Title,
			"link":    o.Link,
			"snippet": o.Snippet,
		})
	}
	return map[string]any{"results": results}, nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}
