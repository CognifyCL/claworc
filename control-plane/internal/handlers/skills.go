package handlers

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gluk-w/claworc/control-plane/internal/analytics"
	"github.com/gluk-w/claworc/control-plane/internal/config"
	"github.com/gluk-w/claworc/control-plane/internal/database"
	"github.com/gluk-w/claworc/control-plane/internal/middleware"
	"github.com/gluk-w/claworc/control-plane/internal/orchestrator"
	"github.com/gluk-w/claworc/control-plane/internal/sshproxy"
	"github.com/gluk-w/claworc/control-plane/internal/taskmanager"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm/clause"
)

// ---------------------------------------------------------------------------
// Clawhub proxy (well-known discovery + search cache)
// ---------------------------------------------------------------------------

const clawhubWellKnownURL = "https://clawhub.ai/.well-known/clawhub.json"

type clawhubCacheEntry struct {
	body      []byte
	expiresAt time.Time
}

var (
	clawhubMu          sync.RWMutex
	clawhubAPIBase     string
	clawhubAPIBaseExp  time.Time
	clawhubSearchCache = map[string]*clawhubCacheEntry{}
	clawhubHTTPClient  = &http.Client{Timeout: 10 * time.Second}
)

func getClawhubAPIBase(ctx context.Context) (string, error) {
	clawhubMu.RLock()
	base := clawhubAPIBase
	exp := clawhubAPIBaseExp
	clawhubMu.RUnlock()

	if base != "" && time.Now().Before(exp) {
		return base, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clawhubWellKnownURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := clawhubHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch clawhub well-known: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var wk struct {
		APIBase string `json:"apiBase"`
	}
	if err := json.Unmarshal(body, &wk); err != nil {
		return "", fmt.Errorf("parse clawhub well-known: %w", err)
	}
	if wk.APIBase == "" {
		return "", fmt.Errorf("clawhub well-known: empty apiBase")
	}

	clawhubMu.Lock()
	clawhubAPIBase = wk.APIBase
	clawhubAPIBaseExp = time.Now().Add(time.Hour)
	clawhubMu.Unlock()
	return wk.APIBase, nil
}

// ClawhubSearch proxies search queries to the Clawhub public registry.
func ClawhubSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := r.URL.Query().Get("limit")
	if limit == "" {
		limit = "20"
	}

	cacheKey := "search:" + q + ":" + limit

	clawhubMu.RLock()
	entry := clawhubSearchCache[cacheKey]
	clawhubMu.RUnlock()

	if entry != nil && time.Now().Before(entry.expiresAt) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(entry.body)
		return
	}

	apiBase, err := getClawhubAPIBase(r.Context())
	if err != nil {
		log.Printf("clawhub search: %v", err)
		http.Error(w, `{"error":"clawhub unavailable"}`, http.StatusBadGateway)
		return
	}

	url := apiBase + "/api/v1/search?q=" + q + "&limit=" + limit
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	resp, err := clawhubHTTPClient.Do(req)
	if err != nil {
		log.Printf("clawhub search fetch: %v", err)
		http.Error(w, `{"error":"clawhub unavailable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"read error"}`, http.StatusBadGateway)
		return
	}

	if resp.StatusCode == http.StatusOK {
		newEntry := &clawhubCacheEntry{body: body, expiresAt: time.Now().Add(60 * time.Second)}
		clawhubMu.Lock()
		clawhubSearchCache[cacheKey] = newEntry
		clawhubMu.Unlock()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// ---------------------------------------------------------------------------
// SKILL.md frontmatter parsing
// ---------------------------------------------------------------------------

type mcpDockerConfig struct {
	Image   string            `yaml:"image"`
	Command []string          `yaml:"command,omitempty"`
	Port    int               `yaml:"port"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type mcpLocalConfig struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type mcpConfig struct {
	Name      string           `yaml:"name"`
	Transport string           `yaml:"transport"` // "sse" or "stdio"
	Docker    *mcpDockerConfig `yaml:"docker,omitempty"`
	Local     *mcpLocalConfig  `yaml:"local,omitempty"`
}

type skillFrontmatter struct {
	Name            string     `yaml:"name"`
	Description     string     `yaml:"description"`
	RequiredEnvVars []string   `yaml:"required_env_vars,omitempty"`
	MCP             *mcpConfig `yaml:"mcp,omitempty"`
}

var placeholderRegex = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

func resolvePlaceholders(input string, env map[string]string) string {
	return placeholderRegex.ReplaceAllStringFunc(input, func(m string) string {
		varName := placeholderRegex.FindStringSubmatch(m)[1]
		if val, ok := env[varName]; ok {
			return val
		}
		return ""
	})
}

var slugRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidSlug(slug string) bool {
	return slugRegex.MatchString(slug)
}

func resolveRemoteSkillFilePath(slug, relPath string) (string, error) {
	if !isValidSlug(slug) {
		return "", fmt.Errorf("invalid skill slug")
	}
	if relPath == "" {
		return "", fmt.Errorf("path required")
	}
	cleanRel := path.Clean(relPath)
	if strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(cleanRel, "..") || cleanRel == ".." {
		return "", fmt.Errorf("invalid file path")
	}
	remoteRoot := "/home/claworc/.openclaw/skills/" + slug
	absPath := path.Clean(path.Join(remoteRoot, cleanRel))
	if !strings.HasPrefix(absPath, remoteRoot+"/") && absPath != remoteRoot {
		return "", fmt.Errorf("invalid file path")
	}
	return absPath, nil
}


func parseSkillFrontmatter(content []byte) (*skillFrontmatter, error) {
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		return nil, fmt.Errorf("SKILL.md missing frontmatter opening ---")
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		return nil, fmt.Errorf("SKILL.md missing frontmatter closing ---")
	}
	yamlBlock := rest[:end]
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return nil, fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	if fm.Name == "" {
		return nil, fmt.Errorf("SKILL.md frontmatter missing name")
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("SKILL.md frontmatter missing description")
	}
	return &fm, nil
}

// ---------------------------------------------------------------------------
// List skills
// ---------------------------------------------------------------------------

type skillResponse struct {
	ID              uint     `json:"id"`
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	Summary         string   `json:"summary"`
	RequiredEnvVars []string `json:"required_env_vars"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

func skillToResponse(s database.Skill) skillResponse {
	return skillResponse{
		ID:              s.ID,
		Slug:            s.Slug,
		Name:            s.Name,
		Summary:         s.Summary,
		RequiredEnvVars: parseRequiredEnvVars(s.RequiredEnvVars),
		CreatedAt:       s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:       s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func parseRequiredEnvVars(raw string) []string {
	if raw == "" || raw == "[]" {
		return []string{}
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil || names == nil {
		return []string{}
	}
	return names
}

func encodeRequiredEnvVars(names []string) string {
	if len(names) == 0 {
		return "[]"
	}
	b, err := json.Marshal(names)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func ListSkills(w http.ResponseWriter, r *http.Request) {
	var skills []database.Skill
	if err := database.DB.Order("created_at desc").Find(&skills).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to list skills")
		return
	}
	resp := make([]skillResponse, len(skills))
	for i, s := range skills {
		resp[i] = skillToResponse(s)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Upload skill (zip)
// ---------------------------------------------------------------------------

func UploadSkill(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "File too large or invalid form")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "Missing file field")
		return
	}
	defer file.Close()

	zipData, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read file")
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid zip file")
		return
	}

	prefix := detectZipPrefix(zr.File)
	files := map[string][]byte{}
	var skillMDContent []byte

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" {
			continue
		}
		if strings.Contains(name, "..") {
			writeError(w, http.StatusBadRequest, "Invalid path in zip: "+name)
			return
		}
		rc, err := f.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "Failed to read zip entry")
			return
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, "Failed to read zip entry content")
			return
		}
		files[name] = data
		if name == "SKILL.md" {
			skillMDContent = data
		}
	}

	if skillMDContent == nil {
		writeError(w, http.StatusBadRequest, "Zip does not contain SKILL.md")
		return
	}

	fm, err := parseSkillFrontmatter(skillMDContent)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid SKILL.md: "+err.Error())
		return
	}

	slug := fm.Name

	overwrite := r.URL.Query().Get("overwrite") == "true"

	var existing database.Skill
	if err := database.DB.Where("slug = ?", slug).First(&existing).Error; err == nil {
		if !overwrite {
			writeError(w, http.StatusConflict, "Skill '"+slug+"' already exists")
			return
		}
		// Remove existing files and DB record before re-creating
		_ = os.RemoveAll(filepath.Join(config.Cfg.DataPath, "skills", slug))
		database.DB.Delete(&existing)
	}

	skill, err := saveSkillToLibrary(slug, fm, files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save skill")
		return
	}

	var totalSkills int64
	database.DB.Model(&database.Skill{}).Count(&totalSkills)
	analytics.Track(r.Context(), analytics.EventSkillUploaded, map[string]any{
		"total_skills": totalSkills,
	})

	writeJSON(w, http.StatusCreated, skillToResponse(skill))
}

