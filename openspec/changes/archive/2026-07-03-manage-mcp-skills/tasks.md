# Review Workload Forecast

- 400-line budget risk: Low
- Chained PRs: No
- Decision needed before apply: No

---

## Phase 1: Metadata Parsing, Validation & Env Resolution
- [x] 1.1 Add MCP Go structures to `control-plane/internal/handlers/skills.go`:
  - Define `mcpDockerConfig`, `mcpLocalConfig`, and `mcpConfig` structs.
  - Extend the `skillFrontmatter` struct to include an `MCP *mcpConfig` field tag (`yaml:"mcp,omitempty"`).
- [x] 1.2 Implement the environment variable placeholder resolver:
  - Add a regex-based `resolvePlaceholders(input string, env map[string]string) string` helper function in `skills.go` to resolve `{{VAR_NAME}}` format templates against instance/global environments.
- [x] 1.3 Update `parseSkillFrontmatter`:
  - Ensure the parser function correctly deserializes the new `mcp` block from the frontmatter of `SKILL.md`.
- [x] 1.4 Write unit tests for parsing and resolution:
  - Add test cases in `control-plane/internal/handlers/skills_test.go` verifying correct metadata parsing and placeholder resolution (both success and fallback scenarios).

## Phase 2: Orchestration Layer Changes (Sidecar Ingress & Network Policies)
- [x] 2.1 Extend `WorkloadSpec`:
  - Add `IngressAllowedFrom []string` to the `WorkloadSpec` struct in `control-plane/internal/orchestrator/spec.go` to specify which agent instance labels can connect.
- [x] 2.2 Modify NetworkPolicy application in `kubernetes_apply.go`:
  - Update `applyNetworkPolicy` in `control-plane/internal/orchestrator/kubernetes_apply.go` to iterate over `spec.IngressAllowedFrom` and generate corresponding ingress policy rules allowing connection on exposed ports.
- [x] 2.3 Add tests for network policies:
  - Update tests in `control-plane/internal/orchestrator/kubernetes_apply_test.go` to verify generated network policy rules when `IngressAllowedFrom` is set.

## Phase 3: Lifecycle Management & CLI Hooks (Deploy, Undeploy, and Cleanup)
- [x] 3.1 Update `deployToInstance` in `skills.go`:
  - Parse the incoming `SKILL.md` from the deployment `fileMap`.
  - Clean up any previous sidecar and run `openclaw mcp unset` for old configurations if the skill was previously deployed.
  - Copy the skill files to the remote instance via SSH.
  - If `mcpConfig` is defined:
    - Load and merge target instance environment variables.
    - Resolve environment placeholder values.
    - **SSE transport**:
      - Construct sidecar `WorkloadSpec` named `mcp-<instance-id>-<skill-slug>` setting `IngressAllowedFrom` to the instance name.
      - Start the sidecar via `orchestrator.Apply` and wait/poll for it to become healthy.
      - Run `openclaw mcp add <name> --transport sse --url http://mcp-<instance-id>-<skill-slug>:<port>/sse` via SSH inside the agent.
    - **Stdio transport**:
      - Run `openclaw mcp add <name> --transport stdio --command <cmd> [--args <args>] [--env <env>]` via SSH inside the agent.
- [x] 3.2 Implement `UndeploySkill` handler and API endpoint:
  - Implement `handlers.UndeploySkill` in `control-plane/internal/handlers/skills.go` to accept JSON payload `{"instance_ids": [...]}`.
  - Inside the handler:
    - Connect via SSH to each target instance.
    - Parse `SKILL.md` (locally or from the instance) to extract the `mcpConfig`.
    - If configured, run `openclaw mcp unset <name>` and delete the sidecar workload using `orchestrator.DeleteWorkload`.
    - Delete the skill files from the instance directory `/home/claworc/.openclaw/skills/<slug>`.
  - Register route `POST /api/v1/skills/{slug}/undeploy` in `control-plane/main.go`.
- [x] 3.3 Add cleanup hook in `DeleteInstance`:
  - Update `DeleteInstance` in `control-plane/internal/handlers/instances.go` to find all skills containing `transport: sse` MCP configurations and delete running sidecars using `orchestrator.DeleteWorkload` before final database deletion.

## Phase 4: Testing & Verification
- [x] 4.1 Write integration tests:
  - Add integration tests for deployment, update, and undeployment workflows (mocking ssh/orchestrator calls) to verify the lifecycle actions of sidecar creation and CLI configuration.
- [x] 4.2 Verify backend compiler and test suite:
  - Execute `cd control-plane && go test ./internal/...` to ensure all unit and integration tests pass.
- [x] 4.3 Verify frontend compilation:
  - Run `cd control-plane/frontend && npm run build` to confirm there are no TypeScript compilation or build errors.
