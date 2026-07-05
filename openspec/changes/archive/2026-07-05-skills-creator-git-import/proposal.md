# Proposal: Skills Creator & Git Import

## Intent
Enable users to visually create new skills or import them from Git repositories directly within Claworc's admin UI, improving skill bootstrapping efficiency and library management.

## Scope

### In Scope
- Web-based Visual Skill Creator Wizard (metadata, MCP options, in-browser Monaco file editor).
- Direct Git URL import with branch/tag selection.
- Backend git clone using native command execution with strict security controls (shell injection, SSRF, local file read, and hang prevention).
- Tracking of `GitURL` and `GitBranch` in the database.
- An endpoint to pull and overwrite updates for Git-linked skills.

### Out of Scope
- Direct write-back/push from Claworc to remote Git repositories.
- Support for complex Git authentication flows (SSH keys, private credentials) in the initial release (only HTTPS/HTTP URLs allowed).

## Capabilities

### New Capabilities
- `skills-creator-git-import`: Ability to bootstrap skills via a visual UI wizard or import and update them directly from public Git repositories securely.

### Modified Capabilities
None

## Approach
1. **Database Schema**: Add `GitURL` and `GitBranch` to the [Skill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go#L48) model in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go).
2. **Backend API**: Implement endpoints `POST /skills/create`, `POST /skills/git-import`, and `POST /skills/{slug}/git-pull` in [main.go](file:///home/ubuntu/claworc/control-plane/main.go) and [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go).
3. **Security Safeguards**: Use direct `exec.CommandContext` without a shell, parse URLs to enforce `http`/`https`, resolve hostnames via `net.LookupIP` to reject private/loopback IPs using the lookup pattern in [sanitize.go](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go), restrict git protocol access, and apply context timeouts with disabled CLI prompts.
4. **UI Components**: Create unified "Add Skill" modal in [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx) featuring a multi-step creator wizard (with Monaco editor) and a Git import form.

## Affected Areas
- [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go): Update `Skill` GORM model.
- [main.go](file:///home/ubuntu/claworc/control-plane/main.go): Add creation/import/pull API routes.
- [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go): Implement `CreateSkillFromWizard`, `ImportGitSkill`, and `PullGitSkillUpdates`.
- [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx): Embed unified "Add Skill" entrypoint.

## Risks
- **SSRF / Host Probe**: Mitigated by checking resolved IPs using the lookup pattern in [sanitize.go](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go).
- **Hanging Commands**: Mitigated by running `exec` with context timeouts and interactive prompts disabled.

## Rollback Plan
Drop database columns `git_url` and `git_branch`. Revert new handlers and routes in `main.go`/`skills.go`, and restore original `SkillsPage.tsx`.

## Dependencies
- Native `git` CLI installed on the control plane host system.

## Success Criteria
- [ ] Administrators can create a skill via Wizard, generating a valid `SKILL.md` and custom files.
- [ ] Users can import skills via a valid public HTTPS Git repository URL.
- [ ] Injected shell commands, local protocols (`file://`), and loopback addresses (`127.0.0.1`) are blocked and logged.

## Proposal question round
1. Should we support HTTP basic auth credentials in the Git clone URL?
2. Should pulling updates auto-merge, or completely overwrite files?
