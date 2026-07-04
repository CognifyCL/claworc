# Exploration: Agent Skills Management

This document explores the implementation of a new feature: **Agent Skills Management**. The objective is to allow users to see the list of active/deployed skills in the UI of each agent (inside [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx)) and to view, debug, and edit these skills directly on the agent from the web interface.

---

## 1. Goal & Requirements

1. **Agent-Specific Skills Visibility**:
   - Provide a new "Skills" tab in [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx).
   - Display a list of deployed skills for the selected agent.
   - Show status badges (e.g., Active/Running, Stopped, Error) for each skill, particularly for Model Context Protocol (MCP) servers.

2. **Live Editing & Debugging**:
   - Allow users to view and edit files inside a deployed skill's directory (located at `/home/claworc/.openclaw/skills/{slug}`) directly from the agent detail view.
   - Allow users to view logs specifically for containerized (SSE transport) MCP skill sidecars.
   - Gracefully handle situations where the agent is stopped or SSH is disconnected.

3. **Security & Robustness (Judgment Day Adversarial Findings)**:
   - **Zero Python3 Dependency on Target Agents**: Avoid spawning Python3 processes or executing Python scripts on the target agent instances. Standard POSIX-compliant commands (`find`, `file`, `stat`, etc.) must be used for file operations and path traversals.
   - **Shell Injection & Directory Traversal Protection**: Ensure strict path validation, regex-based skill slug validation, and proper shell-argument escaping via `shellQuote` to prevent shell injection vulnerabilities.
   - **Skip Dependency & Build Folders**: Avoid performance bottlenecks by skipping common package/virtual environment directories (e.g., `node_modules`, `.venv`, `.git`) when walking/searching skill directories.
   - **Resource Limits & Buffer Safety**: Limit editable/readable skill files to a maximum size of 2MB, and use a safe line-scanning buffer (at least 2MB) when streaming logs to handle large log lines without crashing.
   - **Orphan Workload Cleanup**: Automatically clean up and garbage-collect containerized sidecar workloads by querying `InstanceSkill` DB records to resolve any synchronization mismatches (e.g., due to network drops during undeployment).

---

## 2. Existing Architecture & Code Analysis

