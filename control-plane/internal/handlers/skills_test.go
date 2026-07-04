package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/gluk-w/claworc/control-plane/internal/database"
	"github.com/gluk-w/claworc/control-plane/internal/middleware"
	"github.com/gluk-w/claworc/control-plane/internal/orchestrator"
	"github.com/gluk-w/claworc/control-plane/internal/sshproxy"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"
)

// sampleSkillPath returns the absolute path to skills/sampleskill/SKILL.md,
// resolved relative to this test file so it does not depend on CWD.
func sampleSkillPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = .../claworc/control-plane/internal/handlers/skills_test.go
	// target   = .../claworc/skills/sampleskill/SKILL.md
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "skills", "sampleskill", "SKILL.md")
}

func TestParseSkillFrontmatter_SampleSkillFile(t *testing.T) {
	t.Parallel()

	path := sampleSkillPath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample skill at %s: %v", path, err)
	}

	fm, err := parseSkillFrontmatter(content)
	if err != nil {
		t.Fatalf("parseSkillFrontmatter: %v", err)
	}

	if fm.Name != "sampleskill" {
		t.Errorf("Name = %q, want %q", fm.Name, "sampleskill")
	}
	if fm.Description == "" {
		t.Error("Description is empty")
	}
	want := []string{"API_KEY", "PROVIDER_NAME"}
	if !reflect.DeepEqual(fm.RequiredEnvVars, want) {
		t.Errorf("RequiredEnvVars = %v, want %v", fm.RequiredEnvVars, want)
	}
}

func TestParseSkillFrontmatter_ErrorCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		content     string
		wantErrSubs string
	}{
		{
			name:        "missing opening ---",
			content:     "name: foo\ndescription: bar\n",
			wantErrSubs: "missing frontmatter opening ---",
		},
		{
			name:        "missing closing ---",
			content:     "---\nname: foo\ndescription: bar\n",
			wantErrSubs: "missing frontmatter closing ---",
		},
		{
			name:        "missing name",
			content:     "---\ndescription: bar\n---\nbody\n",
			wantErrSubs: "missing name",
		},
		{
			name:        "missing description",
			content:     "---\nname: foo\n---\nbody\n",
			wantErrSubs: "missing description",
		},
		{
			name:        "malformed YAML",
			content:     "---\nname: [unclosed\n---\nbody\n",
			wantErrSubs: "parse frontmatter YAML",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseSkillFrontmatter([]byte(tc.content))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSubs)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubs) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErrSubs)
			}
		})
	}
}

func TestParseRequiredEnvVars_RoundTrip(t *testing.T) {
	t.Parallel()

	in := []string{"API_KEY", "PROVIDER_NAME"}
	encoded := encodeRequiredEnvVars(in)
	if encoded != `["API_KEY","PROVIDER_NAME"]` {
		t.Errorf("encoded = %q, want %q", encoded, `["API_KEY","PROVIDER_NAME"]`)
	}
	decoded := parseRequiredEnvVars(encoded)
	if !reflect.DeepEqual(decoded, in) {
		t.Errorf("decoded = %v, want %v", decoded, in)
	}
}

