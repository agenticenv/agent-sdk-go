package types

// subAgentToolParamQuery is the parameter name for the query to send to the sub-agent.
const SubAgentToolParamQuery = "query"

// SubAgentRoute tells the workflow how to delegate to a sub-agent: child AgentWorkflow on TaskQueue,
// with nested routes for that sub-agent's own sub-agents (frozen at parent run start).
type SubAgentRoute struct {
	Name        string                   `json:"name"`
	TaskQueue   string                   `json:"task_queue"`
	ChildRoutes map[string]SubAgentRoute `json:"child_routes,omitempty"`
}
