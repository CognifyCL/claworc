package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gluk-w/claworc/control-plane/internal/config"
	"github.com/gluk-w/claworc/control-plane/internal/database"
	"github.com/go-chi/chi/v5"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupGitImportSkillsTestDB(t *testing.T) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := db.AutoMigrate(&database.Skill{}); err != nil {
		t.Fatalf("auto-migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() { database.DB = nil })

	dir := t.TempDir()
	prev := config.Cfg.DataPath
	config.Cfg.DataPath = dir
	t.Cleanup(func() { config.Cfg.DataPath = prev })
	return dir
}

func TestCreateSkillRequestStruct(t *testing.T) {
	s := `{"name":"A","slug":"a","summary":"s","required_env_vars":["K"],"mcp":{"name":"n","transport":"stdio"},"files":{"main.py":"print('hello')"}}`
	var req CreateSkillRequest
	if err := json.Unmarshal([]byte(s), &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if req.Name != "A" || req.Slug != "a" || req.Summary != "s" {
		t.Errorf("unexpected fields: %+v", req)
	}
	if req.MCP == nil || req.MCP.Name != "n" || req.MCP.Transport != "stdio" {
		t.Errorf("unexpected MCP: %+v", req.MCP)
	}
	if req.Files["main.py"] != "print('hello')" {
		t.Errorf("unexpected files: %v", req.Files)
	}
}

func TestImportGitSkillRequestStruct(t *testing.T) {
	s := `{"git_url":"https://github.com/a/b","git_branch":"main"}`
	var req ImportGitSkillRequest
	if err := json.Unmarshal([]byte(s), &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if req.GitURL != "https://github.com/a/b" || req.GitBranch != "main" {
		t.Errorf("unexpected fields: %+v", req)
	}
}

func TestCreateSkillFromWizard_Validation(t *testing.T) {
	setupGitImportSkillsTestDB(t)

	cases := []struct {
		name       string
		payload    interface{}
		wantStatus int
		wantSubstr string
	}{
		{
			name:       "empty body",
			payload:    nil,
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid request",
		},
		{
			name: "invalid slug format",
			payload: CreateSkillRequest{
				Name: "Test Skill",
				Slug: "invalid slug name!",
			},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid slug",
		},
		{
			name: "unsafe slug path traversal",
			payload: CreateSkillRequest{
				Name: "Test Skill",
				Slug: "../unsafe",
			},
			wantStatus: http.StatusBadRequest,
			wantSubstr: "invalid slug",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			if tc.payload != nil {
				body, _ = json.Marshal(tc.payload)
			}
			req := httptest.NewRequest(http.MethodPost, "/skills/create", bytes.NewReader(body))
			w := httptest.NewRecorder()
			CreateSkillFromWizard(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if !strings.Contains(w.Body.String(), tc.wantSubstr) {
				t.Errorf("body = %q, want it to contain %q", w.Body.String(), tc.wantSubstr)
			}
		})
	}
}

func TestCreateSkillFromWizard_Conflict(t *testing.T) {
	setupGitImportSkillsTestDB(t)

	// Pre-insert a skill
	err := database.DB.Create(&database.Skill{
		Slug: "existing-slug",
		Name: "Existing Skill",
	}).Error
	if err != nil {
		t.Fatalf("failed to insert mock skill: %v", err)
	}

	payload := CreateSkillRequest{
		Name: "Conflict Skill",
		Slug: "existing-slug",
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/skills/create", bytes.NewReader(body))
	w := httptest.NewRecorder()
	CreateSkillFromWizard(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 Conflict", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("body = %q, want conflict error message", w.Body.String())
	}
}

func TestCreateSkillFromWizard_Success(t *testing.T) {
	dir := setupGitImportSkillsTestDB(t)

	payload := CreateSkillRequest{
		Name:            "Awesome Skill",
		Slug:            "awesome-skill",
		Summary:         "An awesome test skill.",
		RequiredEnvVars: []string{"API_KEY", "SECRET"},
		MCP: &mcpConfig{
			Name:      "awesome-mcp",
			Transport: "stdio",
			Local: &mcpLocalConfig{
				Command: "python3",
				Args:    []string{"main.py"},
			},
		},
		Files: map[string]string{
			"main.py": "print('hello')",
			"utils.py": "def foo(): pass",
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/skills/create", bytes.NewReader(body))
	w := httptest.NewRecorder()
	CreateSkillFromWizard(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 Created. Body: %s", w.Code, w.Body.String())
	}

	// Verify DB record
	var skill database.Skill
	err := database.DB.Where("slug = ?", "awesome-skill").First(&skill).Error
	if err != nil {
		t.Fatalf("failed to find created skill in DB: %v", err)
	}
	if skill.Name != "Awesome Skill" || skill.Summary != "An awesome test skill." {
		t.Errorf("unexpected skill fields in DB: %+v", skill)
	}

	// Verify filesystem contents
	skillDir := filepath.Join(dir, "skills", "awesome-skill")
	skillMd, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("failed to read SKILL.md: %v", err)
	}
	skillMdStr := string(skillMd)
	if !strings.Contains(skillMdStr, "name: Awesome Skill") {
		t.Errorf("SKILL.md frontmatter missing name: %s", skillMdStr)
	}
	if !strings.Contains(skillMdStr, "description: An awesome test skill.") {
		t.Errorf("SKILL.md frontmatter missing description: %s", skillMdStr)
	}
	if !strings.Contains(skillMdStr, "awesome-mcp") {
		t.Errorf("SKILL.md frontmatter missing mcp config: %s", skillMdStr)
	}

	// Verify custom files
	mainPy, err := os.ReadFile(filepath.Join(skillDir, "main.py"))
	if err != nil || string(mainPy) != "print('hello')" {
		t.Errorf("main.py invalid or missing: %v, content: %q", err, string(mainPy))
	}
	utilsPy, err := os.ReadFile(filepath.Join(skillDir, "utils.py"))
	if err != nil || string(utilsPy) != "def foo(): pass" {
		t.Errorf("utils.py invalid or missing: %v, content: %q", err, string(utilsPy))
	}
}

func TestImportGitSkill(t *testing.T) {
	// 1. Setup mock git executable in a temp directory
	tempBinDir := t.TempDir()
	gitPath := filepath.Join(tempBinDir, "git")
	
	gitScript := `#!/bin/sh
# Find the last argument as destination
for arg do
  dest="$arg"
done

if [ "$GIT_MOCK_SUCCESS" = "1" ]; then
  mkdir -p "$dest"
  cat << 'EOF' > "$dest/SKILL.md"
---
name: Mock Git Skill
description: Imported via Git
required_env_vars:
  - GIT_KEY
---
# Mock Git Skill
Imported via Git
EOF
  exit 0
elif [ "$GIT_MOCK_INVALID_SKILL" = "1" ]; then
  mkdir -p "$dest"
  echo "invalid frontmatter" > "$dest/SKILL.md"
  exit 0
else
  echo "fatal: repository not found" >&2
  exit 1
fi
`
	if err := os.WriteFile(gitPath, []byte(gitScript), 0755); err != nil {
		t.Fatalf("failed to write mock git script: %v", err)
	}

	// Prepend tempBinDir to PATH
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", tempBinDir+string(os.PathListSeparator)+originalPath)

	t.Run("reject non-HTTPS scheme", func(t *testing.T) {
		setupGitImportSkillsTestDB(t)
		payload := ImportGitSkillRequest{GitURL: "http://github.com/foo/bar.git"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "HTTPS") && !strings.Contains(w.Body.String(), "scheme") {
			t.Errorf("expected HTTPS/scheme validation error, got: %q", w.Body.String())
		}
	})

	t.Run("reject SSRF hostname resolving to loopback", func(t *testing.T) {
		setupGitImportSkillsTestDB(t)
		payload := ImportGitSkillRequest{GitURL: "https://127.0.0.1/foo.git"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "resolves to a private/internal address") {
			t.Errorf("expected SSRF error, got: %q", w.Body.String())
		}
	})

	t.Run("reject existing slug collision", func(t *testing.T) {
		setupGitImportSkillsTestDB(t)
		// Pre-insert slug
		database.DB.Create(&database.Skill{Slug: "bar", Name: "Existing"})

		payload := ImportGitSkillRequest{GitURL: "https://github.com/foo/bar.git"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 Conflict", w.Code)
		}
	})

	t.Run("clean up on clone failure", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "0")
		t.Setenv("GIT_MOCK_INVALID_SKILL", "0")

		payload := ImportGitSkillRequest{GitURL: "https://github.com/foo/bar.git"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		// Verify directory is cleaned up
		destDir := filepath.Join(dir, "skills", "bar")
		if _, err := os.Stat(destDir); !os.IsNotExist(err) {
			t.Errorf("destDir %s should be cleaned up", destDir)
		}
	})

	t.Run("clean up on invalid SKILL.md", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "0")
		t.Setenv("GIT_MOCK_INVALID_SKILL", "1")

		payload := ImportGitSkillRequest{GitURL: "https://github.com/foo/bar.git"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		// Verify directory is cleaned up
		destDir := filepath.Join(dir, "skills", "bar")
		if _, err := os.Stat(destDir); !os.IsNotExist(err) {
			t.Errorf("destDir %s should be cleaned up on invalid SKILL.md", destDir)
		}
	})

	t.Run("success import", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "1")
		t.Setenv("GIT_MOCK_INVALID_SKILL", "0")

		payload := ImportGitSkillRequest{GitURL: "https://github.com/foo/bar.git", GitBranch: "main"}
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/skills/git-import", bytes.NewReader(body))
		w := httptest.NewRecorder()
		ImportGitSkill(w, req)

		if w.Code != http.StatusOK && w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 200/201. Body: %s", w.Code, w.Body.String())
		}

		// Verify DB entry
		var skill database.Skill
		if err := database.DB.Where("slug = ?", "bar").First(&skill).Error; err != nil {
			t.Fatalf("failed to find imported skill in DB: %v", err)
		}
		if skill.GitURL != "https://github.com/foo/bar.git" || skill.GitBranch != "main" {
			t.Errorf("unexpected DB fields: %+v", skill)
		}

		// Verify file exists
		destDir := filepath.Join(dir, "skills", "bar")
		if _, err := os.Stat(filepath.Join(destDir, "SKILL.md")); err != nil {
			t.Errorf("SKILL.md not found in %s", destDir)
		}
	})
}