// isSafeSlug reports whether slug is a single, safe path component: non-empty,
// no path separators, and a local path (no absolute paths or ".." traversal).
// filepath.IsLocal is the canonical containment check (and is recognized as a
// path-injection sanitizer).
func isSafeSlug(slug string) bool {
	if slug == "" {
		return false
	}
	if strings.ContainsRune(slug, '/') || strings.ContainsRune(slug, '\\') {
		return false
	}
	return filepath.IsLocal(slug)
}

// safeJoin joins a user-supplied (slash-separated) relative name onto baseDir,
// guaranteeing the result stays within baseDir. It rejects absolute paths and
// any ".." traversal that would escape baseDir by requiring the localized,
// cleaned name to be a local path (filepath.IsLocal) before joining.
func safeJoin(baseDir, name string) (string, error) {
	// Normalize slash-separated archive names to the OS separator and collapse
	// any "." / ".." segments.
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("empty path")
	}
	// filepath.IsLocal rejects absolute paths and anything that escapes the
	// current directory (e.g. "../x"); it is modeled as a sanitizer barrier.
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("path escapes base directory")
	}
	return filepath.Join(baseDir, clean), nil
}

// saveSkillToLibrary writes the given file map to {DataPath}/skills/{slug} and
// creates the matching database record. The caller is responsible for any
// pre-existing slug handling (overwrite, unique-suffix, etc.). On DB failure the
// freshly-written directory is removed so the filesystem and DB stay in sync.
func saveSkillToLibrary(slug string, fm *skillFrontmatter, files map[string][]byte) (database.Skill, error) {
	// The slug and file names originate from user-controlled input (request body
	// and downloaded archive entry names). Validate them so the resulting paths
	// cannot escape {DataPath}/skills via separators or ".." traversal.
	if !isSafeSlug(slug) {
		return database.Skill{}, fmt.Errorf("invalid skill slug %q", slug)
	}

	skillsRoot := filepath.Join(config.Cfg.DataPath, "skills")
	destDir, err := safeJoin(skillsRoot, slug)
	if err != nil {
		return database.Skill{}, fmt.Errorf("invalid skill slug %q: %w", slug, err)
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return database.Skill{}, fmt.Errorf("create skill directory: %w", err)
	}

	for name, data := range files {
		destPath, err := safeJoin(destDir, name)
		if err != nil {
			os.RemoveAll(destDir)
			return database.Skill{}, fmt.Errorf("invalid skill file path %q: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			os.RemoveAll(destDir)
			return database.Skill{}, fmt.Errorf("create directory: %w", err)
		}
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			os.RemoveAll(destDir)
			return database.Skill{}, fmt.Errorf("write file: %w", err)
		}
	}

	skill := database.Skill{
		Slug:            slug,
		Name:            fm.Name,
		Summary:         fm.Description,
		RequiredEnvVars: encodeRequiredEnvVars(fm.RequiredEnvVars),
	}
	if err := database.DB.Create(&skill).Error; err != nil {
		os.RemoveAll(destDir)
		return database.Skill{}, fmt.Errorf("save skill: %w", err)
	}
	return skill, nil
}

// ---------------------------------------------------------------------------
// Import a Clawhub skill into the local library
// ---------------------------------------------------------------------------

