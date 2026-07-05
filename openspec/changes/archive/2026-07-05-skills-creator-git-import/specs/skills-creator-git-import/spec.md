# Specification: Skills Creator & Git Import

## 1. Overview
This specification defines the Skills Creator and Git Import capabilities. It enables users to visually author new skills via a creator wizard or securely import and update them from public Git repositories via HTTPS.

## 2. Requirements

### 2.1 Visual Creator Wizard
- **REQ-1**: The admin interface MUST provide a multi-step "Add Skill" visual creator wizard in [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx).
- **REQ-2**: The wizard MUST prompt for metadata (Name, unique Slug, Summary, and Required Env Vars), MCP configurations, and source files.
- **REQ-3**: The source files step MUST provide an interactive file-tree with a Monaco-based editor allowing users to create, rename, and edit custom files.
- **REQ-4**: Upon submission, the API MUST format a valid `SKILL.md` (containing parsed YAML frontmatter and markdown documentation) and save it along with custom source files to `{DataPath}/skills/{slug}/` via [saveSkillToLibrary](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L450).

### 2.2 Secure Git HTTPS Import
- **REQ-5**: The system MUST restrict remote Git repository imports to the HTTPS scheme.
- **REQ-6**: The system MUST reject non-HTTPS URLs and explicitly reject local protocol schemes (e.g., `file://`).
- **REQ-7**: The control plane MUST parse the import URL, resolve the hostname using [ValidateExternalURL](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go#L62), and reject any URL resolving to a loopback, private, unspecified, or link-local IP address.
- **REQ-8**: The backend MUST clone repository contents using `exec.CommandContext` with separate command arguments and no shell invocation to prevent shell injection.
- **REQ-9**: The clone command MUST enforce a context timeout (e.g., 30 seconds) and set environment variables `GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=echo`, and `SSH_ASKPASS=echo` to disable interactive terminal prompts.

### 2.3 Git Update & Pull
- **REQ-10**: The update API `POST /skills/{slug}/git-pull` MUST support pulling remote updates for Git-linked skills.
- **REQ-11**: By default, the pull action MUST merge updates with the existing local directory using Git to preserve untracked custom user files.
- **REQ-12**: The API MUST support a `force` parameter which deletes the local skill folder and performs a clean clone from Git.

## 3. Scenarios

### Scenario 1: Visual Wizard File Generation
**Given** an administrator accesses the Visual Creator wizard
**When** the user provides Name "My Skill", Slug "my-skill", and creates `main.py` with custom python code
**Then** the control plane MUST write a valid `SKILL.md` with GORM YAML frontmatter and save both `SKILL.md` and `main.py` inside `{DataPath}/skills/my-skill/`.

### Scenario 2: Blocking Non-HTTPS and SSRF Schemes
**Given** a Git import URL of `file:///etc/passwd` or resolving to `127.0.0.1`
**When** the import is requested
**Then** the backend MUST reject the request without executing any git commands.

### Scenario 3: Secure Git Import and Disabled Prompts
**Given** a valid public Git HTTPS URL targeting a repository requiring credentials
**When** the git clone is executed
**Then** the system MUST fail immediately instead of hanging, due to terminal prompts being disabled.

### Scenario 4: Standard Git Update (Automerge)
**Given** an imported skill with an untracked custom user file `custom_config.json`
**When** a standard git pull update is triggered
**Then** the backend MUST pull remote updates, keeping the file `custom_config.json` intact.

### Scenario 5: Force Overwrite Git Update
**Given** an imported skill with an untracked custom user file `custom_config.json`
**When** a git pull update is triggered with `force=true`
**Then** the backend MUST delete the local skill folder, perform a clean clone, and remove `custom_config.json`.
