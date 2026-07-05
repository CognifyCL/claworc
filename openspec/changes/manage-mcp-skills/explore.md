# Exploration: Skill Creator Wizard & Git Repository Import

This document explores the technical design and implementation of two new capabilities in Claworc:
1. **Visual Skill Creator/Wizard**: Allowing users to create skills directly from the web interface instead of only being able to upload a ZIP file.
2. **Git Repository Import**: Allowing users to import skills directly from Git repositories by providing a repository clone URL in the UI.

---

## 1. Goal & Requirements

### 1.1 Visual Skill Creator/Wizard
* **Objective**: Provide an interactive form/wizard in the web UI for users (administrators) to bootstrap new skills from scratch.
* **Fields Required**:
  - Skill name, slug (auto-generated from name but customizable), and description.
  - Required environment variables (e.g., API keys, configuration variables).
  - MCP configuration: Option to specify if it is an MCP server, transport type (`stdio` or `sse`), and corresponding config (command, args, docker image, ports, env mappings).
  - File editor: Ability to define and write multiple custom source files (e.g., `main.py`, `package.json`, `.env.example`).
* **Output**: The creator should generate a valid `SKILL.md` (containing parsed GORM frontmatter YAML and markdown documentation) along with any secondary files, and register it directly into Claworc's library.

### 1.2 Git Repository Import
* **Objective**: Import a skill directly into the library using a remote Git clone URL.
* **Fields Required**:
  - Clone URL (HTTPS or SSH, with HTTPS being preferred for standard usage).
  - Target branch/tag/commit reference (optional, defaults to the default branch).
  - Create new / rename strategy (optional, for handling slug conflicts).
* **Process**:
  - Clone the repository into a secure temporary location.
  - Parse `SKILL.md` frontmatter to resolve the skill's slug, name, summary, and required env vars.
  - Validate and save all repository files into `{DataPath}/skills/{slug}`.
  - Register the skill in the database.
  - Clean up temporary clone resources.

---

## 2. Existing Architecture & Code Analysis

### 2.1 Backend Architecture
* **Database Model**: Defined in [models.go](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go#L48-L56). The `Skill` struct holds:
  - `Slug` (unique index, not null)
  - `Name` (not null)
  - `Summary` (text summary)
  - `RequiredEnvVars` (JSON string array)
  - `CreatedAt` & `UpdatedAt`
* **Storage Location**: Skills are stored on disk under `{DataPath}/skills/{slug}/` (typically resolved as `filepath.Join(config.Cfg.DataPath, "skills", slug)`).
* **Save Helper**: The function `saveSkillToLibrary` in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L450) handles writing files to disk (using `safeJoin` to prevent path traversal) and saving the GORM record:
  ```go
  func saveSkillToLibrary(slug string, fm *skillFrontmatter, files map[string][]byte) (database.Skill, error)
  ```