// ImportClawhubSkill downloads a skill from Clawhub and saves it into the local
// library (without deploying it to any instance). If a skill with the same slug
// already exists, the caller must pass create_new=true to store it under a fresh
// "{slug}-1", "{slug}-2", … slug; otherwise a 409 is returned.
func ImportClawhubSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slug      string `json:"slug"`
		Version   string `json:"version"`
		CreateNew bool   `json:"create_new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.Slug == "" {
		writeError(w, http.StatusBadRequest, "Missing slug")
		return
	}

	files, err := buildSkillFileMap(r.Context(), body.Slug, "clawhub", body.Version)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Failed to download skill: "+err.Error())
		return
	}

	skillMD, ok := files["SKILL.md"]
	if !ok {
		writeError(w, http.StatusBadGateway, "Downloaded skill does not contain SKILL.md")
		return
	}
	fm, err := parseSkillFrontmatter(skillMD)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Invalid SKILL.md: "+err.Error())
		return
	}

	targetSlug := body.Slug
	var existing database.Skill
	if err := database.DB.Where("slug = ?", targetSlug).First(&existing).Error; err == nil {
		if !body.CreateNew {
			writeError(w, http.StatusConflict, "Skill '"+targetSlug+"' already exists")
			return
		}
		targetSlug = nextAvailableSkillSlug(body.Slug)
	}

	skill, err := saveSkillToLibrary(targetSlug, fm, files)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to save skill")
		return
	}

	var totalSkills int64
	database.DB.Model(&database.Skill{}).Count(&totalSkills)
	analytics.Track(r.Context(), analytics.EventSkillUploaded, map[string]any{
		"total_skills": totalSkills,
	})

	writeJSON(w, http.StatusCreated, skillToResponse(skill))
}

// nextAvailableSkillSlug returns the first "{base}-N" slug (N starting at 1) that
// is not already present in the skills table.
func nextAvailableSkillSlug(base string) string {
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		var count int64
		database.DB.Model(&database.Skill{}).Where("slug = ?", candidate).Count(&count)
		if count == 0 {
			return candidate
		}
	}
}

// detectZipPrefix returns a common top-level directory prefix if all files share one.
func detectZipPrefix(files []*zip.File) string {
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) != 2 {
			return ""
		}
		prefix := parts[0] + "/"
		for _, f2 := range files {
			if !f2.FileInfo().IsDir() && !strings.HasPrefix(f2.Name, prefix) {
				return ""
			}
		}
		return prefix
	}
	return ""
}

// ---------------------------------------------------------------------------
// Skill file editor (list / get / put)
// ---------------------------------------------------------------------------

type skillFileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Binary bool   `json:"binary"`
}

type skillFileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Binary  bool   `json:"binary"`
}

type skillFilePutRequest struct {
	Content string `json:"content"`
}

// isBinaryContent returns true if the first 8KB of data contains a NUL byte.
func isBinaryContent(data []byte) bool {
	limit := len(data)
	if limit > 8192 {
		limit = 8192
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// resolveSkillFilePath validates the user-supplied relative path and returns
// the absolute on-disk path. It rejects empty paths, "..", and any path that
// would escape the skill directory.
func resolveSkillFilePath(skillSlug, rel string) (string, string, error) {
	if rel == "" {
		return "", "", fmt.Errorf("path required")
	}
	if strings.Contains(rel, "..") {
		return "", "", fmt.Errorf("invalid path")
	}
	cleanRel := filepath.ToSlash(filepath.Clean(rel))
	if strings.HasPrefix(cleanRel, "/") || strings.HasPrefix(cleanRel, "..") {
		return "", "", fmt.Errorf("invalid path")
	}
	root := filepath.Join(config.Cfg.DataPath, "skills", skillSlug)
	abs := filepath.Join(root, filepath.FromSlash(cleanRel))
	relCheck, err := filepath.Rel(root, abs)
	if err != nil {
		return "", "", fmt.Errorf("invalid path")
	}
	if strings.HasPrefix(relCheck, "..") || relCheck == ".." {
		return "", "", fmt.Errorf("invalid path")
	}
	return abs, cleanRel, nil
}

// lookupSkill loads the skill record and returns 404 if not found.
func lookupSkill(w http.ResponseWriter, slug string) (*database.Skill, bool) {
	var skill database.Skill
	if err := database.DB.Where("slug = ?", slug).First(&skill).Error; err != nil {
		writeError(w, http.StatusNotFound, "Skill not found")
		return nil, false
	}
	return &skill, true
}

// ListSkillFiles returns the list of files inside a skill's on-disk directory.
func ListSkillFiles(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	skill, ok := lookupSkill(w, slug)
	if !ok {
		return
	}

	root := filepath.Join(config.Cfg.DataPath, "skills", skill.Slug)
	var entries []skillFileEntry
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// Sniff first 8KB for binary detection.
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		buf := make([]byte, 8192)
		n, _ := f.Read(buf)
		f.Close()
		entries = append(entries, skillFileEntry{
			Path:   filepath.ToSlash(rel),
			Size:   info.Size(),
			Binary: isBinaryContent(buf[:n]),
		})
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to list skill files")
		return
	}
	if entries == nil {
		entries = []skillFileEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// GetSkillFile returns the content of a single file inside a skill directory.
func GetSkillFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	skill, ok := lookupSkill(w, slug)
	if !ok {
		return
	}

	abs, relPath, err := resolveSkillFilePath(skill.Slug, chi.URLParam(r, "*"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "File not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to read file")
		return
	}

	resp := skillFileContent{Path: relPath, Binary: isBinaryContent(data)}
	if !resp.Binary {
		resp.Content = string(data)
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutSkillFile writes new content to a file inside a skill directory. If the
// file is SKILL.md the frontmatter is re-parsed and the DB record updated.
func PutSkillFile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	skill, ok := lookupSkill(w, slug)
	if !ok {
		return
	}

	abs, relPath, err := resolveSkillFilePath(skill.Slug, chi.URLParam(r, "*"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Refuse to overwrite an existing binary file via the text editor.
	if existing, err := os.ReadFile(abs); err == nil && isBinaryContent(existing) {
		writeError(w, http.StatusBadRequest, "Binary files cannot be edited as text")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	var req skillFilePutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	newContent := []byte(req.Content)

	// SKILL.md must remain valid — re-parse before writing.
	var newFrontmatter *skillFrontmatter
	if relPath == "SKILL.md" {
		fm, err := parseSkillFrontmatter(newContent)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid SKILL.md: "+err.Error())
			return
		}
		newFrontmatter = fm
	}

	// Atomic write: tmp file + rename.
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to create directory")
		return
	}
	tmp := abs + ".tmp"
	if err := os.WriteFile(tmp, newContent, 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to write file")
		return
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		writeError(w, http.StatusInternalServerError, "Failed to commit file")
		return
	}

	if newFrontmatter != nil {
		skill.Name = newFrontmatter.Name
		skill.Summary = newFrontmatter.Description
		skill.RequiredEnvVars = encodeRequiredEnvVars(newFrontmatter.RequiredEnvVars)
		if err := database.DB.Save(skill).Error; err != nil {
			log.Printf("update skill record after SKILL.md edit: %v", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Delete skill
// ---------------------------------------------------------------------------

func DeleteSkill(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var skill database.Skill
	if err := database.DB.Where("slug = ?", slug).First(&skill).Error; err != nil {
		writeError(w, http.StatusNotFound, "Skill not found")
		return
	}

	// Sanitize slug to prevent path traversal — use the DB record's slug
	// which was validated on creation, not the URL parameter directly
	destDir := filepath.Join(config.Cfg.DataPath, "skills", skill.Slug)
	if err := os.RemoveAll(destDir); err != nil {
		log.Printf("delete skill dir: %v", err)
	}

	if err := database.DB.Delete(&skill).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to delete skill")
		return
	}

	var remaining int64
	database.DB.Model(&database.Skill{}).Count(&remaining)
	analytics.Track(r.Context(), analytics.EventSkillDeleted, map[string]any{
		"remaining_skills": remaining,
	})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Deploy skill
// ---------------------------------------------------------------------------

type deploySkillRequest struct {
	InstanceIDs []uint `json:"instance_ids"`
	Source      string `json:"source"`
	Version     string `json:"version,omitempty"`
}

type deploySkillResult struct {
	InstanceID     uint     `json:"instance_id"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
	MissingEnvVars []string `json:"missing_env_vars,omitempty"`
}

