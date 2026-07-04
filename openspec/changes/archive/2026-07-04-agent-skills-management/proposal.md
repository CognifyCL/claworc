# Proposal: Agent Skills Management

## Intent
Enable visibility, editing, and debugging of active/deployed skills directly on individual agents via the web interface.

## Scope

### In Scope
- "Skills" tab in [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx) to list deployed skills.
- Live file view/edit under `/home/claworc/.openclaw/skills/{slug}` using `sshproxy`.
- Stream container logs for SSE transport MCP sidecars.
- Hybrid DB ([InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go)) and SSH sync.
- **POSIX-only remote scanning**: Standard POSIX tools (`find`, `file`, `stat`), no Python3.
- **Command injection protection**: Regex slugs, path containment, and `shellQuote` escaping.
- **Log buffer safety**: SSE log streaming with a 2MB maximum line-scanning buffer.
- **File size caps**: Enforced 2MB file size limit for viewing and editing.
- **Workload cleanup**: Reconcile container workloads against [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) to remove orphans.

### Out of Scope
- Global skill editing from the agent detail view.
- File editing when the agent is stopped.

## Capabilities

### New Capabilities
- `agent-skills-management`: Inspect, edit, and stream logs for deployed skills on specific agent instances.

### Modified Capabilities
None

## Approach
1. **State Tracking & Sync**: Add [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) GORM model. Sync SSH file paths to the DB when online; serve cached DB list when offline.
2. **Remote Editing**: Adapt [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) to call instance-specific file endpoints using `sshproxy`.
3. **Log Streaming**: Extend `ContainerOrchestrator` with `StreamWorkloadLogs` and add SSE log endpoint.
4. **Workload Cleanup**: Run a background daemon to query active containers, killing any with no matching [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) record.

## Affected Areas
- [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go): Add [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) model.
- [main.go](file:///home/ubuntu/claworc/control-plane/main.go): Register instance skill API routes.
- [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go): Implement list, view, edit, and log-streaming handlers with safety controls.
- [orchestrator.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/orchestrator.go): Add `StreamWorkloadLogs` interface.
- [AgentDetailPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/AgentDetailPage.tsx): Add "Skills" tab.
- [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx): Support instance-specific editing.

## Risks
- **SSH Latency**: Mitigate via listing caching and parallel reads.
- **Log Buffer Crashes**: Mitigate with a 2MB maximum token buffer in `bufio.Scanner`.

## Rollback Plan
Drop [InstanceSkill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go) DB model. Revert frontend changes and orchestrator interface extensions.

## Dependencies
- Container orchestrator must support log retrieval (`StreamWorkloadLogs`).

## Success Criteria
- [ ] Deployed skills display with status badges in Agent Details UI.
- [ ] Users can edit/save remote files; changes to `SKILL.md` re-apply MCP configuration.
- [ ] SSE MCP sidecar logs stream dynamically to the UI.

## Proposal question round
1. Should local file edits automatically commit to git if the skill has a repository origin?
2. What default buffer size and tail count should we use for streaming logs?
