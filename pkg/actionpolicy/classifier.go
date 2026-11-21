package actionpolicy

import (
	"context"
	"regexp"
	"sort"
)

const defaultUnclassifiedLabel = "restricted"

// Classifier assigns labels to a tool call and to its result.
// An error from ClassifyAction or ClassifyResult does not deny the call.
// The agent uses the classifier's unclassified labels instead.
type Classifier interface {
	ID() string
	ClassifyAction(ctx context.Context, action Action) ([]string, error)
	ClassifyResult(ctx context.Context, action Action, result string) ([]string, error)
}

// ClassifierConfig is the static label map for [NewClassifier].
type ClassifierConfig struct {
	// Tools maps a tool name to the labels applied to that call.
	// A tool with no entry gets [ClassifierConfig.Unclassified].
	Tools map[string][]string
	// Patterns maps a label to a regular expression.
	// ClassifyAction tests it against the canonical JSON of the arguments.
	// ClassifyResult tests it against the result text.
	Patterns map[string]*regexp.Regexp
	// Unclassified is used when a tool has no entry in Tools.
	// Nil means ["restricted"]. An empty slice means the call gets no labels.
	// Use the highest-sensitivity label as the default.
	Unclassified []string
}

type nativeClassifier struct {
	id           string
	tools        map[string][]string
	patterns     map[string]*regexp.Regexp
	patternKeys  []string
	unclassified []string
}

// NewClassifier returns a classifier that labels calls from cfg.
// id is recorded in the agent fingerprint. An empty id is rejected by agent validation
// when the classifier is registered; NewClassifier itself allows any id.
func NewClassifier(id string, cfg ClassifierConfig) Classifier {
	tools := make(map[string][]string, len(cfg.Tools))
	for name, labels := range cfg.Tools {
		tools[name] = copyStrings(labels)
	}
	patterns := make(map[string]*regexp.Regexp, len(cfg.Patterns))
	keys := make([]string, 0, len(cfg.Patterns))
	for label, re := range cfg.Patterns {
		if re == nil || label == "" {
			continue
		}
		patterns[label] = re
		keys = append(keys, label)
	}
	sort.Strings(keys)
	return &nativeClassifier{
		id:           id,
		tools:        tools,
		patterns:     patterns,
		patternKeys:  keys,
		unclassified: normalizeUnclassified(cfg.Unclassified),
	}
}

func (c *nativeClassifier) ID() string { return c.id }

// UnclassifiedLabels returns the labels used for a tool that is not listed.
func (c *nativeClassifier) UnclassifiedLabels() []string {
	return copyStrings(c.unclassified)
}

func (c *nativeClassifier) ClassifyAction(_ context.Context, action Action) ([]string, error) {
	labels, ok := c.tools[action.Tool]
	if !ok {
		labels = c.unclassified
	}
	out := copyStrings(labels)
	if len(c.patternKeys) == 0 {
		return out, nil
	}
	encoded, err := CanonicalJSON(action.Args)
	if err != nil {
		return copyStrings(c.unclassified), nil
	}
	return appendPatternLabels(out, c.patternKeys, c.patterns, string(encoded)), nil
}

func (c *nativeClassifier) ClassifyResult(_ context.Context, _ Action, result string) ([]string, error) {
	if len(c.patternKeys) == 0 {
		return nil, nil
	}
	return appendPatternLabels(nil, c.patternKeys, c.patterns, result), nil
}

func normalizeUnclassified(in []string) []string {
	if in == nil {
		return []string{defaultUnclassifiedLabel}
	}
	return copyStrings(in)
}

func appendPatternLabels(dst []string, keys []string, patterns map[string]*regexp.Regexp, text string) []string {
	for _, label := range keys {
		re := patterns[label]
		if re == nil || !re.MatchString(text) {
			continue
		}
		if containsString(dst, label) {
			continue
		}
		dst = append(dst, label)
	}
	return dst
}

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// UnionLabels returns dst followed by any values in extra that are not already present.
func UnionLabels(dst, extra []string) []string {
	if len(extra) == 0 {
		return dst
	}
	out := append([]string(nil), dst...)
	for _, label := range extra {
		if label == "" || containsString(out, label) {
			continue
		}
		out = append(out, label)
	}
	return out
}