func DeploySkill(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var req deploySkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(req.InstanceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "No instance IDs specified")
		return
	}
	if req.Source == "" {
		req.Source = "library"
	}

	// Per-instance authorization: caller must be admin or manager of every
	// targeted instance's team. The route itself is not admin-gated so that
	// team managers can deploy library skills to their own team's instances.
	for _, instID := range req.InstanceIDs {
		if !middleware.CanMutateInstance(r, instID) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("Not authorized to deploy to instance %d", instID))
			return
		}
	}

	fileMap, err := buildSkillFileMap(r.Context(), slug, req.Source, req.Version)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to load skill: "+err.Error())
		return
	}

	// Determine the env var names this skill declares it needs. We parse the
	// frontmatter from the fileMap so the check works for both library and
	// clawhub sources without a DB lookup.
	var requiredEnvVars []string
	if skillMD, ok := fileMap["SKILL.md"]; ok {
		if fm, err := parseSkillFrontmatter(skillMD); err == nil {
			requiredEnvVars = fm.RequiredEnvVars
		}
	}

	// Globally-defined env var names (shared across all instances) — only the
	// names are needed, not the values, so we skip decryption.
	globalEnvNames := map[string]struct{}{}
	for _, k := range LoadGlobalEnvVarKeys() {
		globalEnvNames[k] = struct{}{}
	}

	// Async: register one task per target instance and return 202 with the
	// task IDs immediately. Per-instance results arrive over the SSE stream
	// driven by the TaskManager; the frontend renders them as toasts and
	// updates its skills page when the matching tasks end.
	taskIDs := make([]string, 0, len(req.InstanceIDs))
	if TaskMgr == nil {
		// Fallback for tests without a wired TaskMgr: keep synchronous
		// behaviour so unit tests don't need a manager.
		results := make([]deploySkillResult, len(req.InstanceIDs))
		var wg sync.WaitGroup
		for i, instID := range req.InstanceIDs {
			wg.Add(1)
			go func(idx int, instanceID uint) {
				defer wg.Done()
				result := deployToInstance(r.Context(), instanceID, slug, fileMap)
				result.MissingEnvVars = computeMissingEnvVars(instanceID, requiredEnvVars, globalEnvNames)
				results[idx] = result
			}(i, instID)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
		return
	}
	for _, instID := range req.InstanceIDs {
		instanceID := instID
		var displayName, instanceLabel string
		var inst database.Instance
		if err := database.DB.Select("display_name").First(&inst, instanceID).Error; err == nil {
			displayName = fmt.Sprintf("%s — %s", inst.DisplayName, slug)
			instanceLabel = inst.DisplayName
		} else {
			displayName = fmt.Sprintf("instance %d — %s", instanceID, slug)
			instanceLabel = fmt.Sprintf("instance %d", instanceID)
		}
		taskID := TaskMgr.Start(taskmanager.StartOpts{
			Type:         taskmanager.TaskSkillDeploy,
			InstanceID:   instanceID,
			UserID:       callerID(r),
			ResourceID:   slug,
			ResourceName: displayName,
			Title:        fmt.Sprintf("Deploying %s to %s", slug, instanceLabel),
			Run: func(ctx context.Context, h *taskmanager.Handle) error {
				h.UpdateMessage("uploading skill files")
				result := deployToInstance(ctx, instanceID, slug, fileMap)
				if result.Status != "ok" {
					if result.Error != "" {
						return fmt.Errorf("%s", result.Error)
					}
					return fmt.Errorf("deploy failed")
				}
				return nil
			},
		})
		taskIDs = append(taskIDs, taskID)
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"task_ids": taskIDs})
}

// computeMissingEnvVars returns the subset of requiredEnvVars that is neither
// defined globally nor per-instance. Missing env vars are a warning, not a
// failure — the deploy still proceeds.
func computeMissingEnvVars(instanceID uint, requiredEnvVars []string, globalEnvNames map[string]struct{}) []string {
	if len(requiredEnvVars) == 0 {
		return nil
	}
	instNames := map[string]struct{}{}
	var inst database.Instance
	if err := database.DB.First(&inst, instanceID).Error; err == nil {
		for k := range decodeEncryptedEnvVarsJSON(inst.EnvVars) {
			instNames[k] = struct{}{}
		}
	}
	var missing []string
	for _, name := range requiredEnvVars {
		if _, ok := globalEnvNames[name]; ok {
			continue
		}
		if _, ok := instNames[name]; ok {
			continue
		}
		missing = append(missing, name)
	}
	return missing
}

