# Review Workload Forecast

- 400-line budget risk: High
- Chained PRs: Yes
- Decision needed before apply: No

---

## Phase 1: DB/Migrate
- [x] 1.1 Define the GORM model struct [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) with fields `ID`, `InstanceID` (part of unique index `idx_instance_skill_slug` and foreign key referencing `Instance` with constraint `OnDelete:CASCADE`), `Slug` (part of unique index `idx_instance_skill_slug`), `Name`, `Summary`, `Status` (default `"deployed"`), `CreatedAt`, and `UpdatedAt`.
- [x] 1.2 Add the type alias `InstanceSkill = models.InstanceSkill` to [models.go (parent)](file:///home/ubuntu/claworc/control-plane/internal/database/models.go) to re-export the model.
- [x] 1.3 Create Goose database migration file [migration_00009_create_instance_skills.go](file:///home/ubuntu/claworc/control-plane/internal/database/migrations/migration_00009_create_instance_skills.go) to handle Up/Down migration of `InstanceSkill` table and the unique index `idx_instance_skill_slug`.
- [x] 1.4 Register the model in `AutoMigrateAll` inside [migration_00001_baseline.go](file:///home/ubuntu/claworc/control-plane/internal/database/migrations/migration_00001_baseline.go).
- [x] 1.5 Register the model in `allMigratedModels` inside [main.go](file:///home/ubuntu/claworc/control-plane/cmd/migrationcheck/main.go).

## Phase 2: Orchestrator
- [x] 2.1 Upgrade the [ContainerOrchestrator](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go) interface in [orchestrator.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go) to include the [StreamWorkloadLogs](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go) method to stream container logs for sidecar workloads.
- [x] 2.2 Implement [StreamWorkloadLogs](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/docker.go) in the Docker Orchestrator inside [docker.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/docker.go) using Docker's `ContainerLogs` client method and stdcopy-based stdout/stderr multiplexing.
- [x] 2.3 Implement [StreamWorkloadLogs](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes.go) in the Kubernetes Orchestrator inside [kubernetes.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes.go) by resolving the pod name from labels and using the client's `GetLogs().Stream()` API.
- [x] 2.4 Consolidate testing mocks: Rename `mockOps` in [configure_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/configure_test.go) to `mockOrchestrator` and merge it with the mock defined in [ssh_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/ssh_test.go) into a single reusable struct that implements [ContainerOrchestrator](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go), adding a mock implementation for [StreamWorkloadLogs](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go).

## Phase 3: Backend API
- [x] 3.1 Register the 5 new instance skill API routes inside [main.go](file:///home/ubuntu/claworc/control-plane/main.go) under the authenticated route group:
  - `GET /instances/{id}/skills` -> [ListInstanceSkills](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go)
  - `GET /instances/{id}/skills/{slug}/files` -> [ListInstanceSkillFiles](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go)
  - `GET /instances/{id}/skills/{slug}/files/*` -> [GetInstanceSkillFile](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go)
  - `PUT /instances/{id}/skills/{slug}/files/*` -> [PutInstanceSkillFile](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go)
  - `GET /instances/{id}/skills/{slug}/logs` -> [StreamInstanceSkillLogs](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go)
- [x] 3.2 Implement regex-based slug validation helper [isValidSlug](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) matching `^[a-zA-Z0-9_-]+$` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) and validate the slug parameter across all 5 new handlers, returning `HTTP 400 Bad Request` if invalid.
- [x] 3.3 Implement the remote path sandbox resolution helper [resolveRemoteSkillFilePath](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) inside [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) to sanitize file paths using `path.Clean` and check for prefixes within the skill base folder to prevent traversal attacks.
- [x] 3.4 Implement [ListInstanceSkills](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) handler logic:
  - Check authorization using `middleware.CanAccessInstance`.
  - If online: scan `/home/claworc/.openclaw/skills/` via `sshproxy.ListDirectory`.
  - If the directory does not exist, delete all `InstanceSkill` DB records for the instance and return `[]`.
  - Otherwise, parse the frontmatter of `SKILL.md` in each subdirectory (`slug`), validate the slug, and upsert records using the GORM `OnConflict` clause.
  - Perform garbage collection by deleting records for slugs not seen during the scan.
  - If offline: fetch cached records from the `InstanceSkill` database table.
- [x] 3.5 Implement [ListInstanceSkillFiles](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) handler logic:
  - Verify the agent is online, returning `HTTP 503 Service Unavailable` if offline.
  - Run a POSIX-compliant walk `find` command via `sshproxy.RunCommand` (pruning `node_modules`, `.git`, `.venv`, measuring size with `wc -c`, and identifying binary files by scanning the first 8KB with `dd` for NUL bytes) to gather sizes, binary flags, and relative paths in one SSH command.
  - Use `sshproxy.ShellQuote` to escape the base path to prevent command injection.
- [x] 3.6 Implement [GetInstanceSkillFile](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) handler logic:
  - Verify the agent is online, returning `HTTP 503` if offline.
  - Resolve the remote file path using the sandbox helper.
  - Perform a 2MB maximum file size check by querying size via `wc -c` over SSH first, rejecting with `HTTP 413 Payload Too Large` if exceeded.
  - Read the file content via `sshproxy.ReadFile` and return as JSON.
- [x] 3.7 Implement [PutInstanceSkillFile](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) handler logic:
  - Check authorization using `middleware.CanMutateInstance`.
  - Verify the agent is online, returning `HTTP 503`/`412` if offline.
  - Validate the body size of the content to be written, rejecting with `HTTP 413` if size > 2MB.
  - If the target file is `SKILL.md`, read the old `SKILL.md` content via SSH, parse both old and new frontmatter, and compare them.
  - Write updated file contents using `sshproxy.WriteFile`.
  - If `SKILL.md` was updated and its frontmatter changed, trigger config re-application:
    - Teardown the old MCP configuration (via `openclaw mcp unset` and deleting the sidecar container if transport was `"sse"`).
    - Deploy the new MCP configuration (merging environment variables, starting the sidecar container via `ContainerOrchestrator.Apply` if transport is `"sse"`, and running `openclaw mcp add` via SSH).
- [x] 3.8 Implement [StreamInstanceSkillLogs](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) SSE handler:
  - Establish a Server-Sent Events stream using `text/event-stream` headers.
  - Determine the sidecar container name as `mcp-{id}-{slug}`.
  - Set up an `io.Pipe` and a background scanner with a 2MB maximum token size buffer to handle large log lines without throwing buffer errors.
  - Stream logs via [StreamWorkloadLogs](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go) and flush the response writer line-by-line.

## Phase 4: Frontend
- [x] 4.1 Update frontend type definitions in [skills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/types/skills.ts) for `InstanceSkill`, `SkillFileEntry`, and `SkillFileContent`.
- [x] 4.2 Add API integration methods inside [skills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/api/skills.ts) to call `/instances/{id}/skills` and the related skill file and log endpoints.
- [x] 4.3 Update React query hooks in [useSkills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/hooks/useSkills.ts) (`useSkillFiles`, `useSkillFile`, and `useSaveSkillFile`) to support an optional `instanceId` parameter.
- [x] 4.4 Parameterize [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) to accept and pass down an optional `instanceId` prop to query/mutation hooks.
- [x] 4.5 Add the `"skills"` tab to [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx) under the tab menu:
  - [x] Render cached/offline skill lists with `"offline"` badges next to each when the agent is stopped/offline.
  - [x] Render live list of skills with status badges when running.
  - [x] Provide buttons to launch [SkillEditorModal](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) and mount the SSE log streaming viewer for sidecar logs.

## Phase 5: Testing/Verify
- [x] 5.1 Add backend unit/integration test cases in [skills_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills_test.go) covering the path resolution helper, slug validation, GORM database operations/seeding, and the five new API routes.
- [x] 5.2 Execute backend test suite: `cd control-plane && go test ./internal/...` to verify all tests pass successfully.
- [x] 5.3 Build frontend production assets: `cd control-plane/frontend && npm run build` to confirm there are no TypeScript compilation or warnings/errors.