func TestPullGitSkillUpdates(t *testing.T) {
	tempBinDir := t.TempDir()
	gitPath := filepath.Join(tempBinDir, "git")
	
	gitScript := `#!/bin/sh
# Find the last argument as destination
for arg do
  dest="$arg"
done

if [ "$1" = "clone" ]; then
  if [ "$GIT_MOCK_SUCCESS" = "1" ]; then
    mkdir -p "$dest"
    cat << 'EOF' > "$dest/SKILL.md"
---
name: Mock Git Skill Clone
description: Cloned via Git
required_env_vars:
  - CLONE_KEY
---
# Mock Git Skill Clone
Cloned via Git
EOF
    exit 0
  else
    echo "fatal: clone failed" >&2
    exit 1
  fi
elif [ "$1" = "pull" ]; then
  if [ "$GIT_MOCK_SUCCESS" = "1" ]; then
    cat << 'EOF' > "SKILL.md"
---
name: Mock Updated Git Skill
description: Pull was successful
required_env_vars:
  - PULL_KEY
---
# Mock Updated Git Skill
Pull was successful
EOF
    exit 0
  else
    echo "error: local changes conflict" >&2
    exit 1
  fi
else
  exit 1
fi
`
	if err := os.WriteFile(gitPath, []byte(gitScript), 0755); err != nil {
		t.Fatalf("failed to write mock git script: %v", err)
	}

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", tempBinDir+string(os.PathListSeparator)+originalPath)

	t.Run("reject non-Git skill", func(t *testing.T) {
		setupGitImportSkillsTestDB(t)
		// insert non-git skill
		database.DB.Create(&database.Skill{Slug: "nongit", Name: "Non Git"})

		req := httptest.NewRequest(http.MethodPost, "/skills/nongit/git-pull", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "nongit")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		PullGitSkillUpdates(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
		if !strings.Contains(w.Body.String(), "not Git-linked") {
			t.Errorf("expected not Git-linked error, got %q", w.Body.String())
		}
	})

	t.Run("standard pull success", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "1")

		// Create local skill folder and file
		skillDir := filepath.Join(dir, "skills", "gitskill")
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("failed to create skillDir: %v", err)
		}
		initialFM := []byte("---\nname: Initial Git Skill\ndescription: Initial summary\n---\nbody\n")
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), initialFM, 0644); err != nil {
			t.Fatalf("failed to write initial SKILL.md: %v", err)
		}

		// insert DB entry
		database.DB.Create(&database.Skill{
			Slug:      "gitskill",
			Name:      "Initial Git Skill",
			Summary:   "Initial summary",
			GitURL:    "https://github.com/foo/gitskill.git",
			GitBranch: "main",
		})

		req := httptest.NewRequest(http.MethodPost, "/skills/gitskill/git-pull", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "gitskill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		PullGitSkillUpdates(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK. Body: %s", w.Code, w.Body.String())
		}

		// Verify DB updated
		var skill database.Skill
		if err := database.DB.Where("slug = ?", "gitskill").First(&skill).Error; err != nil {
			t.Fatalf("failed to find skill in DB: %v", err)
		}
		if skill.Name != "Mock Updated Git Skill" || skill.Summary != "Pull was successful" {
			t.Errorf("DB fields not updated correctly: %+v", skill)
		}
	})

	t.Run("standard pull conflict", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "0")

		skillDir := filepath.Join(dir, "skills", "conflict-skill")
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("failed to create skillDir: %v", err)
		}
		initialFM := []byte("---\nname: Initial Git Skill\ndescription: Initial summary\n---\nbody\n")
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), initialFM, 0644); err != nil {
			t.Fatalf("failed to write initial SKILL.md: %v", err)
		}

		database.DB.Create(&database.Skill{
			Slug:    "conflict-skill",
			Name:    "Initial Git Skill",
			Summary: "Initial summary",
			GitURL:  "https://github.com/foo/conflict-skill.git",
		})

		req := httptest.NewRequest(http.MethodPost, "/skills/conflict-skill/git-pull", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "conflict-skill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		PullGitSkillUpdates(w, req)

		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409 Conflict", w.Code)
		}
		if !strings.Contains(w.Body.String(), "local changes conflict") {
			t.Errorf("expected conflict message in body, got: %q", w.Body.String())
		}
	})

	t.Run("force pull success", func(t *testing.T) {
		dir := setupGitImportSkillsTestDB(t)
		t.Setenv("GIT_MOCK_SUCCESS", "1")

		skillDir := filepath.Join(dir, "skills", "forceskill")
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatalf("failed to create skillDir: %v", err)
		}
		initialFM := []byte("---\nname: Initial Git Skill\ndescription: Initial summary\n---\nbody\n")
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), initialFM, 0644); err != nil {
			t.Fatalf("failed to write initial SKILL.md: %v", err)
		}
		// Also create an untracked custom user file
		untrackedFile := filepath.Join(skillDir, "custom.json")
		if err := os.WriteFile(untrackedFile, []byte("{}"), 0644); err != nil {
			t.Fatalf("failed to write untracked file: %v", err)
		}

		database.DB.Create(&database.Skill{
			Slug:      "forceskill",
			Name:      "Initial Git Skill",
			Summary:   "Initial summary",
			GitURL:    "https://github.com/foo/forceskill.git",
			GitBranch: "main",
		})

		req := httptest.NewRequest(http.MethodPost, "/skills/forceskill/git-pull?force=true", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "forceskill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		PullGitSkillUpdates(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 OK. Body: %s", w.Code, w.Body.String())
		}

		// Verify DB updated with Clone info (as force does clean clone)
		var skill database.Skill
		if err := database.DB.Where("slug = ?", "forceskill").First(&skill).Error; err != nil {
			t.Fatalf("failed to find skill in DB: %v", err)
		}
		if skill.Name != "Mock Git Skill Clone" {
			t.Errorf("DB fields not updated from clone: %+v", skill)
		}

		// Verify untracked file is deleted
		if _, err := os.Stat(untrackedFile); !os.IsNotExist(err) {
			t.Errorf("expected untracked file %s to be deleted under force mode", untrackedFile)
		}
	})
}
