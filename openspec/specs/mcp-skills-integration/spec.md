# Specification: MCP Skills Integration

## 1. Overview
This specification defines the Model Context Protocol (MCP) skills integration capability in Claworc. It enables dynamic management and orchestration of containerized (SSE transport) and agent-local process (Stdio transport) MCP servers via Claworc Skills.

## 2. Requirements

### 2.1 Metadata Parsing & Validation
- **REQ-1**: The control plane MUST parse the `mcp` frontmatter block from `SKILL.md` when a skill is deployed or updated.
- **REQ-2**: The control plane MUST resolve environment variable placeholders matching the pattern `{{VAR_NAME}}` using the target agent instance's environment variables before launching or configuring the MCP server.

### 2.2 Orchestration & Lifecycle Management
- **REQ-3**: For MCP servers using `sse` transport, the control plane MUST orchestrate a sidecar container using the configured Docker image, arguments, and resolved environment variables.
- **REQ-4**: Sibling/sidecar containers MUST be deployed on the same virtual network as the agent instance container.
- **REQ-5**: For MCP servers using `stdio` transport, the agent container MUST execute the local process command and arguments as child processes using standard input/output.
- **REQ-6**: The control plane MUST invoke `openclaw mcp add` to register the MCP server with the agent instance upon deployment.
- **REQ-7**: The control plane MUST invoke `openclaw mcp unset` (or deregister equivalent) and stop/delete any associated sidecar container when the skill is undeployed or deleted.

## 3. Scenarios

### Scenario 1: Deploying a Docker-based (SSE) MCP Skill
**Given** a skill defining a Docker-based MCP server in `SKILL.md` with `transport: sse` and an environment variable placeholder `{{DB_URL}}`
**And** the target agent instance has the environment variable `DB_URL` set to `postgresql://localhost/db`
**When** the user deploys the skill
**Then** the control plane MUST resolve the placeholder to `postgresql://localhost/db`
**And** the orchestrator MUST start a sidecar container running the specified Docker image with the resolved environment variables on the same network
**And** the control plane MUST execute `openclaw mcp add` to register the server using the sidecar's SSE endpoint URL

### Scenario 2: Deploying a Local Process (Stdio) MCP Skill
**Given** a skill defining a local-process MCP server in `SKILL.md` with `transport: stdio`
**When** the user deploys the skill
**Then** the control plane MUST copy the skill files to the agent instance
**And** the control plane MUST execute `openclaw mcp add` using the SSH client, specifying the process command, arguments, and resolved environment variables

### Scenario 3: Undeploying an MCP Skill
**Given** an active skill that registers an MCP server and has an associated running sidecar container
**When** the user undeploys the skill
**Then** the control plane MUST execute the command to deregister the MCP server from the agent
**And** the control plane MUST stop and delete the associated sidecar container