func buildSkillFileMap(ctx context.Context, slug, source, version string) (map[string][]byte, error) {
	if source == "library" {
		dir := filepath.Join(config.Cfg.DataPath, "skills", slug)
		fileMap := map[string][]byte{}
		err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			fileMap[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read skill files: %w", err)
		}
		return fileMap, nil
	}

	// clawhub source: download zip
	apiBase, err := getClawhubAPIBase(ctx)
	if err != nil {
		return nil, fmt.Errorf("clawhub unavailable: %w", err)
	}

	url := apiBase + "/api/v1/download?slug=" + slug
	if version != "" {
		url += "&version=" + version
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := clawhubHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch skill from clawhub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clawhub download returned %d", resp.StatusCode)
	}

	zipData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("invalid zip from clawhub: %w", err)
	}

	prefix := detectZipPrefix(zr.File)
	fileMap := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if prefix != "" {
			name = strings.TrimPrefix(name, prefix)
		}
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		fileMap[name] = data
	}
	return fileMap, nil
}

func deployToInstance(ctx context.Context, instanceID uint, slug string, fileMap map[string][]byte) deploySkillResult {
	result := deploySkillResult{InstanceID: instanceID}

	client, ok := SSHMgr.GetConnection(instanceID)
	if !ok {
		result.Status = "error"
		result.Error = "SSH not connected"
		return result
	}

	// Use path (not filepath) for remote Unix paths
	remoteBase := "/home/claworc/.openclaw/skills/" + slug

	// Clean up old MCP config if previously deployed
	oldSkillMD, err := sshproxy.ReadFile(client, remoteBase+"/SKILL.md")
	if err == nil {
		if oldFM, err := parseSkillFrontmatter(oldSkillMD); err == nil && oldFM.MCP != nil {
			instanceConn := sshproxy.NewSSHInstance(client)
			_, _, _, _ = instanceConn.ExecOpenclaw(ctx, "mcp", "unset", oldFM.MCP.Name)
			if oldFM.MCP.Transport == "sse" {
				orch := orchestrator.Get()
				if orch != nil {
					sidecarName := fmt.Sprintf("mcp-%d-%s", instanceID, slug)
					_ = orch.DeleteWorkload(ctx, orchestrator.WorkloadSpec{Name: sidecarName})
				}
			}
		}
	}

	if err := sshproxy.CreateDirectory(client, remoteBase); err != nil {
		result.Status = "error"
		result.Error = "Failed to create skill directory: " + err.Error()
		return result
	}

	for name, data := range fileMap {
		remotePath := path.Join(remoteBase, name)
		parentDir := path.Dir(remotePath)
		if parentDir != remoteBase {
			if err := sshproxy.CreateDirectory(client, parentDir); err != nil {
				result.Status = "error"
				result.Error = "Failed to create directory " + parentDir + ": " + err.Error()
				return result
			}
		}
		if err := sshproxy.WriteFile(client, remotePath, data); err != nil {
			result.Status = "error"
			result.Error = "Failed to write " + name + ": " + err.Error()
			return result
		}
	}

	// Check if this skill has an MCP configuration
	if skillMD, ok := fileMap["SKILL.md"]; ok {
		if fm, err := parseSkillFrontmatter(skillMD); err == nil && fm.MCP != nil {
			var inst database.Instance
			if err := database.DB.First(&inst, instanceID).Error; err != nil {
				result.Status = "error"
				result.Error = "Instance not found: " + err.Error()
				return result
			}

			globalEnv := LoadGlobalEnvVars()
			instEnv := LoadInstanceEnvVars(inst)
			mergedEnv := make(map[string]string)
			MergeUserEnvVars(mergedEnv, globalEnv, instEnv)

			instanceConn := sshproxy.NewSSHInstance(client)

			if fm.MCP.Transport == "sse" {
				if fm.MCP.Docker == nil {
					result.Status = "error"
					result.Error = "Docker config is required for SSE transport"
					return result
				}
				resolvedEnv := make(map[string]string)
				for k, v := range fm.MCP.Docker.Env {
					resolvedEnv[k] = resolvePlaceholders(v, mergedEnv)
				}

				orch := orchestrator.Get()
				if orch == nil {
					result.Status = "error"
					result.Error = "Orchestrator unavailable"
					return result
				}

				sidecarName := fmt.Sprintf("mcp-%d-%s", instanceID, slug)
				spec := orchestrator.WorkloadSpec{
					Name:    sidecarName,
					Image:   fm.MCP.Docker.Image,
					Command: fm.MCP.Docker.Command,
					Env:     resolvedEnv,
					Ports: []orchestrator.PortSpec{
						{
							ContainerPort: fm.MCP.Docker.Port,
						},
					},
					Labels: map[string]string{
						"managed-by":  "claworc",
						"type":        "mcp-sidecar",
						"instance_id": fmt.Sprintf("%d", instanceID),
						"skill":       slug,
					},
					IngressAllowedFrom: []string{
						inst.Name,
					},
				}
				if err := orch.Apply(ctx, spec); err != nil {
					result.Status = "error"
					result.Error = "Failed to start sidecar workload: " + err.Error()
					return result
				}

				healthy := false
				for attempt := 0; attempt < 30; attempt++ {
					status, err := orch.GetInstanceStatus(ctx, sidecarName)
					if err == nil && status == "running" {
						healthy = true
						break
					}
					select {
					case <-ctx.Done():
						result.Status = "error"
						result.Error = "Timeout waiting for sidecar container"
						return result
					case <-time.After(1 * time.Second):
					}
				}
				if !healthy {
					result.Status = "error"
					result.Error = "Sidecar container failed to start or become healthy"
					return result
				}

				url := fmt.Sprintf("http://%s:%d/sse", sidecarName, fm.MCP.Docker.Port)
				_, stderr, code, err := instanceConn.ExecOpenclaw(ctx, "mcp", "add", fm.MCP.Name, "--transport", "sse", "--url", url)
				if err != nil || code != 0 {
					result.Status = "error"
					result.Error = fmt.Sprintf("Failed to register MCP server over SSE: %v (stderr: %s)", err, stderr)
					return result
				}
			} else if fm.MCP.Transport == "stdio" {
				if fm.MCP.Local == nil {
					result.Status = "error"
					result.Error = "Local config is required for stdio transport"
					return result
				}
				resolvedEnv := make(map[string]string)
				for k, v := range fm.MCP.Local.Env {
					resolvedEnv[k] = resolvePlaceholders(v, mergedEnv)
				}

				cmdArgs := []string{"mcp", "add", fm.MCP.Name, "--transport", "stdio", "--command", fm.MCP.Local.Command}
				if len(fm.MCP.Local.Args) > 0 {
					joinedArgs := strings.Join(fm.MCP.Local.Args, " ")
					cmdArgs = append(cmdArgs, "--args", joinedArgs)
				}
				for k, v := range resolvedEnv {
					cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", k, v))
				}

				_, stderr, code, err := instanceConn.ExecOpenclaw(ctx, cmdArgs...)
				if err != nil || code != 0 {
					result.Status = "error"
					result.Error = fmt.Sprintf("Failed to register MCP server over Stdio: %v (stderr: %s)", err, stderr)
					return result
				}
			}
		}
	}

	// Upsert InstanceSkill
	if skillMD, ok := fileMap["SKILL.md"]; ok {
		if fm, err := parseSkillFrontmatter(skillMD); err == nil {
			instSkill := database.InstanceSkill{
				InstanceID: instanceID,
				Slug:       slug,
				Name:       fm.Name,
				Summary:    fm.Description,
				Status:     "deployed",
			}
			if err := database.DB.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "instance_id"}, {Name: "slug"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "summary", "status", "updated_at"}),
			}).Create(&instSkill).Error; err != nil {
				log.Printf("Failed to upsert InstanceSkill DB record: %v", err)
			}
		}
	}

	result.Status = "ok"
	return result
}