func TestParseRequiredEnvVars_EdgeCases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty string", "", []string{}},
		{"empty array literal", "[]", []string{}},
		{"invalid JSON", "not json", []string{}},
		{"JSON null", "null", []string{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseRequiredEnvVars(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseRequiredEnvVars(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestEncodeRequiredEnvVars_Empty(t *testing.T) {
	t.Parallel()

	if got := encodeRequiredEnvVars(nil); got != "[]" {
		t.Errorf("encodeRequiredEnvVars(nil) = %q, want %q", got, "[]")
	}
	if got := encodeRequiredEnvVars([]string{}); got != "[]" {
		t.Errorf("encodeRequiredEnvVars([]) = %q, want %q", got, "[]")
	}
}

func TestParseSkillFrontmatter_MCPConfig(t *testing.T) {
	t.Parallel()

	content := `---
name: mcp-skill
description: "MCP server skill"
mcp:
  name: github-mcp
  transport: sse
  docker:
    image: "mcp/github-server:latest"
    command: ["node", "/app/index.js"]
    port: 8080
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "{{GITHUB_TOKEN}}"
  local:
    command: "npx"
    args:
      - "-y"
      - "@modelcontextprotocol/server-github"
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "{{GITHUB_TOKEN}}"
---
body
`

	fm, err := parseSkillFrontmatter([]byte(content))
	if err != nil {
		t.Fatalf("parseSkillFrontmatter failed: %v", err)
	}

	if fm.MCP == nil {
		t.Fatal("expected MCP config to be populated, got nil")
	}
	if fm.MCP.Name != "github-mcp" {
		t.Errorf("MCP.Name = %q, want %q", fm.MCP.Name, "github-mcp")
	}
	if fm.MCP.Transport != "sse" {
		t.Errorf("MCP.Transport = %q, want %q", fm.MCP.Transport, "sse")
	}
	if fm.MCP.Docker == nil {
		t.Fatal("expected MCP.Docker to be populated, got nil")
	}
	if fm.MCP.Docker.Image != "mcp/github-server:latest" {
		t.Errorf("MCP.Docker.Image = %q", fm.MCP.Docker.Image)
	}
	if fm.MCP.Docker.Port != 8080 {
		t.Errorf("MCP.Docker.Port = %d", fm.MCP.Docker.Port)
	}
	if fm.MCP.Local == nil {
		t.Fatal("expected MCP.Local to be populated, got nil")
	}
	if fm.MCP.Local.Command != "npx" {
		t.Errorf("MCP.Local.Command = %q", fm.MCP.Local.Command)
	}
}

func TestResolvePlaceholders(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"GITHUB_TOKEN": "secret-token",
		"PORT":         "8080",
	}

	cases := []struct {
		input    string
		expected string
	}{
		{"{{GITHUB_TOKEN}}", "secret-token"},
		{"http://localhost:{{PORT}}/sse", "http://localhost:8080/sse"},
		{"{{MISSING_VAR}}", ""},
		{"no placeholder", "no placeholder"},
		{"{{GITHUB_TOKEN}} and {{PORT}} and {{MISSING}}", "secret-token and 8080 and "},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got := resolvePlaceholders(tc.input, env)
			if got != tc.expected {
				t.Errorf("resolvePlaceholders(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

type testOrchestrator struct {
	mockOrchestrator
	applied []orchestrator.WorkloadSpec
	deleted []orchestrator.WorkloadSpec
}

func (o *testOrchestrator) Apply(ctx context.Context, spec orchestrator.WorkloadSpec) error {
	o.applied = append(o.applied, spec)
	return nil
}

func (o *testOrchestrator) DeleteWorkload(ctx context.Context, spec orchestrator.WorkloadSpec) error {
	o.deleted = append(o.deleted, spec)
	return nil
}

func (o *testOrchestrator) GetInstanceStatus(ctx context.Context, name string) (string, error) {
	return "running", nil
}

func TestDeployAndUndeployMCPWorkflow(t *testing.T) {
	setupHandlersTestDB(t)

	// Create test instance
	inst := database.Instance{
		Name:        "bot-test-instance",
		DisplayName: "Bot Test Instance",
		Status:      "running",
	}
	if err := database.DB.Create(&inst).Error; err != nil {
		t.Fatalf("create instance: %v", err)
	}

	// Mock global env vars
	encoded, err := UpsertEncryptedEnvVarsJSON("{}", map[string]string{
		"API_KEY": "secret-key-123",
	}, nil)
	if err != nil {
		t.Fatalf("UpsertEncryptedEnvVarsJSON: %v", err)
	}
	if err := database.SetSetting("default_env_vars", encoded); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	// Setup fake SSH server
	fs := newFileTestFS()
	pubKeyBytes, privKeyPEM, err := sshproxy.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privKeyPEM)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	addr, sshCleanup := fileTestSSHServer(t, signer.PublicKey(), fs)
	defer sshCleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	mgr := sshproxy.NewSSHManager(signer, string(pubKeyBytes))
	defer mgr.CloseAll()
	SSHMgr = mgr
	defer func() { SSHMgr = nil }()

	_, err = mgr.Connect(context.Background(), inst.ID, host, port)
	if err != nil {
		t.Fatalf("SSH connect: %v", err)
	}

	// SSE transport test
	t.Run("sse", func(t *testing.T) {
		mockOrch := &testOrchestrator{}
		orchestrator.Set(mockOrch)
		defer orchestrator.Set(nil)

		fileMap := map[string][]byte{
			"SKILL.md": []byte(`---
name: sse-skill
description: "SSE MCP skill"
mcp:
  name: sse-mcp
  transport: sse
  docker:
    image: "mcp/sse-server:latest"
    port: 8081
    env:
      API_KEY: "{{API_KEY}}"
---
body
`),
		}

		res := deployToInstance(context.Background(), inst.ID, "sse-skill", fileMap)
		if res.Status != "ok" {
			t.Fatalf("deployToInstance failed: %s", res.Error)
		}

		// Verify sidecar was started via Apply
		if len(mockOrch.applied) != 1 {
			t.Fatalf("expected 1 sidecar applied, got %d", len(mockOrch.applied))
		}
		sidecar := mockOrch.applied[0]
		expectedName := fmt.Sprintf("mcp-%d-sse-skill", inst.ID)
		if sidecar.Name != expectedName {
			t.Errorf("sidecar.Name = %q, want %q", sidecar.Name, expectedName)
		}
		if sidecar.Image != "mcp/sse-server:latest" {
			t.Errorf("sidecar.Image = %q", sidecar.Image)
		}
		if sidecar.Env["API_KEY"] != "secret-key-123" {
			t.Errorf("sidecar.Env[API_KEY] = %q, want secret-key-123", sidecar.Env["API_KEY"])
		}
		if len(sidecar.IngressAllowedFrom) != 1 || sidecar.IngressAllowedFrom[0] != inst.Name {
			t.Errorf("sidecar.IngressAllowedFrom = %v, want [%s]", sidecar.IngressAllowedFrom, inst.Name)
		}

		// Now test UndeploySkill handler via HTTP
		reqBody := fmt.Sprintf(`{"instance_ids": [%d]}`, inst.ID)
		req, err := http.NewRequest("POST", "/skills/sse-skill/undeploy", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		// Set route context
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "sse-skill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		// Authorize as admin
		user := &database.User{ID: 1, Role: "admin"}
		req = req.WithContext(middleware.WithUser(req.Context(), user))

		rr := httptest.NewRecorder()
		UndeploySkill(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("UndeploySkill returned status %d, body: %s", rr.Code, rr.Body.String())
		}

		// Verify sidecar was deleted
		if len(mockOrch.deleted) != 1 {
			t.Fatalf("expected 1 sidecar deleted, got %d", len(mockOrch.deleted))
		}
		if mockOrch.deleted[0].Name != expectedName {
			t.Errorf("deleted sidecar name = %q, want %q", mockOrch.deleted[0].Name, expectedName)
		}
	})

	// Stdio transport test
	t.Run("stdio", func(t *testing.T) {
		mockOrch := &testOrchestrator{}
		orchestrator.Set(mockOrch)
		defer orchestrator.Set(nil)

		fileMap := map[string][]byte{
			"SKILL.md": []byte(`---
name: stdio-skill
description: "Stdio MCP skill"
mcp:
  name: stdio-mcp
  transport: stdio
  local:
    command: "node"
    args:
      - "index.js"
    env:
      API_KEY: "{{API_KEY}}"
---
body
`),
		}

		res := deployToInstance(context.Background(), inst.ID, "stdio-skill", fileMap)
		if res.Status != "ok" {
			t.Fatalf("deployToInstance failed: %s", res.Error)
		}

		// Stdio doesn't apply any container workloads
		if len(mockOrch.applied) != 0 {
			t.Errorf("expected 0 sidecars applied, got %d", len(mockOrch.applied))
		}

		// Clean up via UndeploySkill
		reqBody := fmt.Sprintf(`{"instance_ids": [%d]}`, inst.ID)
		req, err := http.NewRequest("POST", "/skills/stdio-skill/undeploy", strings.NewReader(reqBody))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", "stdio-skill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		user := &database.User{ID: 1, Role: "admin"}
		req = req.WithContext(middleware.WithUser(req.Context(), user))

		rr := httptest.NewRecorder()
		UndeploySkill(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("UndeploySkill returned status %d, body: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestIsValidSlug(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		{"my-skill", true},
		{"skill_123", true},
		{"my-skill-123", true},
		{"my/skill", false},
		{"..", false},
		{"", false},
		{"skill!", false},
	}
	for _, tc := range cases {
		got := isValidSlug(tc.slug)
		if got != tc.want {
			t.Errorf("isValidSlug(%q) = %v, want %v", tc.slug, got, tc.want)
		}
	}
}

func TestResolveRemoteSkillFilePath(t *testing.T) {
	cases := []struct {
		slug    string
		relPath string
		wantErr bool
		wantAbs string
	}{
		{"my-skill", "index.js", false, "/home/claworc/.openclaw/skills/my-skill/index.js"},
		{"my-skill", "src/helper.js", false, "/home/claworc/.openclaw/skills/my-skill/src/helper.js"},
		{"my-skill", "SKILL.md", false, "/home/claworc/.openclaw/skills/my-skill/SKILL.md"},
		{"my-skill", "../other-skill/index.js", true, ""},
		{"my-skill", "/etc/passwd", true, ""},
		{"my-skill", "src/../../etc/passwd", true, ""},
		{"invalid/slug", "index.js", true, ""},
	}
	for _, tc := range cases {
		got, err := resolveRemoteSkillFilePath(tc.slug, tc.relPath)
		if (err != nil) != tc.wantErr {
			t.Errorf("resolveRemoteSkillFilePath(%q, %q) err = %v, wantErr = %v", tc.slug, tc.relPath, err, tc.wantErr)
		}
		if err == nil && got != tc.wantAbs {
			t.Errorf("resolveRemoteSkillFilePath(%q, %q) = %q, want %q", tc.slug, tc.relPath, got, tc.wantAbs)
		}
	}
}

func TestInstanceSkillHandlers_RoutingAndValidation(t *testing.T) {
	// Create a test DB and instance for route testing
	setupTestDB(t)
	inst := &database.Instance{
		Name:        "test-skill-agent",
		DisplayName: "Test Skill Agent",
		Status:      "running",
	}
	if err := database.DB.Create(inst).Error; err != nil {
		t.Fatalf("Create Instance: %v", err)
	}

	user := &database.User{ID: 1, Role: "admin"}

	t.Run("ListInstanceSkills_InvalidSlug", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/instances/999/skills", nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		// We'll set an invalid slug in a custom request test if we can, but ListInstanceSkills does not take slug.
	})

	t.Run("ListInstanceSkillFiles_InvalidSlug", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/instances/1/skills/../files", nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "invalid/slug")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		ListInstanceSkillFiles(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid slug, got %d", rr.Code)
		}
	})

	t.Run("GetInstanceSkillFile_InvalidSlug", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/instances/1/skills/../files/SKILL.md", nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "invalid/slug")
		rctx.URLParams.Add("*", "SKILL.md")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		GetInstanceSkillFile(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid slug, got %d", rr.Code)
		}
	})

	t.Run("PutInstanceSkillFile_InvalidSlug", func(t *testing.T) {
		req, _ := http.NewRequest("PUT", "/instances/1/skills/../files/SKILL.md", strings.NewReader(`{"content":"test"}`))
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "invalid/slug")
		rctx.URLParams.Add("*", "SKILL.md")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		PutInstanceSkillFile(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid slug, got %d", rr.Code)
		}
	})

	t.Run("StreamInstanceSkillLogs_InvalidSlug", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/instances/1/skills/../logs", nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "invalid/slug")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		StreamInstanceSkillLogs(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid slug, got %d", rr.Code)
		}
	})
}

func TestInstanceSkillHandlers_HappyAndEdge(t *testing.T) {
	setupTestDB(t)

	// Create test instance
	inst := &database.Instance{
		Name:        "agent-under-test",
		DisplayName: "Agent Under Test",
		Status:      "running",
	}
	if err := database.DB.Create(inst).Error; err != nil {
		t.Fatalf("Create Instance: %v", err)
	}

	user := &database.User{ID: 1, Role: "admin"}

	// Set up mock SSH connection and filesystem
	fs := newFileTestFS()
	// Add some files inside a skill directory
	fs.dirs["/home/claworc/.openclaw/skills"] = true
	fs.dirs["/home/claworc/.openclaw/skills/test-skill"] = true
	fs.files["/home/claworc/.openclaw/skills/test-skill/SKILL.md"] = []byte(`---
name: test-skill
description: "A test skill for handlers"
mcp:
  name: test-mcp
  transport: stdio
  local:
    command: "python3"
    args: ["main.py"]
---
SKILL.md body
`)
	fs.files["/home/claworc/.openclaw/skills/test-skill/main.py"] = []byte("print('hello')")
	fs.files["/home/claworc/.openclaw/skills/test-skill/large.txt"] = make([]byte, 3*1024*1024) // 3MB file
	fs.files["/home/claworc/.openclaw/skills/test-skill/binary.dat"] = []byte{0x00, 0x01, 0x02} // binary file

	// Mock SSHManager
	_, privKeyPEM, err := sshproxy.GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(privKeyPEM)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	addr, sshCleanup := fileTestSSHServer(t, signer.PublicKey(), fs)
	defer sshCleanup()

	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	mgr := sshproxy.NewSSHManager(signer, "ssh-public-key-data")
	defer mgr.CloseAll()
	SSHMgr = mgr
	defer func() { SSHMgr = nil }()

	_, err = mgr.Connect(context.Background(), inst.ID, host, port)
	if err != nil {
		t.Fatalf("SSH connect: %v", err)
	}

	t.Run("ListInstanceSkills_OnlineAndOffline", func(t *testing.T) {
		// 1. Online: should scan and populate DB
		req, _ := http.NewRequest("GET", fmt.Sprintf("/instances/%d/skills", inst.ID), nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		ListInstanceSkills(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("ListInstanceSkills online got status %d, body: %s", rr.Code, rr.Body.String())
		}

		var onlineSkills []database.InstanceSkill
		if err := json.Unmarshal(rr.Body.Bytes(), &onlineSkills); err != nil {
			t.Fatalf("Unmarshal skills: %v", err)
		}
		if len(onlineSkills) != 1 || onlineSkills[0].Slug != "test-skill" {
			t.Errorf("expected 1 skill with slug 'test-skill', got %+v", onlineSkills)
		}

		// Verify it was saved to DB
		var count int64
		database.DB.Model(&database.InstanceSkill{}).Where("instance_id = ? AND slug = ?", inst.ID, "test-skill").Count(&count)
		if count != 1 {
			t.Error("expected InstanceSkill to be saved in DB")
		}

		// 2. Offline: disconnect and verify it falls back to DB cached record
		mgr.CloseAll()
		rrOffline := httptest.NewRecorder()
		ListInstanceSkills(rrOffline, req)
		if rrOffline.Code != http.StatusOK {
			t.Errorf("ListInstanceSkills offline got status %d", rrOffline.Code)
		}
		var offlineSkills []database.InstanceSkill
		json.Unmarshal(rrOffline.Body.Bytes(), &offlineSkills)
		if len(offlineSkills) != 1 || offlineSkills[0].Slug != "test-skill" {
			t.Errorf("expected offline fallback to return cached skill, got %+v", offlineSkills)
		}

		// Reconnect for remaining tests
		_, err = mgr.Connect(context.Background(), inst.ID, host, port)
		if err != nil {
			t.Fatalf("SSH reconnect: %v", err)
		}
	})

	t.Run("ListInstanceSkillFiles", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("/instances/%d/skills/test-skill/files", inst.ID), nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "test-skill")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		ListInstanceSkillFiles(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("ListInstanceSkillFiles got status %d, body: %s", rr.Code, rr.Body.String())
		}

		var files []skillFileEntry
		if err := json.Unmarshal(rr.Body.Bytes(), &files); err != nil {
			t.Fatalf("Unmarshal files: %v", err)
		}
		// Expect SKILL.md, main.py, large.txt, binary.dat
		if len(files) != 4 {
			t.Errorf("expected 4 files, got %d", len(files))
		}

		// Find binary.dat and check binary flag
		var binEntry *skillFileEntry
		for i := range files {
			if files[i].Path == "binary.dat" {
				binEntry = &files[i]
			}
		}
		if binEntry == nil || !binEntry.Binary {
			t.Errorf("expected binary.dat to be marked as binary, got %+v", binEntry)
		}
	})

	t.Run("GetInstanceSkillFile", func(t *testing.T) {
		// Happy path: reading text file
		req, _ := http.NewRequest("GET", fmt.Sprintf("/instances/%d/skills/test-skill/files/main.py", inst.ID), nil)
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "test-skill")
		rctx.URLParams.Add("*", "main.py")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		GetInstanceSkillFile(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("GetInstanceSkillFile got status %d, body: %s", rr.Code, rr.Body.String())
		}

		var content skillFileContent
		if err := json.Unmarshal(rr.Body.Bytes(), &content); err != nil {
			t.Fatalf("Unmarshal content: %v", err)
		}
		if content.Content != "print('hello')" || content.Binary {
			t.Errorf("unexpected content: %+v", content)
		}

		// Size cap path: reading 3MB file should be rejected with 413
		reqLarge, _ := http.NewRequest("GET", fmt.Sprintf("/instances/%d/skills/test-skill/files/large.txt", inst.ID), nil)
		reqLarge = reqLarge.WithContext(middleware.WithUser(reqLarge.Context(), user))
		rctxLarge := chi.NewRouteContext()
		rctxLarge.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctxLarge.URLParams.Add("slug", "test-skill")
		rctxLarge.URLParams.Add("*", "large.txt")
		reqLarge = reqLarge.WithContext(context.WithValue(reqLarge.Context(), chi.RouteCtxKey, rctxLarge))

		rrLarge := httptest.NewRecorder()
		GetInstanceSkillFile(rrLarge, reqLarge)
		if rrLarge.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("expected 413 Payload Too Large, got %d", rrLarge.Code)
		}
	})

	t.Run("PutInstanceSkillFile_AndConfigReload", func(t *testing.T) {
		mockOrch := &testOrchestrator{}
		orchestrator.Set(mockOrch)
		defer orchestrator.Set(nil)

		// Updating SKILL.md to transport: sse, which should trigger Apply workload and new MCP add
		newContent := `---
name: test-skill
description: "Updated description"
mcp:
  name: test-mcp-sse
  transport: sse
  docker:
    image: "mcp/sse-server:v2"
    port: 8082
    env:
      VAL: "hello"
---
`
		reqBody := fmt.Sprintf(`{"content": %q}`, newContent)
		req, _ := http.NewRequest("PUT", fmt.Sprintf("/instances/%d/skills/test-skill/files/SKILL.md", inst.ID), strings.NewReader(reqBody))
		req = req.WithContext(middleware.WithUser(req.Context(), user))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctx.URLParams.Add("slug", "test-skill")
		rctx.URLParams.Add("*", "SKILL.md")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

		rr := httptest.NewRecorder()
		PutInstanceSkillFile(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("PutInstanceSkillFile got status %d, body: %s", rr.Code, rr.Body.String())
		}

		// Verify sidecar was applied
		if len(mockOrch.applied) != 1 {
			t.Fatalf("expected 1 sidecar container applied, got %d", len(mockOrch.applied))
		}
		if mockOrch.applied[0].Image != "mcp/sse-server:v2" {
			t.Errorf("expected image 'mcp/sse-server:v2', got %q", mockOrch.applied[0].Image)
		}

		// Size cap check: writing content > 2MB should be rejected
		largeContent := strings.Repeat("A", 2*1024*1024+1)
		reqLargeBody := fmt.Sprintf(`{"content": %q}`, largeContent)
		reqLarge, _ := http.NewRequest("PUT", fmt.Sprintf("/instances/%d/skills/test-skill/files/main.py", inst.ID), strings.NewReader(reqLargeBody))
		reqLarge = reqLarge.WithContext(middleware.WithUser(reqLarge.Context(), user))
		rctxLarge := chi.NewRouteContext()
		rctxLarge.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
		rctxLarge.URLParams.Add("slug", "test-skill")
		rctxLarge.URLParams.Add("*", "main.py")
		reqLarge = reqLarge.WithContext(context.WithValue(reqLarge.Context(), chi.RouteCtxKey, rctxLarge))

		rrLarge := httptest.NewRecorder()
		PutInstanceSkillFile(rrLarge, reqLarge)
		if rrLarge.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("expected 413 Payload Too Large on write, got %d", rrLarge.Code)
		}
	})
}

