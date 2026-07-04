# Proposal: Manage MCP Skills

## Intent
Claworc needs to support Model Context Protocol (MCP) servers packaged as Skills. This proposal enables dynamic lifecycle management and execution of both containerized (SSE transport) and local process (Stdio transport) MCP servers.

## Scope

### In Scope
- Parse the new `mcp` frontmatter block in `SKILL.md`.
- Resolve environment variable placeholders (`{{VAR}}`) in MCP configs.
- Orchestrate sibling/sidecar containers for Docker-based MCP servers using SSE.
- Launch and monitor local process-based MCP servers inside agent containers using Stdio.
- Register/deregister MCP servers in OpenClaw using `openclaw mcp`.

### Out of Scope
- Direct Docker socket mounting in agent containers (Approach C).
- Automatic installation of runtime environments (e.g., node, python) in agent containers.

## Capabilities

### New Capabilities
- `manage-mcp-skills`: Declarative management, env resolution, and lifecycle orchestration of containerized (SSE) and local process (Stdio) MCP servers via Skills.

### Modified Capabilities
None

## Approach
A Hybrid Solution:
1. **Sidecar Containers (SSE)**: The control plane orchestrates sibling Docker/K8s containers on the same network, then runs `openclaw mcp add` using SSE.
2. **Local Processes (Stdio)**: The agent runs `openclaw mcp add` via SSH to run Stdio-based servers as local child processes.

## Affected Areas

| Area | Impact | Description |
|------|--------|-------------|
| `control-plane/internal/handlers/skills.go` | Modified | Parse MCP metadata and trigger sidecar/process lifecycle. |
| `control-plane/internal/orchestrator/` | Modified | Add `ApplyMCPSidecar` and `DeleteMCPSidecar` to orchestrator interface/backends. |

## Risks

| Risk | Likelihood | Mitigation |
|------|------------|------------|
| Port conflicts | Low | Dynamically allocate ports on the bridge network. |
| Runtime missing for local | Medium | Document pre-requisites and validation. |

## Rollback Plan
Remove the `mcp` parsing logic in `skills.go` and revert the `ContainerOrchestrator` interface changes. Existing skills will ignore the `mcp` block.

## Dependencies
- OpenClaw CLI support for `openclaw mcp` commands.

## Success Criteria
- [ ] Control plane parses `SKILL.md` with `mcp` configuration block.
- [ ] Docker/K8s orchestrator starts/stops sidecar containers on skill deploy/undeploy.
- [ ] OpenClaw agents connect to SSE sidecars or run Stdio processes.

## Proposal question round
1. Should runtime requirements (e.g. Node/Python) for local MCP processes be checked during skill validation, or assumed present?
2. Do we support scaling/replicas for sidecar containers, or is it strictly 1-to-1?
3. Should MCP environment variables be configured solely via existing instance environment variables, or do we need a separate UI?
4. How should Claworc handle/display failures when an MCP server crashes?
