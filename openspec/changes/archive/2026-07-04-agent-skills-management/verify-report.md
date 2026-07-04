# Verification Report: Agent Skills Management

This report details the verification of the **Agent Skills Management** capability in the control plane backend and frontend.

## Verdict: PASS WITH WARNINGS

All core capabilities, file operations, DB/SSH synchronization, and sidecar log streaming functionality are fully implemented and verified via automated testing. Both backend and frontend projects build successfully without errors. 

> [!WARNING]
> **Warning**: **REQ-14** (Background Workload Cleanup Daemon) is not fully implemented in the Go codebase. While cascade deletion is successfully executed on explicit actions (such as undeploying a skill, updating `SKILL.md` transport, or deleting an instance), a periodic background reconciliation daemon to clean up orphans was omitted. All other specifications, requirements, and tasks are complete.

---

## 1. Build Verification

### Backend Compilation & Tests
- **Backend Build**: Compiled successfully.
  - Command: `go build -o /dev/null ./...` (Completed with exit code 0)
- **Backend Tests**: All 40+ tests in `./internal/handlers/` and the rest of the workspace passed successfully when using `CGO_ENABLED=1`.
  - Command: `CGO_ENABLED=1 go test -v ./...` (Passed in 14.45s)

### Frontend Compilation
- **Frontend Build**: Vite build compiled client environment for production successfully with zero TypeScript compilation errors or build warnings.
  - Command: `npm run build` inside `control-plane/frontend` (Completed with exit code 0)
  - Output Assets:
    - `dist/assets/index-0hs3jpFg.css` (160.73 kB)
    - `dist/assets/index-BMu30KVL.js` (2,018.38 kB)

---

## 2. Task Completion Verification (`tasks.md`)

All tasks defined in [tasks.md](file:///home/ubuntu/claworc/openspec/changes/agent-skills-management/tasks.md) are marked completed (`[x]`). Below is the verification status of each phase:

*   **Phase 1: DB/Migrate (1.1 - 1.5) [x]**: Verified the GORM model `InstanceSkill` exists in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go), is re-exported, and is registered in Goose migration `v9` and the `migrationcheck` tool.
*   **Phase 2: Orchestrator (2.1 - 2.4) [x]**: Verified interface upgrade of `ContainerOrchestrator` to include `StreamWorkloadLogs`. Verified Docker, Kubernetes, and Mock orchestrator implementations.
*   **Phase 3: Backend API (3.1 - 3.8) [x]**: Verified all 5 new routes are registered in `main.go` and implemented in `skills.go` with regex slug validation, remote path sandboxing, 2MB size cap, and SSE streaming with a 2MB maximum token buffer.
*   **Phase 4: Frontend (4.1 - 4.5) [x]**: Verified frontend types, React Query hooks, `SkillEditorModal` parameterization, and integration of the "Skills" tab in `AgentDetailPage.tsx` with offline cached support and live sidecar log streaming.
*   **Phase 5: Testing/Verify (5.1 - 5.3) [x]**: Verified comprehensive testing in `skills_test.go` and successful builds.

---

## 3. Spec Scenario Compliance Mapping

The automated Go tests in [skills_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills_test.go) map directly to the scenarios specified in the capability spec:

| Spec Scenario | Test Name / Location | Verification Method & Logic | Status |
| :--- | :--- | :--- | :--- |
| **Scenario 1: Fetching Skills List (Online)** | `TestInstanceSkillHandlers_HappyAndEdge` / `ListInstanceSkills_OnlineAndOffline` | Connects via mock SSH connection, calls `ListInstanceSkills` handler, verifies directory is scanned, frontmatter parsed, and records updated in GORM `InstanceSkill` DB. | **PASS** |
| **Scenario 2: Fetching Skills List (Offline)** | `TestInstanceSkillHandlers_HappyAndEdge` / `ListInstanceSkills_OnlineAndOffline` | Closes mock SSH connections, runs `ListInstanceSkills` handler, and verifies it successfully falls back to cached GORM DB records. | **PASS** |
| **Scenario 3: Editing a Remote Skill File** | `TestInstanceSkillHandlers_HappyAndEdge` / `PutInstanceSkillFile_AndConfigReload` | Puts content into `SKILL.md` via `PutInstanceSkillFile`, verifies it writes using `sshproxy`, detects config transport changes, and triggers dynamic sidecar setup. | **PASS** |
| **Scenario 4: Streaming Logs with Large Lines** | `TestStreamInstanceSkillLogs_ScannerBuffer` | Configures SSE logs endpoint with a mock stream yielding a 1.5MB log line, verifying the `bufio.Scanner` with 2MB maximum token size streams it without crashing. | **PASS** |
| **Scenario 5: Path Sandbox and Shell Injection Protection** | `TestIsValidSlug`, `TestResolveRemoteSkillFilePath`, `TestInstanceSkillHandlers_RoutingAndValidation` | Asserts invalid slug validation (rejections on `/`, `..`, empty), path containment, and that all 5 handlers return HTTP 400 Bad Request on invalid slugs. | **PASS** |
| **Scenario 6: Cascade Deletion of Sidecars and Orphan Cleanup** | `TestDeployAndUndeployMCPWorkflow` / `sse` | Deploys a skill using SSE transport (which calls `Apply` workload), runs the `UndeploySkill` HTTP handler, and verifies the orchestrator `DeleteWorkload` is invoked. | **PASS (With Warnings)** |

---

## 4. Design Coherence Verification

Code structures mirror the architectural design specified in [design.md](file:///home/ubuntu/claworc/openspec/changes/agent-skills-management/design.md):
- **Model Definition**: GORM model matches section 2.1 exactly, including foreign key cascades.
- **Goose Migration**: Database schema v9 creates the table and unique constraint correctly.
- **Routing**: Chi router paths registered in `main.go` match section 3.
- **Sandbox Protection**: Remote path walks are restricted correctly. `find` command matches section 3.2's design (no Python3 dependency, prunes `.git`, `.venv`, and `node_modules`, checks for binary files using `dd` and `tr`).
- **SSE streaming & Docker/Kubernetes integration**: Interface upgrades match section 3.4.

---

## 5. Summary of Findings & Actionable Recommendations

### Findings
1. **Missing Background Workload Daemon (REQ-14)**: The codebase currently deletes containers on explicit events: `UndeploySkill`, `PutInstanceSkillFile` (when transport changes), and `DeleteInstance` (when the agent is deleted). However, there is no background thread checking for container discrepancies (e.g. containers that exist in Docker/K8s but do not have an active database row in `InstanceSkill`).
2. **Robust Sandbox Validation**: Absolute remote path validation is strongly implemented via `resolveRemoteSkillFilePath`, completely neutralizing potential directory traversal injection (`..` attacks).
3. **CGO Requirement**: Go database sqlite tests require CGO to run successfully, which is correctly handled by invoking tests with `CGO_ENABLED=1`.

### Recommendations
1. **Implement Workload Reconciler**: In a future phase, add a lightweight worker loop in `main.go` that periodically runs a cleanup task. This task should query active workloads matching `mcp-*` via `orchestrator.ListWorkloads` (or similar interface) and compare them with `InstanceSkill` DB records, invoking `DeleteWorkload` on unmatched ones.
2. **Keep CGO Flag in CI**: Ensure that the project CI/CD configuration defines `CGO_ENABLED=1` for Go test suites to avoid SQLite stub runtime crashes.