* **Routing**: In [main.go](file:///home/ubuntu/claworc/control-plane/main.go#L487-L491), route configuration gates skill additions under admin authorization:
  ```go
  r.Post("/skills", handlers.UploadSkill) // Current ZIP upload
  r.Post("/skills/clawhub/import", handlers.ImportClawhubSkill) // Clawhub registry download
  ```

### 2.2 Frontend Architecture
* **Library Interface**: [SkillsPage.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx) displays list views and handles modals. Currently, it triggers `UploadSkillModal` when the "Upload Skill" button is clicked.
* **Modals & Editors**:
  - [UploadSkillModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/UploadSkillModal.tsx) provides a drag-and-drop ZIP file uploader.
  - [SkillEditorModal.tsx](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/skills/SkillEditorModal.tsx) uses Monaco editor to modify files for existing skills.
* **API Client & Hooks**:
  - [api/skills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/api/skills.ts) manages axios HTTP requests to backend endpoints.
  - [hooks/useSkills.ts](file:///home/ubuntu/claworc/control-plane/frontend/src/common/hooks/useSkills.ts) exposes react-query wrappers (e.g., `useUploadSkill`).

---

## 3. Design Approaches & Trade-offs

We evaluate design options for implementing the creator and Git import below.

### 3.1 Visual Skill Creator: API Mechanics

#### Approach A: Frontend-Assembled ZIP Upload
* **Mechanism**: The frontend creator wizard compiles user metadata and files, generates `SKILL.md` in the browser, packs them into a `.zip` in memory (using a library like JSZip), and uploads the zip to the existing `POST /skills` endpoint.
* **Pros**: Reuses the existing upload handler without adding backend endpoints.
* **Cons**:
  - High frontend complexity (managing binary ZIP blobs, introducing browser zip packages).
  - Harder to validate metadata directly on the API layer (errors only surface after extraction).
  - Poor extensibility for future updates.

#### Approach B: Direct JSON-based Creator Endpoint (Recommended)
* **Mechanism**: Introduce a new REST endpoint `POST /skills/create` accepting a JSON payload representing metadata (Name, Summary, EnvVars, MCP settings) and a map of filenames to contents. The backend generates `SKILL.md` and saves the skill to disk.
* **Pros**:
  - Clean API contract and simple validation.
  - No new frontend package dependencies (no JSZip).
  - Reuses the robust, audited `saveSkillToLibrary` logic.
* **Cons**: Requires adding a new API route and backend handler.

---

### 3.2 Git Repository Import: Backend Implementation

#### Approach A: Direct System `git` CLI Invocation (Recommended)
* **Mechanism**: The backend invokes the host's `git` command-line utility via `os/exec.CommandContext` to clone the target repository to a temporary folder, reads the cloned filesystem, and imports it.
* **Pros**:
  - Lightweight; uses the standard, fully-compatible Git client.
  - No bloated external Go dependencies added to `go.mod`.
  - Supports standard SSH/HTTPS mechanisms natively.
* **Cons**:
  - Requires the host system to have the `git` binary installed (already true on almost all Linux environments).
  - Requires strict security filters to prevent shell injection, server-side request forgery (SSRF), and arbitrary file read vulnerabilities.

#### Approach B: Pure Go Git Library (`go-git`)
* **Mechanism**: Add `github.com/go-git/go-git/v5` as a project dependency to clone the repository in-memory or on-disk entirely within Go runtime.
* **Pros**: Self-contained; works independently of host-system binaries.
* **Cons**:
  - Adds a huge dependency tree to `go.mod`, introducing security and maintenance overhead.
  - Limited protocol and SSH configuration compatibility compared to native git.

#### Approach C: ZIP Downloader Fallback
* **Mechanism**: Guess or request zip-download links (e.g. `https://github.com/user/repo/archive/refs/heads/main.zip`) and run the existing zip extraction logic.
* **Pros**: Reuses the ZIP upload pipeline with zero local CLI commands.
* **Cons**: Only works for popular public git providers (GitHub/GitLab). Fails for private git servers or alternative hosts.

---

### 3.3 MCP Server Configurations Integration

#### Approach A: Bound strictly as a Skill (Current Model)
* **Mechanism**: MCP configuration resides within the `SKILL.md` frontmatter. Deploying the skill deploys the corresponding MCP toolset.
* **Pros**: Encourages encapsulation; a skill's tools, code, and documentation remain packaged together.
* **Cons**: Standalone MCP servers must be artificially packaged inside a boilerplate skill folder containing a dummy `SKILL.md`.

#### Approach B: Standalone MCP Configurations
* **Mechanism**: Decouple MCP configurations entirely, treating them as first-class database entities with a separate UI management page, while allowing skills to reference them.
* **Pros**: Higher flexibility. Allows registering general-purpose MCP servers once and deploying them globally.
* **Cons**: Increases database, API, and frontend complexity (separate tables, deployment orchestrations, and UI workflows).

---

## 4. Security & Robustness (Adversarial Protections)

Executing remote Git clones and writing arbitrary user-generated files exposes the system to several high-severity vulnerability vectors. The implementation must implement the following safeguards:

### 4.1 Shell Injection Prevention
* **Risk**: If user-provided Git URLs or branches are concatenated into shell command strings (e.g., `sh -c "git clone " + url`), a malicious payload like `https://github.com/a.git; rm -rf /` will run arbitrary system commands.
* **Mitigation**: Never invoke a shell. Run git commands by calling `exec.CommandContext` directly and pass arguments as a slice of separate strings:
  ```go
  // Args are passed as individual array elements, avoiding shell interpretation
  cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--", cloneURL, tempDir)
  ```

### 4.2 Local File Read Vulnerabilities (Git Local protocol)
* **Risk**: Git allows cloning from local paths or `file://` URLs. If a malicious user supplies `file:///etc/passwd`, git clone will copy sensitive host files into the temporary skill directory.
* **Mitigation**:
  1. Parse the incoming URL with `url.Parse` and verify the scheme is strictly `http` or `https`.
  2. Configure git commands to explicitly disable local and external protocol access:
     ```go
     cmd := exec.CommandContext(ctx, "git",
         "-c", "protocol.file.allow=never",
         "-c", "protocol.ext.allow=never",
         "clone", "--depth", "1", "--", cloneURL, tempDir,
     )
     ```

### 4.3 Server-Side Request Forgery (SSRF)
* **Risk**: A user can target internal, private, or loopback IPs (e.g., `http://127.0.0.1:8500`, `http://169.254.169.254/latest/meta-data`) to probe network status or steal metadata.
* **Mitigation**: Resolve the hostnames of the URL via Go's `net.LookupIP` and verify the IP is a public address, explicitly rejecting loopback, link-local, private, or unspecified IP ranges (using the pattern established in [sanitize.go](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go#L58-L90)).

### 4.4 Hanging Processes (Denial of Service)
* **Risk**: Git commands targeting SSH URLs or private repos that require credentials will block execution by prompting for user input in the terminal (`Username for 'https://github.com':`), hanging the HTTP thread indefinitely.
* **Mitigation**:
  1. Enforce a context timeout (e.g., `30 seconds`) using `context.WithTimeout`.
  2. Disable git prompts and interactive configurations via environment variables:
     ```go
     cmd.Env = append(os.Environ(),
         "GIT_TERMINAL_PROMPT=0",
         "GIT_ASKPASS=echo",
         "SSH_ASKPASS=echo",
     )
     ```

---

## 5. Proposed Implementation Plan

### 5.1 Backend Changes

#### 5.1.1 Database Updates
We will add `GitURL` and `GitBranch` optional columns to the `Skill` model in `control-plane/internal/database/models/models.go` to keep track of imported skills:
```go
type Skill struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Slug            string    `gorm:"uniqueIndex;not null" json:"slug"`
	Name            string    `gorm:"not null" json:"name"`
	Summary         string    `json:"summary"`
	RequiredEnvVars string    `gorm:"type:text;default:'[]'" json:"-"`
	GitURL          string    `gorm:"size:1024" json:"git_url,omitempty"`
	GitBranch       string    `gorm:"size:256" json:"git_branch,omitempty"`
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}
```

#### 5.1.2 API Routes
Add routes to `control-plane/main.go` under the admin/library-curation section:
```go
r.Post("/skills/create", handlers.CreateSkillFromWizard)
r.Post("/skills/git-import", handlers.ImportGitSkill)
r.Post("/skills/{slug}/git-pull", handlers.PullGitSkillUpdates)
```

#### 5.1.3 Handlers Implementation
* **`CreateSkillFromWizard`**:
  - Accept JSON request representing metadata, files, and optional MCP configs.
  - Validate parameters (safe slugs, valid environment variable names).
  - Format `SKILL.md` containing frontmatter (YAML) + markdown guidelines.
  - Save the files to library and register the DB row.
* **`ImportGitSkill`**:
  - Accept JSON payload with `clone_url` and optional `branch`/`create_new` fields.
  - Sanitize and resolve IP address for SSRF protection.
  - Clone via safe `exec.CommandContext` to a temporary directory.
  - Walk the clone folder, extract `SKILL.md`, parse metadata.
  - Reconcile slug, then save files and store `GitURL` + `GitBranch` in GORM.
  - Clean up temporary files via deferred `os.RemoveAll`.
* **`PullGitSkillUpdates`**:
  - Retrieve the existing GORM `Skill`. If `GitURL` is empty, return `400 Bad Request`.
  - Re-clone the repository to check for updates.
  - Compare/overwrite the files in the library directory and save updates.

---

## 5.2 Frontend Changes

#### 5.2.1 API Integration
Add API methods to `frontend/src/common/api/skills.ts` and React Query mutation hooks to `frontend/src/common/hooks/useSkills.ts`:
* `createSkill(payload: CreateSkillPayload)`
* `importGitSkill(payload: GitImportPayload)`
* `pullGitSkill(slug: string)`

#### 5.2.2 Unified "Add Skill" Entrypoint
Update the "Upload Skill" button on `SkillsPage.tsx` to "Add Skill" with a dropdown or unified tabs modal offering three distinct paths:
1. **Upload ZIP**: Current drag-and-drop file uploader.
2. **Git Import**: Input clone URL, optional branch, and import triggers.
3. **Visual Creator**: Multi-step wizard to formulate the skill.

#### 5.2.3 Visual Creator Modal Component (`SkillCreatorModal.tsx`)
A wizard split into steps:
* **Step 1: Metadata**: General fields (Name, Slug, Description, Required Env Vars).
* **Step 2: MCP configuration**: Toggle option for MCP enablement, transport selector (`stdio`/`sse`), and sub-fields (Docker image/commands/ports or local command/args).
* **Step 3: Source Files**: Simple file-tree structure where users can add/rename files and write their initial code (e.g. Python scripts) directly in a Monaco-based editor.

#### 5.2.4 Git Import Modal Component (`GitImportModal.tsx`)
A form soliciting the clone URL and branch:
* Validates URL schemes locally.
* Connects to `useImportGitSkill` mutation.
* Displays a loader while cloning is in progress.

---

## 6. Verification & Test Plan

### 6.1 Backend Unit Tests
Add verification tests in `control-plane/internal/handlers/skills_git_test.go`:
* **Security Validation Tests**: Verify that paths like `file:///` URLs or malformed domains targeting localhost resolve to validation errors.
* **Shell Escape Tests**: Inject shell metacharacters into URL inputs and confirm no arbitrary process is executed.
* **Clone Failure & Timeout Recovery**: Mock a delayed git server and assert the request fails gracefully at timeout limits.

### 6.2 Frontend Component Tests
Write unit tests using Testing Library:
* Verify that file creation, editing, and deleting operate cleanly within the Creator Wizard file-tree.
* Ensure validation displays appropriate inline error feedback for duplicate slugs or invalid Git URLs.
