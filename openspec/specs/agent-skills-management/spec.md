# Specification: Agent Skills Management

## 1. Overview
This specification defines the Agent Skills Management capability. It enables real-time visibility, live editing of remote skill files, and log streaming for deployed Model Context Protocol (MCP) skills directly on specific agent instances via the web interface.

## 2. Requirements

### 2.1 State Tracking & Synchronization
- **REQ-1**: The system MUST store agent-specific skill deployment records in the database using the [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) model.
- **REQ-2**: When online, the control plane MUST query active remote skills via SSH and sync them with the [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) table.
- **REQ-3**: When offline, the control plane MUST return cached records from the [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) table.

### 2.2 Remote Skill File Editing & Path Resolution
- **REQ-4**: The frontend MUST expose file view and edit capabilities within the "Skills" tab of [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx) using [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx).
- **REQ-5**: File edits MUST route via `/instances/{id}/skills/{slug}/files/...` and write via [sshproxy](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go).
- **REQ-6**: Modifying `SKILL.md` MUST trigger the control plane to automatically reload the skill's MCP configuration.
- **REQ-7**: The system MUST reject file operations and log streams if the target agent instance is offline.
- **REQ-8**: To prevent command injection and traversal, the system MUST validate slugs against `^[a-zA-Z0-9_-]+$` and restrict resolved paths within the skill root directory.
- **REQ-9**: The control plane MUST escape SSH parameters via [shellQuote](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/logs.go#L276) and enforce a 2MB file size limit.

### 2.3 Sidecar Log Streaming
- **REQ-10**: The control plane MUST expose `/instances/{id}/skills/{slug}/logs` via Server-Sent Events (SSE).
- **REQ-11**: The [ContainerOrchestrator](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go#L11) MUST implement a method to stream real-time logs of the SSE transport MCP sidecar container.
- **REQ-12**: The log scanner MUST use a 2MB maximum token buffer to handle large log lines without crashing.

### 2.4 Workload Cleanup & Deletion
- **REQ-13**: The system MUST cascade delete the sidecar container `mcp-{instance_id}-{slug}` and non-shared volumes when a skill is undeployed or transport changes.
- **REQ-14**: A background reconciliation job MUST periodically query active container workloads, matching them against [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) DB records and deleting orphans.

## 3. Scenarios

### Scenario 1: Fetching Skills List (Online)
**Given** an agent is reachable via SSH
**When** the user accesses the "Skills" tab on [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx)
**Then** the control plane MUST query active remote skills, update [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) records, and display them.

### Scenario 2: Fetching Skills List (Offline)
**Given** an agent is unreachable
**When** the user accesses the "Skills" tab on [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx)
**Then** the control plane MUST return cached [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) records.

### Scenario 3: Editing a Remote Skill File
**Given** an online agent instance
**When** a user edits a file via [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx)
**Then** the backend MUST write changes using [sshproxy](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go), and if `SKILL.md` was edited, reload MCP configuration.

### Scenario 4: Streaming Logs with Large Lines
**Given** a sidecar container generating lines up to 2MB
**When** the user views the logs of the skill
**Then** the backend MUST stream logs via SSE without crashing.

### Scenario 5: Path Sandbox and Shell Injection Protection
**Given** a path traversal (`..`) or invalid slug characters in a request
**When** the request is processed
**Then** the system MUST reject the request and execute no SSH commands.

### Scenario 6: Cascade Deletion of Sidecars and Orphan Cleanup
**Given** a sidecar container lacking a matching DB record
**When** the reconciliation daemon runs or undeployment is triggered
**Then** the system MUST delete the container and its non-shared volumes.
