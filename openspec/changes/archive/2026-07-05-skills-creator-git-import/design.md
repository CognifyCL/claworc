# Technical Design: Skills Creator & Git Import

This document defines the technical implementation details for adding the visual Skill Creator Wizard and Git HTTPS Import capabilities.

---

## 1. Database Schema Changes

We will update the `Skill` GORM model in [`control-plane/internal/database/models/models.go`](file:///home/ubuntu/claworc/control-plane/internal/database/models/models.go#L48) to track Git linkage metadata.

### Model Updates

```go
type Skill struct {
	ID              uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Slug            string    `gorm:"uniqueIndex;not null" json:"slug"`
	Name            string    `gorm:"not null" json:"name"`
	Summary         string    `json:"summary"`
	RequiredEnvVars string    `gorm:"type:text;default:'[]'" json:"-"` // JSON []string
	GitURL          string    `gorm:"type:text" json:"git_url,omitempty"`
	GitBranch       string    `gorm:"type:text" json:"git_branch,omitempty"`
	CreatedAt       time.Time `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}
```

### Database Migration

Per the project policy, additive column changes are applied automatically by `AutoMigrateAll` on boot. However, to pass the CI "Migration Drift Check" guard, we must register a no-op Goose migration in the registry.

We will create a new migration file [`control-plane/internal/database/migrations/migration_00010_noop_git_import_fields.go`](file:///home/ubuntu/claworc/control-plane/internal/database/migrations/migration_00010_noop_git_import_fields.go):

```go
package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	register(&goose.Migration{
		Version: 10,
		Source:  "00010_noop_git_import_fields.go",
		UpFnContext: func(ctx context.Context, tx *sql.Tx) error {
			return nil
		},
		DownFnContext: func(ctx context.Context, tx *sql.Tx) error {
			return nil
		},
	})
}
```

---

## 2. API Routes and Models

Three new HTTP routes will be introduced under the admin group inside [`control-plane/main.go`](file:///home/ubuntu/claworc/control-plane/main.go#L487-L492):

```go
r.Post("/skills/create", handlers.CreateSkillFromWizard)
r.Post("/skills/git-import", handlers.ImportGitSkill)
r.Post("/skills/{slug}/git-pull", handlers.PullGitSkillUpdates)
```

### 2.1 Request & Response Models

We will define the following models in [`control-plane/internal/handlers/skills.go`](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go):

```go
type CreateSkillRequest struct {
	Name            string            `json:"name"`
	Slug            string            `json:"slug"`
	Summary         string            `json:"summary"`
	RequiredEnvVars []string          `json:"required_env_vars"`
	MCP             *mcpConfig        `json:"mcp,omitempty"`
	Files           map[string]string `json:"files"` // map: filename -> content
}

