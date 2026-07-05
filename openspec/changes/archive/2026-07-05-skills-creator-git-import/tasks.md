# Review Workload Forecast

- 400-line budget risk: Medium
- Chained PRs: No
- Decision needed before apply: No

---

## Phase 1: DB/Migrate
- [x] 1.1 Add `GitURL` and `GitBranch` columns to the [Skill](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go#L48) struct in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go). Use type `string` with tags `gorm:"type:text" json:"git_url,omitempty"` and `gorm:"type:text" json:"git_branch,omitempty"`.
- [x] 1.2 Create Goose database migration file [migration_00010_noop_git_import_fields.go](file:///home/ubuntu/claworc/control-plane/internal/database/migrations/migration_00010_noop_git_import_fields.go) under `control-plane/internal/database/migrations/` as a no-op migration (version 10) to satisfy the CI migration drift check.

## Phase 2: Backend API
- [x] 2.1 Define request Go structs `CreateSkillRequest` and `ImportGitSkillRequest` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) for parsing visual wizard inputs and Git URL inputs.
- [x] 2.2 Register the three new admin endpoints in [main.go](file:///home/ubuntu/claworc/control-plane/main.go#L487-L492):
  - `POST /skills/create` -> `handlers.CreateSkillFromWizard`
  - `POST /skills/git-import` -> `handlers.ImportGitSkill`
  - `POST /skills/{slug}/git-pull` -> `handlers.PullGitSkillUpdates`
- [x] 2.3 Implement the visual creator wizard handler `CreateSkillFromWizard` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go):
  - Validate the `slug` input using [isValidSlug](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L197) and [isSafeSlug](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L417).
  - Check for existing skill collisions in the database, returning `409 Conflict` if the slug is already registered.
  - Dynamically format `SKILL.md` (YAML frontmatter + markdown body with Name and Description).
  - Map incoming custom files, append `SKILL.md`, and save locally and in the database using the existing [saveSkillToLibrary](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L450) helper.
- [x] 2.4 Implement the Git import handler `ImportGitSkill` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go):
  - Parse the user-provided `GitURL` using `url.Parse` and enforce the `https` scheme (reject local protocol schemes or standard `http`).
  - Resolve the hostname to prevent SSRF and hostname probing using [ValidateExternalURL](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go#L62).
  - Extract the skill slug candidate from the last segment of the Git URL path, removing the `.git` suffix, and validate it using [isValidSlug](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L197) and [isSafeSlug](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L417).
  - Verify that the slug candidate doesn't already exist in the database, returning `409 Conflict` if registered.
  - Clone the repository under `{DataPath}/skills/{slug}` using `exec.CommandContext` (no shell invocation) with a 30-second timeout, `depth=1`, specifying branch via `-b` if `GitBranch` is provided.
  - Inject environment overrides (`GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=echo`, and `SSH_ASKPASS=echo`) to prevent interactive prompts from hanging the CLI.
  - If the clone fails, clean up the created directory.
  - Read and parse `SKILL.md` from the cloned repository to validate it (missing or invalid frontmatter results in removing the folder and returning a bad request).
  - Insert the new `Skill` record in GORM, persisting `GitURL` and `GitBranch` values.
- [x] 2.5 Implement the Git update pull handler `PullGitSkillUpdates` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go):
  - Look up the skill in the database by slug and verify it is Git-linked (has a `GitURL`).
  - Resolve the local target directory path.
  - If the `force=true` query parameter is set: delete the local skill folder and perform a clean clone using the exact `ImportGitSkill` cloning sequence.
  - If `force` is false (default): run `git pull` inside the skill directory with disabled terminal prompts and 30-second context timeout. If the command fails (due to unstaged changes/conflicts), return stderr as a conflict response so the frontend can offer a force overwrite.
  - Re-parse `SKILL.md` from the updated files and update the GORM record fields (`Name`, `Summary`, `RequiredEnvVars`).

### Phase 3: Frontend
- [x] 3.1 Update the frontend `Skill` interface in [skills.ts (types)](file:///home/ubuntu/claworc/control-plane/frontend/src/common/types/skills.ts#L1) to include optional fields `git_url?: string;` and `git_branch?: string;`.
- [x] 3.2 Add TypeScript client function definitions in [skills.ts (API)](file:///home/ubuntu/claworc/control-plane/frontend/src/common/api/skills.ts) for `createSkillFromWizard`, `importGitSkill`, and `pullGitSkillUpdates`.
- [x] 3.3 Add React Query hooks in [useSkills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/hooks/useSkills.ts) for `useCreateSkill`, `useImportGitSkill`, and `usePullGitSkill`.
- [x] 3.4 Update [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx):
  - Rename the action button from "Upload Skill" to "Add Skill" and toggle a `showAddModal` state instead of `showUpload`.
  - Create a new tabbed modal (or update [UploadSkillModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/UploadSkillModal.tsx) to handle the new tabs) that includes:
    - **Visual Wizard**: Form for name, slug, description, env vars; MCP transport configuration (stdio or SSE); custom file manager sidebar using [MonacoConfigEditor.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/MonacoConfigEditor.tsx); and submission via `useCreateSkill`.
    - **Git Import**: Fields for `git_url` and `git_branch`, submission via `useImportGitSkill`, displaying error alerts on SSRF validation failures.
    - **Upload ZIP**: Fallback to the original ZIP file upload form.
  - Update local skill library list/cards to detect if a skill is Git-linked (has `git_url`). For Git-linked cards, show a "Git Pull" button.
  - Implement a confirmation dialog when standard Git pull fails, prompting: *"Standard update failed (likely due to local edits). Would you like to force overwrite? This will discard all local changes."* If confirmed, trigger `pullGitSkill` with `{ force: true }`.
 
## Phase 4: Testing/Verify
- [x] 4.1 Write unit and integration tests inside `control-plane/internal/handlers/skills_git_import_test.go` (or in [skills_test.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills_test.go)):
  - Verify that invalid schemes (e.g. non-HTTPS, `file://`) are rejected by `ImportGitSkill` without running commands.
  - Verify that SSRF attempts (resolving to loopback/private/link-local IPs) are blocked by the handler.
  - Verify success scenarios for `CreateSkillFromWizard` and metadata parsing.
  - Verify `PullGitSkillUpdates` behavior for standard pull, conflict handling, and force overwrite update.
- [x] 4.2 Run the backend test suite using `go test ./internal/handlers/...` from `control-plane/` to ensure all tests pass.
- [x] 4.3 Run `make migration-check` to verify database schemas match GORM models and the Goose migration is correctly registered.
- [x] 4.4 Build the frontend using `npm run build` from `control-plane/frontend` to ensure no TypeScript compilation or lint issues exist.
