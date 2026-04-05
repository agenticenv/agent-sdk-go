package types

// SubAgentToolParamQuery is the tool/JSON parameter name for the query sent to a sub-agent.
const SubAgentToolParamQuery = "query"

// SubAgentRoute tells the runtime how to delegate to a sub-agent (child run on TaskQueue),
// with nested routes for that sub-agent's sub-agents (frozen at parent run start).
type SubAgentRoute struct {
	Name        string                   `json:"name"`
	TaskQueue   string                   `json:"task_queue"`
	ChildRoutes map[string]SubAgentRoute `json:"child_routes,omitempty"`
}