type ImportGitSkillRequest struct {
	GitURL    string `json:"git_url"`
	GitBranch string `json:"git_branch,omitempty"`
}
```

---

## 3. Handler Implementation Logic

All new handlers will reside in [`control-plane/internal/handlers/skills.go`](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go).

### 3.1 Visual Skill Creator Wizard (`CreateSkillFromWizard`)

1. **Validation**:
   - Ensure the slug is valid (regex `^[a-zA-Z0-9_-]+$`) using `isValidSlug`.
   - Ensure the slug is safe (no path traversal) using `isSafeSlug`.
   - Check if a skill with the target slug already exists in the database. If so, return `409 Conflict`.
2. **`SKILL.md` Formatting**:
   - The backend will format the YAML frontmatter and document body dynamically.
   ```go
   fm := skillFrontmatter{
       Name:            req.Name,
       Description:     req.Summary,
       RequiredEnvVars: req.RequiredEnvVars,
       MCP:             req.MCP,
   }
   yamlBytes, err := yaml.Marshal(fm)
   if err != nil {
       // handle error
   }
   
   var buf bytes.Buffer
   buf.WriteString("---\n")
   buf.Write(yamlBytes)
   buf.WriteString("---\n\n")
   buf.WriteString("# " + req.Name + "\n\n")
   buf.WriteString(req.Summary + "\n")
   ```
3. **Files Compilation**:
   - Populate `filesMap := map[string][]byte{}` from `req.Files`.
   - Append `SKILL.md` key with compiled content bytes.
4. **Save**:
   - Call `saveSkillToLibrary(req.Slug, &fm, filesMap)` which writes to `{DataPath}/skills/{slug}` and updates the DB.
5. **Response**:
   - Return `201 Created` with the newly created skill metadata.

### 3.2 Secure Git HTTPS Import (`ImportGitSkill`)

1. **Validation**:
   - Parse the URL using `url.Parse(req.GitURL)`.
   - Strictly reject any schemes other than `https` (e.g. reject `file://` or `http://`).
   - Run hostname check via the SSRF helper: [`utils.ValidateExternalURL(req.GitURL, "")`](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go#L62).
2. **Slug Extraction**:
   - Extract the last segment of the path and trim `.git` to get the `slugCandidate`.
   - Run `isValidSlug` and `isSafeSlug` checks.
   - Return `409 Conflict` if the slug already exists in the database.
3. **Clone Execution**:
   - Execute command-line Git using `exec.CommandContext` (no shell invocation).
   - Destination directory: `destDir := filepath.Join(config.Cfg.DataPath, "skills", slugCandidate)`.
   - Timeout context of 30 seconds.
   - Disable interactive prompts via environment variables:
     ```go
     cmd.Env = append(os.Environ(),
         "GIT_TERMINAL_PROMPT=0",
         "GIT_ASKPASS=echo",
         "SSH_ASKPASS=echo",
     )
     ```
   - Build arguments dynamically:
     ```go
     args := []string{"clone", "--depth", "1"}
     if req.GitBranch != "" {
         args = append(args, "-b", req.GitBranch)
     }
     args = append(args, req.GitURL, destDir)
     ```
4. **Post-Clone Sync & Database Write**:
   - If the clone fails, clean up the directory and return the error message.
   - If the clone succeeds, read and parse `destDir/SKILL.md` using `parseSkillFrontmatter`. If missing or invalid, remove `destDir` and return a bad request error.
   - Create a new DB `Skill` record, populating the newly introduced `GitURL` and `GitBranch` columns.

### 3.3 Git Update & Pull (`PullGitSkillUpdates`)

This route handles updates for Git-linked skills, supporting both standard merge pulls and force overwrites.

1. **Database Lookup**:
   - Resolve skill slug from URL and check if `GitURL` is present. If empty, return a bad request.
2. **Determine Target Path**:
   - Path: `destDir := filepath.Join(config.Cfg.DataPath, "skills", skill.Slug)`.
3. **Update Strategy**:
   - **Force Update (`force=true` query param)**:
     - Delete the local directory: `os.RemoveAll(destDir)`.
     - Execute a clean clone using the exact logic from `ImportGitSkill` (using stored `GitURL` and `GitBranch`).
   - **Standard Pull (default)**:
     - Execute `git pull` inside `destDir` (`cmd.Dir = destDir`) with 30s context timeout and disabled prompts.
     - If the pull command fails (e.g. due to merge conflicts or local uncommitted changes), return the stderr output as a custom conflict message so the frontend can offer a force overwrite.
4. **Post-Pull Database Update**:
   - Re-read and re-parse `SKILL.md` from disk.
   - Save the updated metadata (`Name`, `Summary`, `RequiredEnvVars`) into the database `Skill` record.

---

## 4. SSRF Lookup & Security Controls

To prevent SSRF, hostname probing, and terminal hanging:

### 4.1 Strict HTTPS Enforcement

```go
parsed, err := url.Parse(req.GitURL)
if err != nil || parsed.Scheme != "https" {
    return fmt.Errorf("invalid URL or non-HTTPS scheme")
}
```

### 4.2 DNS Hostname Validation

We will leverage the existing [`utils.ValidateExternalURL`](file:///home/ubuntu/claworc/control-plane/internal/utils/sanitize.go#L62) helper, which:
1. Resolves the URL host to list of IPs via `net.DefaultResolver.LookupIPAddr`.
2. Rejects the request if any IP resolves to:
   - Loopback: `ip.IP.IsLoopback()` (e.g. `127.0.0.1`, `::1`)
   - Private: `ip.IP.IsPrivate()` (e.g. RFC 1918 subnets)
   - Link-local: `ip.IP.IsLinkLocalUnicast()` / `IsLinkLocalMulticast()`
   - Unspecified: `ip.IP.IsUnspecified()` (e.g. `0.0.0.0`)

### 4.3 Git Command Execution Isolation

To avoid shell injection, command execution will bypass shell wrappers:

```go
ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
defer cancel()

cmd := exec.CommandContext(ctx, "git", args...)
cmd.Env = append(os.Environ(),
    "GIT_TERMINAL_PROMPT=0",
    "GIT_ASKPASS=echo",
    "SSH_ASKPASS=echo",
)
```

---

## 5. Frontend Integration

### 5.1 API Client Definitions ([`common/api/skills.ts`](file:///home/ubuntu/claworc/control-plane/frontend/src/common/api/skills.ts))

Add the new endpoints:

```typescript
export async function createSkillFromWizard(payload: {
  name: string;
  slug: string;
  summary: string;
  required_env_vars: string[];
  mcp?: any;
  files: Record<string, string>;
}): Promise<Skill> {
  const res = await client.post<Skill>("/skills/create", payload);
  return res.data;
}

export async function importGitSkill(payload: {
  git_url: string;
  git_branch?: string;
}): Promise<Skill> {
  const res = await client.post<Skill>("/skills/git-import", payload);
  return res.data;
}

export async function pullGitSkillUpdates(slug: string, force = false): Promise<Skill> {
  const res = await client.post<Skill>(`/skills/${slug}/git-pull?force=${force}`);
  return res.data;
}
```

### 5.2 React Query Hooks ([`common/hooks/useSkills.ts`](file:///home/ubuntu/claworc/control-plane/frontend/src/common/hooks/useSkills.ts))

Define mutations for creator and import flows:

```typescript
export function useCreateSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: createSkillFromWizard,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill created successfully");
    },
    onError: (error) => errorToast("Failed to create skill", error),
  });
}

export function useImportGitSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: importGitSkill,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill imported successfully");
    },
    onError: (error) => errorToast("Failed to import skill from Git", error),
  });
}

export function usePullGitSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ slug, force }: { slug: string; force?: boolean }) => pullGitSkillUpdates(slug, force),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill updated successfully");
    },
  });
}
```

### 5.3 Skills Page UI Changes ([`app/pages/SkillsPage.tsx`](file:///home/ubuntu/claworc/control-plane/frontend/src/app/pages/SkillsPage.tsx))

1. **Button & Modal Entrypoint**:
   - Change the header action button text from "Upload Skill" to "Add Skill".
   - Toggle `showAddModal` instead of `showUpload`.
2. **Add Skill Modal structure**:
   - Implement tabs: **Visual Wizard**, **Git Import**, **Upload ZIP**.
   - **Git Import Tab**:
     - Input fields for `git_url` and `git_branch`.
     - Displays error alert if validation fails or repository requires authentication.
   - **Visual Wizard Tab**:
     - Multi-step flow:
       1. **Metadata**: Inputs for name, slug, description, env vars.
       2. **MCP Option**: Select stdio/sse, input local command/args or docker image/command/port.
       3. **Files Editor**: Simple sidebar for creating/editing files, embedding the codebase's existing [`MonacoConfigEditor`](file:///home/ubuntu/claworc/control-plane/frontend/src/common/components/MonacoConfigEditor.tsx) component.
       4. **Save**: Triggers the `useCreateSkill` hook.
3. **Library Skill Cards Update**:
   - If a library skill card has `git_url` set, render a Git Pull button.
   - On pull click:
     - Trigger standard pull.
     - If it fails (due to conflicts or unstaged local changes), show confirmation dialog:
       *"Standard update failed (likely due to local edits). Would you like to force overwrite? This will discard all local changes."*
     - If confirmed, trigger `pullGitSkill` with `{ force: true }`.