type mockOrchestratorWithLogs struct {
	mockOrchestrator
	logLines []string
}

func (m *mockOrchestratorWithLogs) StreamWorkloadLogs(ctx context.Context, name string, follow bool, tailLines int64, writer io.Writer) error {
	for _, line := range m.logLines {
		writer.Write([]byte(line + "\n"))
	}
	return nil
}

func TestStreamInstanceSkillLogs_ScannerBuffer(t *testing.T) {
	setupTestDB(t)
	inst := &database.Instance{
		Name:   "log-agent",
		Status: "running",
	}
	database.DB.Create(inst)
	user := &database.User{ID: 1, Role: "admin"}

	// Create a very long log line of 1.5MB to verify the 2MB scanner buffer
	longLine := strings.Repeat("A", 1500000)
	mockOrch := &mockOrchestratorWithLogs{
		logLines: []string{
			"short line 1",
			longLine,
			"short line 2",
		},
	}
	orchestrator.Set(mockOrch)
	defer orchestrator.Set(nil)

	req, _ := http.NewRequest("GET", fmt.Sprintf("/instances/%d/skills/test-skill/logs", inst.ID), nil)
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", fmt.Sprintf("%d", inst.ID))
	rctx.URLParams.Add("slug", "test-skill")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	StreamInstanceSkillLogs(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("StreamInstanceSkillLogs got status %d", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "data: short line 1\n\n") {
		t.Error("missing first line")
	}
	if !strings.Contains(body, "data: short line 2\n\n") {
		t.Error("missing third line")
	}
	if !strings.Contains(body, "data: "+longLine+"\n\n") {
		t.Error("missing the 1.5MB long log line")
	}
}