type undeploySkillRequest struct {
	InstanceIDs []uint `json:"instance_ids"`
}

type undeploySkillResult struct {
	InstanceID uint   `json:"instance_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func UndeploySkill(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var req undeploySkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(req.InstanceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "No instance IDs specified")
		return
	}

	for _, instID := range req.InstanceIDs {
		if !middleware.CanMutateInstance(r, instID) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("Not authorized to undeploy from instance %d", instID))
			return
		}
	}

	results := make([]undeploySkillResult, len(req.InstanceIDs))
	var wg sync.WaitGroup
	for i, instID := range req.InstanceIDs {
		wg.Add(1)
		go func(idx int, instanceID uint) {
			defer wg.Done()
			res := undeploySkillResult{InstanceID: instanceID, Status: "ok"}

			client, ok := SSHMgr.GetConnection(instanceID)
			if !ok {
				res.Status = "error"
				res.Error = "SSH not connected"
				results[idx] = res
				return
			}

			// Read SKILL.md to extract mcpConfig
			var skillMD []byte
			localPath := filepath.Join(config.Cfg.DataPath, "skills", slug, "SKILL.md")
			if data, err := os.ReadFile(localPath); err == nil {
				skillMD = data
			} else {
				if data, err := sshproxy.ReadFile(client, "/home/claworc/.openclaw/skills/"+slug+"/SKILL.md"); err == nil {
					skillMD = data
				}
			}

			if len(skillMD) > 0 {
				if fm, err := parseSkillFrontmatter(skillMD); err == nil && fm.MCP != nil {
					instanceConn := sshproxy.NewSSHInstance(client)
					// Run openclaw mcp unset <name>
					_, _, _, _ = instanceConn.ExecOpenclaw(r.Context(), "mcp", "unset", fm.MCP.Name)

					if fm.MCP.Transport == "sse" {
						orch := orchestrator.Get()
						if orch != nil {
							sidecarName := fmt.Sprintf("mcp-%d-%s", instanceID, slug)
							_ = orch.DeleteWorkload(r.Context(), orchestrator.WorkloadSpec{Name: sidecarName})
						}
					}
				}
			}

			// Delete skill files from instance directory
			remoteDir := "/home/claworc/.openclaw/skills/" + slug
			if err := sshproxy.DeletePath(client, remoteDir); err != nil {
				res.Status = "error"
				res.Error = "Failed to delete remote files: " + err.Error()
			} else {
				// Delete from database
				if err := database.DB.Where("instance_id = ? AND slug = ?", instanceID, slug).Delete(&database.InstanceSkill{}).Error; err != nil {
					log.Printf("Failed to delete InstanceSkill DB record: %v", err)
				}
			}

			results[idx] = res
		}(i, instID)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]interface{}{"results": results})
}

func ListInstanceSkills(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	var inst database.Instance
	if err := database.DB.First(&inst, id).Error; err != nil {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	if !middleware.CanAccessInstance(r, inst.ID) {
		writeError(w, http.StatusForbidden, "Access denied")
		return
	}

	client, ok := SSHMgr.GetConnection(inst.ID)
	if ok {
		// Online
		remoteDir := "/home/claworc/.openclaw/skills"
		entries, err := sshproxy.ListDirectory(client, remoteDir)
		if err != nil {
			// If the directory does not exist, delete all InstanceSkill records for this instance
			if strings.Contains(err.Error(), "No such file or directory") || strings.Contains(err.Error(), "exit status 2") {
				database.DB.Where("instance_id = ?", inst.ID).Delete(&database.InstanceSkill{})
				writeJSON(w, http.StatusOK, []database.InstanceSkill{})
				return
			}
			// Other errors: log and fallback
			ok = false
		} else {
			var seenSlugs []string
			for _, entry := range entries {
				if entry.Type != "directory" {
					continue
				}
				slug := entry.Name
				if !isValidSlug(slug) {
					continue
				}

				skillMD, err := sshproxy.ReadFile(client, remoteDir+"/"+slug+"/SKILL.md")
				if err != nil {
					continue
				}
				fm, err := parseSkillFrontmatter(skillMD)
				if err != nil {
					continue
				}

				instSkill := database.InstanceSkill{
					InstanceID: inst.ID,
					Slug:       slug,
					Name:       fm.Name,
					Summary:    fm.Description,
					Status:     "deployed",
				}
				err = database.DB.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "instance_id"}, {Name: "slug"}},
					DoUpdates: clause.AssignmentColumns([]string{"name", "summary", "status", "updated_at"}),
				}).Create(&instSkill).Error
				if err == nil {
					seenSlugs = append(seenSlugs, slug)
				}
			}

			if len(seenSlugs) > 0 {
				database.DB.Where("instance_id = ? AND slug NOT IN ?", inst.ID, seenSlugs).Delete(&database.InstanceSkill{})
			} else {
				database.DB.Where("instance_id = ?", inst.ID).Delete(&database.InstanceSkill{})
			}
		}
	}

	var skills []database.InstanceSkill
	if err := database.DB.Where("instance_id = ?", inst.ID).Find(&skills).Error; err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to query skills: "+err.Error())
		return
	}
	if skills == nil {
		skills = []database.InstanceSkill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

func ListInstanceSkillFiles(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	slug := chi.URLParam(r, "slug")
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "Invalid skill slug")
		return
	}

	var inst database.Instance
	if err := database.DB.First(&inst, id).Error; err != nil {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	if !middleware.CanAccessInstance(r, inst.ID) {
		writeError(w, http.StatusForbidden, "Access denied")
		return
	}

	client, ok := SSHMgr.GetConnection(inst.ID)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "Agent must be running to view skill files")
		return
	}

	remoteRoot := "/home/claworc/.openclaw/skills/" + slug
	quotedRoot := sshproxy.ShellQuote(remoteRoot)

	// Prune .git, node_modules, .venv directories, and find all files.
	// Note: dd if="$file" bs=1024 count=8 reads up to 8KB.
	cmd := fmt.Sprintf(`find %s \( -name .git -o -name node_modules -o -name .venv \) -prune -o -type f -exec sh -c '
  root=%s
  root_clean=${root%%/}
  for file do
    sz=$(wc -c < "$file")
    if [ $(dd if="$file" bs=1024 count=8 2>/dev/null | tr -d -c "\000" | wc -c) -gt 0 ]; then
      is_bin="true"
    else
      is_bin="false"
    fi
    rel_path=${file#"$root_clean/"}
    echo "$sz $is_bin $rel_path"
  done
' sh {} +`, quotedRoot, quotedRoot)

	stdout, stderr, code, err := sshproxy.RunCommand(client, cmd)
	if err != nil || code != 0 {
		writeError(w, http.StatusInternalServerError, "Failed to list remote files: "+stderr)
		return
	}

	var files []skillFileEntry
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		size, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		isBinary := parts[1] == "true"
		relPath := parts[2]

		files = append(files, skillFileEntry{
			Path:   relPath,
			Size:   size,
			Binary: isBinary,
		})
	}

	if files == nil {
		files = []skillFileEntry{}
	}

	writeJSON(w, http.StatusOK, files)
}

func GetInstanceSkillFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	slug := chi.URLParam(r, "slug")
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "Invalid skill slug")
		return
	}

	var inst database.Instance
	if err := database.DB.First(&inst, id).Error; err != nil {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	if !middleware.CanAccessInstance(r, inst.ID) {
		writeError(w, http.StatusForbidden, "Access denied")
		return
	}

	client, ok := SSHMgr.GetConnection(inst.ID)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "Agent must be running to view skill files")
		return
	}

	relPath := chi.URLParam(r, "*")
	absPath, err := resolveRemoteSkillFilePath(slug, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid file path: "+err.Error())
		return
	}

	// Size check over SSH
	sizeCmd := fmt.Sprintf("wc -c < %s", sshproxy.ShellQuote(absPath))
	sizeStdout, stderr, code, err := sshproxy.RunCommand(client, sizeCmd)
	if err != nil || code != 0 {
		if strings.Contains(stderr, "No such file or directory") || strings.Contains(err.Error(), "No such file") {
			writeError(w, http.StatusNotFound, "File not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to get file size: "+stderr)
		return
	}

	sizeStr := strings.TrimSpace(sizeStdout)
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to parse file size")
		return
	}

	if size > 2*1024*1024 {
		writeError(w, http.StatusRequestEntityTooLarge, "File exceeds 2MB size limit")
		return
	}

	data, err := sshproxy.ReadFile(client, absPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "File not found: "+err.Error())
		return
	}

	isBinary := bytes.Contains(data, []byte{0})
	var content string
	if !isBinary {
		content = string(data)
	}

	writeJSON(w, http.StatusOK, skillFileContent{
		Content: content,
		Binary:  isBinary,
	})
}

func PutInstanceSkillFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	slug := chi.URLParam(r, "slug")
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "Invalid skill slug")
		return
	}

	var inst database.Instance
	if err := database.DB.First(&inst, id).Error; err != nil {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	if !middleware.CanMutateInstance(r, inst.ID) {
		writeError(w, http.StatusForbidden, "Access denied")
		return
	}

	client, ok := SSHMgr.GetConnection(inst.ID)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "Agent must be running to modify skill files")
		return
	}

	relPath := chi.URLParam(r, "*")
	absPath, err := resolveRemoteSkillFilePath(slug, relPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid file path: "+err.Error())
		return
	}

	var reqBody struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// 2MB size cap check
	if len(reqBody.Content) > 2*1024*1024 {
		writeError(w, http.StatusRequestEntityTooLarge, "File content exceeds 2MB limit")
		return
	}

	isSKILLMD := relPath == "SKILL.md"
	var oldFM, newFM *skillFrontmatter
	if isSKILLMD {
		oldContent, err := sshproxy.ReadFile(client, absPath)
		if err == nil {
			oldFM, _ = parseSkillFrontmatter(oldContent)
		}
		var errParse error
		newFM, errParse = parseSkillFrontmatter([]byte(reqBody.Content))
		if errParse != nil {
			writeError(w, http.StatusBadRequest, "Invalid SKILL.md: "+errParse.Error())
			return
		}
	}

	err = sshproxy.WriteFile(client, absPath, []byte(reqBody.Content))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	if isSKILLMD {
		// Update Database entry
		instSkill := database.InstanceSkill{
			InstanceID: inst.ID,
			Slug:       slug,
			Name:       newFM.Name,
			Summary:    newFM.Description,
			Status:     "deployed",
		}
		database.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "instance_id"}, {Name: "slug"}},
			DoUpdates: clause.AssignmentColumns([]string{"name", "summary", "status", "updated_at"}),
		}).Create(&instSkill)

		// Check if config changed
		changed := false
		if oldFM == nil {
			changed = true
		} else if oldFM.Name != newFM.Name {
			changed = true
		} else if !reflect.DeepEqual(oldFM.MCP, newFM.MCP) {
			changed = true
		}

		if changed {
			instanceConn := sshproxy.NewSSHInstance(client)
			// Teardown old MCP if existed
			if oldFM != nil && oldFM.MCP != nil {
				_, _, _, _ = instanceConn.ExecOpenclaw(r.Context(), "mcp", "unset", oldFM.MCP.Name)
				if oldFM.MCP.Transport == "sse" {
					orch := orchestrator.Get()
					if orch != nil {
						sidecarName := fmt.Sprintf("mcp-%d-%s", inst.ID, slug)
						_ = orch.DeleteWorkload(r.Context(), orchestrator.WorkloadSpec{Name: sidecarName})
					}
				}
			}

			// Deploy new MCP if exists
			if newFM.MCP != nil {
				globalEnv := LoadGlobalEnvVars()
				instEnv := LoadInstanceEnvVars(inst)
				mergedEnv := make(map[string]string)
				MergeUserEnvVars(mergedEnv, globalEnv, instEnv)

				if newFM.MCP.Transport == "sse" {
					if newFM.MCP.Docker == nil {
						writeError(w, http.StatusBadRequest, "Docker config is required for SSE transport")
						return
					}
					resolvedEnv := make(map[string]string)
					for k, v := range newFM.MCP.Docker.Env {
						resolvedEnv[k] = resolvePlaceholders(v, mergedEnv)
					}

					orch := orchestrator.Get()
					if orch == nil {
						writeError(w, http.StatusInternalServerError, "Orchestrator unavailable")
						return
					}

					sidecarName := fmt.Sprintf("mcp-%d-%s", inst.ID, slug)
					spec := orchestrator.WorkloadSpec{
						Name:    sidecarName,
						Image:   newFM.MCP.Docker.Image,
						Command: newFM.MCP.Docker.Command,
						Env:     resolvedEnv,
						Ports: []orchestrator.PortSpec{
							{
								ContainerPort: newFM.MCP.Docker.Port,
							},
						},
						Labels: map[string]string{
							"managed-by":  "claworc",
							"type":        "mcp-sidecar",
							"instance_id": fmt.Sprintf("%d", inst.ID),
							"skill":       slug,
						},
						IngressAllowedFrom: []string{
							inst.Name,
						},
					}
					if err := orch.Apply(r.Context(), spec); err != nil {
						writeError(w, http.StatusInternalServerError, "Failed to apply sidecar: "+err.Error())
						return
					}

					// wait for healthy
					healthy := false
					for attempt := 0; attempt < 30; attempt++ {
						status, err := orch.GetInstanceStatus(r.Context(), sidecarName)
						if err == nil && status == "running" {
							healthy = true
							break
						}
						select {
						case <-r.Context().Done():
							break
						case <-time.After(1 * time.Second):
						}
					}
					if !healthy {
						writeError(w, http.StatusInternalServerError, "Sidecar failed to start")
						return
					}

					url := fmt.Sprintf("http://%s:%d/sse", sidecarName, newFM.MCP.Docker.Port)
					_, stderr, code, err := instanceConn.ExecOpenclaw(r.Context(), "mcp", "add", newFM.MCP.Name, "--transport", "sse", "--url", url)
					if err != nil || code != 0 {
						writeError(w, http.StatusInternalServerError, "Failed to register MCP server over SSE: "+stderr)
						return
					}
				} else if newFM.MCP.Transport == "stdio" {
					if newFM.MCP.Local == nil {
						writeError(w, http.StatusBadRequest, "Local config is required for stdio transport")
						return
					}
					resolvedEnv := make(map[string]string)
					for k, v := range newFM.MCP.Local.Env {
						resolvedEnv[k] = resolvePlaceholders(v, mergedEnv)
					}

					cmdArgs := []string{"mcp", "add", newFM.MCP.Name, "--transport", "stdio", "--command", newFM.MCP.Local.Command}
					if len(newFM.MCP.Local.Args) > 0 {
						joinedArgs := strings.Join(newFM.MCP.Local.Args, " ")
						cmdArgs = append(cmdArgs, "--args", joinedArgs)
					}
					for k, v := range resolvedEnv {
						cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", k, v))
					}

					_, stderr, code, err := instanceConn.ExecOpenclaw(r.Context(), cmdArgs...)
					if err != nil || code != 0 {
						writeError(w, http.StatusInternalServerError, "Failed to register MCP server over Stdio: "+stderr)
						return
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func StreamInstanceSkillLogs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid instance ID")
		return
	}

	slug := chi.URLParam(r, "slug")
	if !isValidSlug(slug) {
		writeError(w, http.StatusBadRequest, "Invalid skill slug")
		return
	}

	var inst database.Instance
	if err := database.DB.First(&inst, id).Error; err != nil {
		writeError(w, http.StatusNotFound, "Instance not found")
		return
	}

	if !middleware.CanAccessInstance(r, inst.ID) {
		writeError(w, http.StatusForbidden, "Access denied")
		return
	}

	// Determine sidecar name
	sidecarName := fmt.Sprintf("mcp-%d-%s", inst.ID, slug)

	orch := orchestrator.Get()
	if orch == nil {
		writeError(w, http.StatusInternalServerError, "Orchestrator unavailable")
		return
	}

	follow := true
	followParam := r.URL.Query().Get("follow")
	if followParam == "false" {
		follow = false
	}

	tail := int64(100)
	tailParam := r.URL.Query().Get("tail")
	if tailParam != "" {
		if t, err := strconv.ParseInt(tailParam, 10, 64); err == nil {
			tail = t
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	pr, pw := io.Pipe()
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	go func() {
		_ = orch.StreamWorkloadLogs(r.Context(), sidecarName, follow, tail, pw)
		pw.Close()
	}()

	scanner := bufio.NewScanner(pr)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 2*1024*1024) // up to 2MB buffer limit
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "data: %s\n\n", line)
		if flusher != nil {
			flusher.Flush()
		}
	}
	pr.Close()
}

