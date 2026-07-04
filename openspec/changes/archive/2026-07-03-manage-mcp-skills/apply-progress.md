# Implementation Progress: MCP Skills Integration

This document tracks the step-by-step implementation of the Model Context Protocol (MCP) skills integration in Claworc.

## TDD Cycle Evidence Table

| Phase / Task | Safety Net | RED (Failing Test) | GREEN (Min Code) | TRIANGULATE (Happy + Edge) | REFACTOR (Clean & Verify) |
| :--- | :---: | :---: | :---: | :---: | :---: |
| **Phase 1: Metadata & Env Resolution** | | | | | |
| 1.1 Add Go Structures | Passed | Passed (Compile Err) | Passed (Parsed block) | Passed | Passed |
| 1.2 Placeholder Resolver | Passed | Passed (Compile Err) | Passed (Regex-based helper) | Passed | Passed |
| 1.3 Update parseSkillFrontmatter | Passed | Passed (Compile Err) | Passed (yaml.Unmarshal auto-populate) | Passed | Passed |
| 1.4 Write Unit Tests | Passed | Passed (Assert fails) | Passed (All test cases green) | Passed | Passed |
| **Phase 2: Orchestration Layer Changes** | | | | | |
| 2.1 Extend WorkloadSpec | Passed | Passed (Compile Err) | Passed (Added field to spec) | Passed | Passed |
| 2.2 Modify NetworkPolicy application | Passed | Passed (Compile Err) | Passed (Added rule loops) | Passed | Passed |
| 2.3 Add tests for network policies | Passed | Passed (Assert fails) | Passed (Assert passes) | Passed | Passed |
| **Phase 3: Lifecycle Management & CLI Hooks** | | | | | |
| 3.1 Update deployToInstance | Passed | Passed (Verify mock calls) | Passed (Applied sidecar / run stdio) | Passed | Passed |
| 3.2 Implement UndeploySkill handler | Passed | Passed (Assert HTTP fails) | Passed (Undeployed & unset) | Passed | Passed |
| 3.3 Add cleanup hook in DeleteInstance | Passed | Passed (Assert cleanup fails) | Passed (Teardown sidecars) | Passed | Passed |
| **Phase 4: Testing & Verification** | | | | | |
| 4.1 Write integration tests | Passed | Passed (Verify mocked hooks) | Passed (Tests pass) | Passed | Passed |
| 4.2 Verify backend compiler/tests | Passed | Passed (N/A) | Passed (Run all tests green) | Passed | Passed |
| 4.3 Verify frontend compilation | Passed | Passed (N/A) | Passed (Build succeeded) | Passed | Passed |

---

## Detailed Steps Done

1. **Safety Net**:
   - Initialized and verified that `CGO_ENABLED=1 go test ./internal/...` baseline passed with no regressions.

2. **Phase 1 (RED & GREEN)**:
   - Added structures `mcpDockerConfig`, `mcpLocalConfig`, `mcpConfig` and extended `skillFrontmatter` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L150) to support `yaml:"mcp,omitempty"`.
   - Wrote `resolvePlaceholders` regex helper to parse `{{VAR}}` formats.
   - Verified that parsing and resolution unit tests compile and run green in [skills_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills_test.go).

3. **Phase 2 (RED & GREEN)**:
   - Extended `WorkloadSpec` with `IngressAllowedFrom []string` in [spec.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/spec.go).
   - Updated `applyNetworkPolicy` in [kubernetes_apply.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes_apply.go) to append ingress permission rules for each entry in `spec.IngressAllowedFrom`.
   - Verified with a dedicated unit test in [kubernetes_apply_test.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes_apply_test.go) that these network policies are correctly generated.

4. **Phase 3 (RED & GREEN)**:
   - Updated `deployToInstance` signature to accept context, then implemented:
     - Remote check for old `SKILL.md` to run `mcp unset` and tear down existing sidecars.
     - Merging of instance env variables.
     - Spawning sidecars via `orchestrator.Apply` and waiting until healthy (`status == "running"`).
     - Running agent-local process registrations for stdio-transport MCP servers.
   - Implemented `UndeploySkill` HTTP handler in `skills.go` and registered it at route `POST /api/v1/skills/{slug}/undeploy` in `main.go`.
   - Added `DeleteInstance` cleanup hook in `instances.go` to find all skills containing `sse` transport MCP configurations and tear down the running sidecars before DB deletion.

5. **Phase 4 (Testing & Verification)**:
   - Added integration test `TestDeployAndUndeployMCPWorkflow` in [skills_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills_test.go) asserting complete deployment, updates, and undeployment lifecycles for both Stdio and SSE transports.
   - Confirmed all backend tests pass.
   - Confirmed frontend builds successfully via Vite compiler.
