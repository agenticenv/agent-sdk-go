package tools

import (
	"sync"

	"github.com/vvsynapse/agent-sdk-go/pkg/interfaces"
)

var _ interfaces.ToolRegistry = (*Registry)(nil)

// Registry is an in-memory ToolRegistry implementation.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]interfaces.Tool
	order []string // preserve registration order for Tools()
}

// NewRegistry returns a new empty ToolRegistry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]interfaces.Tool),
		order: nil,
	}
}

// Register adds a tool. Overwrites if a tool with the same name exists.
func (r *Registry) Register(tool interfaces.Tool) {
	if tool == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if _, exists := r.tools[name]; !exists {
		r.order = append(r.order, name)
	}
	r.tools[name] = tool
}

// Get returns the tool by name, or (nil, false) if not found.
func (r *Registry) Get(name string) (interfaces.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Tools returns all registered tools in registration order.
func (r *Registry) Tools() []interfaces.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]interfaces.Tool, 0, len(r.order))
	for _, name := range r.order {
		if t, ok := r.tools[name]; ok {
			result = append(result, t)
		}
	}
	return result
}
