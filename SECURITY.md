# Security Policy

## Supported Versions

Security fixes are applied to the **default branch** (`main`) and released as **semver tags** (`vMAJOR.MINOR.PATCH`). Treat the **[latest GitHub release](https://github.com/agenticenv/agent-sdk-go/releases/latest)** as the current supported line.

- **Latest release:** full support (including security fixes).
- **Older tags:** we do not guarantee long-term support for every past version; upgrade to the latest release when possible.

Maintainers document breaking changes in release notes. See [RELEASING.md](RELEASING.md) for how versions are cut.

## Reporting a Vulnerability

Please report security vulnerabilities by opening a [GitHub Security Advisory](https://github.com/agenticenv/agent-sdk-go/security/advisories/new). Do not open a public issue for security vulnerabilities.

We will acknowledge your report within 48 hours and will send a more detailed response within 7 days. Please do not publicly disclose the vulnerability until we have released a fix.

We appreciate responsible disclosure and will acknowledge security researchers who help us improve the security of this project (with their permission). We do not currently offer a bug bounty or monetary rewards for vulnerability reports.

## Scope

- Security issues in this SDK (agent, tools, conversation, LLM clients)
- Sensitive data exposure (API keys, approval payloads)

## Security Considerations for Agent Deployments

When deploying agents built with this SDK, review the following application-level risks; the SDK provides the hooks below, but callers own validation and access control.

### Prompt Injection

Agent tools execute based on LLM decisions. Validate and sanitize all external data before injecting into agent context. Use `WithHooks(name, AgentHooks)` with a `BeforeLLM` hook to inspect and filter inputs.

### Prompt Caching Data Residency

When prompt caching is enabled (Anthropic), prefix KV state is stored server-side at the provider. Leave caching disabled (default) or disable explicitly with `llm.WithPromptCaching(false)`.

### MCP Server Trust

MCP stdio transport spawns local processes. Only register MCP servers from trusted sources. Validate MCP server binaries before registering via `WithMCPConfig()`.

### API Key Handling

Never pass API keys through agent prompts or tool inputs. Use environment variables or secret managers. Keys may be held in process memory via `llm.WithAPIKey`; the SDK does not persist them to disk or put them in prompts.

### Tool Authorization

Implement `interfaces.ToolAuthorizer` (`Authorize`) to enforce access control before tool execution. For human-sensitive operations, implement `interfaces.ToolApproval` (`ApprovalRequired`) and/or use `WithToolApprovalPolicy` with `WithApprovalHandler`.

### Third-party Dependencies

Key dependencies — monitor via **Dependabot** (`.github/dependabot.yml`, weekly PRs for `/` and `/cli`) and **`govulncheck`** (`task govuln`, Security workflow, and Release hard-fail before GoReleaser):

- go.temporal.io/sdk
- github.com/restatedev/sdk-go
- github.com/modelcontextprotocol/go-sdk
- github.com/a2aproject/a2a-go/v2

## Out of Scope

- Temporal server or Temporal Cloud
- Restate server or Restate Cloud
- Third-party LLM providers (OpenAI, Anthropic, Google)
- General usage questions