### 2.1 Backend Codebase
- **Skills Lifecycle**: Managed in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go). Currently, [DeploySkill](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L831) parses the `SKILL.md` frontmatter, starts a sidecar workload (if `mcp` transport is SSE) via the [ContainerOrchestrator](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go), and registers the skill on the instance using `openclaw mcp add` over SSH.
- **SSH File Interactions**: [sshproxy/files.go](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go) provides SSH-based primitives such as [ListDirectory](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go#L186), [ReadFile](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go#L201), and [WriteFile](file:///home/ubuntu/claworc/control-plane/internal/sshproxy/files.go#L218) to interact with the instance's filesystem.
- **API Routing**: Defined in [main.go](file:///home/ubuntu/claworc/control-plane/main.go). Global skills are accessed via `/skills/...` routes. Instance-specific file editing is handled via `/instances/{id}/files/...` routes in [files.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/files.go).

### 2.2 Frontend Codebase
- **Agent Detail Layout**: [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx) organizes agent interaction via a `tabs` system (Chat, Terminal, Files, Config, Logs, Settings).
- **Skills Library**: [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx) shows global/clawhub skills and triggers deployment.
- **Editing Interface**: [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) provides a Monaco-editor interface to view and edit files. It uses react-query hooks in [useSkills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/hooks/useSkills.ts) and API callers in [skills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/api/skills.ts) pointing to the global `/skills/...` library routes.

---

## 3. Design Approaches & Trade-offs

We evaluate three approaches to fetch and manage deployed skills per agent:

### Approach A: Stateless SSH-driven Live Querying
- **Mechanism**: The backend does not store deployed skills in the DB. When requested, it queries the running instance via SSH using `sshproxy.ListDirectory` on `/home/claworc/.openclaw/skills/` and parses each `SKILL.md` in real-time.
- **Pros**: Zero DB modifications or synchronization issues.
- **Cons**: 
  - Fails completely when the agent is stopped.
  - High latency (multiple SSH roundtrips to read `SKILL.md` frontmatter for all deployed skills).

### Approach B: Stateful DB-Only Tracking
- **Mechanism**: The control plane uses a new database table `instance_skills` to keep track of deployed skills.
- **Pros**: Extremely fast; works when the agent is stopped.
- **Cons**: Can fall out of sync if files are manually modified or deleted on the agent's persistent volume (e.g., via the terminal or file browser).

### Approach C: Hybrid DB Tracking + SSH Sync (Recommended)
- **Mechanism**: 
  - If the agent is running and SSH is connected, the control plane queries `/home/claworc/.openclaw/skills/` via SSH, reconciles the list of directories with the DB `instance_skills` table, and automatically updates the DB state to match the disk.
  - If the agent is stopped or SSH is disconnected, the control plane returns the list of deployed skills from the DB.
  - File viewing, editing, and saving always happen directly on the agent's disk via SSH.
- **Pros**: Highly resilient (works offline), always accurate when online, and enables direct filesystem editing without polluting the global library.
- **Cons**: Slightly higher implementation complexity.

---

## 4. Proposed Implementation Plan

### 4.1 Database Updates
We will add an `InstanceSkill` GORM model in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go):

```go
type InstanceSkill struct {
	InstanceID uint      `gorm:"primaryKey;autoIndex" json:"instance_id"`
	SkillSlug  string    `gorm:"primaryKey;size:255" json:"skill_slug"`
	DeployedAt time.Time `gorm:"autoCreateTime" json:"deployed_at"`
}
```

This model will be registered in migrations and DB schema checks. On successful deployment in `DeploySkill`, a record is added; on undeployment, it is removed. This model serves as the registry of record for containerized sidecar auditing.

### 4.2 Backend API Routes ([main.go](file:///home/ubuntu/claworc/control-plane/main.go))
We will register new instance-specific skill management endpoints:

```go
r.Route("/instances/{id}/skills", func(r chi.Router) {
    r.Get("/", handlers.ListInstanceSkills)
    r.Get("/{slug}/files", handlers.ListInstanceSkillFiles)
    r.Get("/{slug}/files/*", handlers.GetInstanceSkillFile)
    r.Put("/{slug}/files/*", handlers.PutInstanceSkillFile)
    r.Get("/{slug}/logs", handlers.StreamInstanceSkillLogs)
})
```

### 4.3 Backend Handlers

1. **`ListInstanceSkills`**:
   - Fetches the agent instance.
   - If connected via SSH, lists subdirectories under `/home/claworc/.openclaw/skills/`. Reconciles and updates `InstanceSkill` in the DB.
   - **Path & Slug Validation**: The `slug` must match `^[a-zA-Z0-9_-]+$` to prevent directory traversal or command injection.
   - For each skill: loads metadata. If it matches a global library skill, uses DB metadata; otherwise, parses the remote `SKILL.md` frontmatter.
   - For SSE-based MCP skills, queries the container status from the [ContainerOrchestrator](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go).

2. **`ListInstanceSkillFiles` / `GetInstanceSkillFile` / `PutInstanceSkillFile`**:
   - Replicates the logic of the global skill handlers but redirects reads/writes to `/home/claworc/.openclaw/skills/{slug}` using the instance's `sshproxy` client.
   - **No Python3 Dependency**: Uses strictly POSIX shell commands for directory traversal and metadata retrieval to avoid dependencies on python3:
     - Directory contents and recurse: `find <dir> ...`
     - File MIME type identification: `file -b --mime-type <path>`
     - Path attributes: `stat -c '%F' <path>`
   - **Pruning Package & Venv Directories**: When walking directories to search or list skill files, explicitly exclude common package manager and virtual environment folders.
     - POSIX Command: `find <skill_dir> \( -name node_modules -o -name .venv -o -name .git \) -prune -o -type f -print`
   - **Shell Injection & Path Traversal Prevention**:
     - Strict slug validation: `slug` must match `^[a-zA-Z0-9_-]+$`.
     - Strict path validation: Relative file paths must be normalized using `filepath.Clean` and verified to verify that they are bounded within `/home/claworc/.openclaw/skills/{slug}` (i.e. `strings.HasPrefix(targetPath, skillRootDir)`).
     - Shell argument passing: Wrap all path parameters and external arguments in `sshproxy.shellQuote` before passing them to SSH execution functions.
   - **File Size limits**: Enforce a maximum file size limit of **2MB** in both `GetInstanceSkillFile` and `PutInstanceSkillFile`. If a file exceeds 2MB, reject the read/write request with a `413 Payload Too Large` error.

3. **`StreamInstanceSkillLogs`**:
   - Queries the logs of the sidecar workload (`mcp-{id}-{slug}`) via the orchestrator and streams them back to the client using SSE.
   - **Large Log Lines Safe Buffer**: Configure the log scanner with a 2MB maximum token buffer (default is 64KB) to prevent crashes when processing large log outputs (e.g. detailed LLM traces or long JSON payloads):
     ```go
     scanner := bufio.NewScanner(logReader)
     buf := make([]byte, 64*1024)
     scanner.Buffer(buf, 2*1024*1024) // up to 2MB buffer limit
     ```
   - We will extend the `ContainerOrchestrator` interface in [orchestrator.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go) to support streaming workload logs:
     ```go
     StreamWorkloadLogs(ctx context.Context, name string, tail int, follow bool, writer io.Writer) error
     ```

4. **Sidecar Workload Cleanup Daemon/Hook**:
   - Introduce a background reconciliation job or pre-stop lifecycle hook that queries running containers on the container/workload orchestrator.
   - Query all active containerized sidecar workloads with names matching `mcp-{instance_id}-{slug}`.
   - Validate each running workload against the `InstanceSkill` database table.
   - If a sidecar container exists for a skill or instance that has been undeployed, stopped, or deleted (i.e. no matching `InstanceSkill` record exists), invoke `DeleteWorkload` to clean up the container and its non-shared volumes.

### 4.4 Frontend Updates
1. **Tabs and Routing in [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx)**:
   - Add `"skills"` to the `Tab` type and add `{ key: "skills", label: "Skills" }` to the tab bar.
   - Add a panel `{activeTab === "skills" && ...}`.
2. **Skills Panel Component**:
   - Render a list of deployed skills.
   - Include action buttons for **Edit**, **Undeploy**, **View MCP Logs** (for SSE skills), and a warning badge/action if the skill has local edits or is out-of-sync with the library.
3. **Live Skill Editor Modal**:
   - Adapt [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) to accept an optional `instanceId`.
   - Implement query hooks in `useSkills.ts` that point to the `/instances/{id}/skills/...` endpoints when `instanceId` is provided. This allows direct, isolated edits of files on the agent.
4. **Logs Viewer Modal/Pane**:
   - Provide a side-by-side or modal console for streaming sidecar container logs.

---

## 5. Verification & Test Plan

1. **Unit Testing**:
   - Add unit tests in `control-plane/internal/handlers/skills_test.go` mocking SSH connections to verify that folder listings and remote file edits map correctly.
   - **Path & Slug Validation Tests**: Assert that inputs containing malicious shell characters (e.g. `;`, `&`, `|`, `` ` ``), or traversal elements (e.g. `../../etc`) are rejected immediately without executing any SSH commands.
   - **File Size Limit Tests**: Mock files larger than 2MB and verify that `GetInstanceSkillFile` and `PutInstanceSkillFile` return a `413 Payload Too Large` error.
   - **Large Log Line Scan Tests**: Feed a mock log stream containing lines of 100KB, 500KB, and 1.5MB to verify that the scanner handles them correctly without raising `bufio.ErrTooLong`.
   - **Workload Reconciliation Tests**: Verify that the cleanup daemon correctly detects and terminates a sidecar container when the corresponding `InstanceSkill` DB row is missing.

2. **Integration & Adversarial Testing**:
   - Spin up a test instance lacking Python3 entirely. Deploy an MCP skill, edit files live on the instance via the new endpoints, and verify that all directory listing, file retrieval, and edits function without python dependencies.
   - Create a skill directory with `node_modules`, `.venv`, and `.git` folders. Verify that these folders are completely pruned and excluded from the file listing and file search results.
   - Simulate a network disconnect during an undeployment command, verify that the database record is removed, and assert that the cleanup daemon destroys the orphaned sidecar container.
