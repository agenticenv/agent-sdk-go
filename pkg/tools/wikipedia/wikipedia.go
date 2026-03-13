package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/tools"
)

var _ interfaces.Tool = (*Wikipedia)(nil)

// Wikipedia searches Wikipedia and returns article excerpts. Free, no API key.
type Wikipedia struct {
	client  *http.Client
	baseURL string
	lang    string
	limit   int
}

// Option configures Wikipedia.
type Option func(*Wikipedia)

// WithLanguage sets the Wikipedia language (e.g. "en", "de").
func WithLanguage(lang string) Option {
	return func(w *Wikipedia) {
		if lang != "" {
			w.lang = lang
		}
	}
}

// WithLimit sets the max results (default 5).
func WithLimit(n int) Option {
	return func(w *Wikipedia) {
		if n > 0 && n <= 20 {
			w.limit = n
		}
	}
}

// New returns a new Wikipedia tool.
func New(opts ...Option) *Wikipedia {
	w := &Wikipedia{
		client:  &http.Client{},
		baseURL: "https://%s.wikipedia.org",
		lang:    "en",
		limit:   5,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (*Wikipedia) Name() string { return "wikipedia" }

func (*Wikipedia) Description() string {
	return "Searches Wikipedia for articles and returns excerpts. Use when the user asks about facts, concepts, people, places, or history. Free, no API key required."
}

func (*Wikipedia) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"query": tools.ParamString("Search query (e.g. 'Go programming language', 'Albert Einstein')"),
			"limit": tools.ParamInteger("Max results to return (1-10, default 5)"),
		},
		"query",
	)
}

type wikiPage struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Key       string `json:"key"`
	Excerpt   string `json:"excerpt"`
	Thumbnail *struct {
		URL string `json:"url"`
	} `json:"thumbnail"`
	Description *string `json:"description"`
}

type wikiSearchResponse struct {
	Pages []wikiPage `json:"pages"`
}

func (w *Wikipedia) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := w.limit
	if l, ok := toolsToInt(args["limit"]); ok && l >= 1 && l <= 10 {
		limit = l
	}

	base := fmt.Sprintf(w.baseURL, w.lang)
	u := base + "/w/rest.php/v1/search/page?q=" + url.QueryEscape(query) + "&limit=" + fmt.Sprintf("%d", limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "temporal-agents-go/1.0")
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wikipedia request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikipedia returned %s", resp.Status)
	}

	var out wikiSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("wikipedia decode: %w", err)
	}

	results := make([]map[string]any, 0, len(out.Pages))
	for _, p := range out.Pages {
		r := map[string]any{
			"title":   p.Title,
			"key":     p.Key,
			"excerpt": p.Excerpt,
			"url":     base + "/wiki/" + url.PathEscape(p.Key),
		}
		if p.Description != nil {
			r["description"] = *p.Description
		}
		if p.Thumbnail != nil && p.Thumbnail.URL != "" {
			r["thumbnail"] = "https:" + p.Thumbnail.URL
		}
		results = append(results, r)
	}
	return map[string]any{"results": results}, nil
}

func toolsToInt(v any) (int, bool) {
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
