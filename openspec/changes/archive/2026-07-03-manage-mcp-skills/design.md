# Technical Design: MCP Skills Integration

This document outlines the technical design for integrating Model Context Protocol (MCP) servers packaged as Skills within Claworc. It describes how the control plane parses metadata, resolves environment variables, orchestrates sidecar containers (SSE transport), executes local processes (Stdio transport), and manages the lifecycle of these servers.

---

## 1. Architecture Overview

Claworc will support two distinct execution models (transports) for MCP servers defined within skills:

1. **SSE Transport (Sidecar Workloads)**: Suitable for Docker-packaged MCP servers. The control plane deploys a sibling sidecar container on the same network namespace/network (Docker bridge network or Kubernetes namespace) as the agent, exposes its SSE port, and registers it with the agent using `openclaw mcp add`.
2. **Stdio Transport (Agent-Local Processes)**: Suitable for script/executable-based MCP servers. The control plane instructs the agent container via SSH to launch and monitor the MCP server as a local child process using standard input/output.

### 1.1 Workload Data Flow (SSE Transport)

```mermaid
graph TD
    subgraph Control Plane
        H[Skills Handler] -->|1. Parse Frontmatter| FM[MCP Config]
        H -->|2. Resolve Env Vars| Env[Resolved Env Map]
        H -->|3. Apply| Orch[Docker/K8s Orchestrator]
    end
    subgraph Container Network (claworc / K8s NS)
        Agent[Agent Instance Container] -->|5. HTTP/SSE request| Sidecar[MCP Sidecar Container]
    end
    Orch -->|4. Launch Container| Sidecar
    H -->|6. Exec openclaw mcp add| Agent
```

### 1.2 Workload Data Flow (Stdio Transport)

```mermaid
graph TD
    subgraph Control Plane
        H[Skills Handler] -->|1. Parse Frontmatter| FM[MCP Config]
        H -->|2. Copy Files| AgentFiles[/.openclaw/skills/slug]
        H -->|3. Exec openclaw mcp add| Agent[Agent Instance Container]
    end
    subgraph Agent Instance Container
        Agent -->|4. Spawn child process| StdioProc[Local Stdio Process]
        Agent <-->|5. Stdio Pipe| StdioProc
    end
```

---

## 2. Data Models & Frontmatter Schema

### 2.1 Frontmatter Schema Extension

A new `mcp` block is introduced in the YAML frontmatter of `SKILL.md`.

```yaml
---
name: sample-mcp-skill
description: "Sample MCP server skill integration"
required_env_vars:
  - GITHUB_TOKEN

# Model Context Protocol configuration
mcp:
  name: github-mcp
  transport: sse           # "sse" or "stdio"
  
  # Configuration if running as an orchestrated container (SSE)
  docker:
    image: "mcp/github-server:latest"
    command: ["node", "/app/index.js"] # Optional
    port: 8080             # Target port for SSE
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "{{GITHUB_TOKEN}}"
  
  # Configuration if running as a local process (Stdio)
  local:
    command: "npx"
    args:
      - "-y"
      - "@modelcontextprotocol/server-github"
    env:
      GITHUB_PERSONAL_ACCESS_TOKEN: "{{GITHUB_TOKEN}}"
---
```

### 2.2 Go Implementation Structures

We will modify [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) to define the new parsing structs:

```go
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

// Extended skillFrontmatter
type skillFrontmatter struct {
	Name            string     `yaml:"name"`
	Description     string     `yaml:"description"`
	RequiredEnvVars []string   `yaml:"required_env_vars,omitempty"`
	MCP             *mcpConfig `yaml:"mcp,omitempty"`
}
```

The existing [parseSkillFrontmatter](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L156) function will automatically populate the `MCP` configuration field when deserializing `SKILL.md` via `yaml.Unmarshal`.

---

## 3. Environment Variable Resolution

Environment variables specified in the MCP configuration (e.g. `{{GITHUB_TOKEN}}`) must be resolved using the target instance's merged environment.

### 3.1 Resolving Placeholders

We will implement a regex-based resolver function in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go):

```go
import "regexp"

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
```

### 3.2 Loading Instance Environment

The environment will be loaded during the deployment task using helpers defined in [envvars.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/envvars.go):

```go
// Loaded for target instanceID
globalEnv := LoadGlobalEnvVars()
instEnv := LoadInstanceEnvVars(dbInst)
mergedEnv := make(map[string]string)
MergeUserEnvVars(mergedEnv, globalEnv, instEnv)
```

The parsed MCP environment mapping will be resolved against `mergedEnv`:

```go
resolvedEnv := make(map[string]string)
for k, v := range rawMcpEnv {
	resolvedEnv[k] = resolvePlaceholders(v, mergedEnv)
}
```

---

## 4. Sidecar Containers Orchestration (SSE Transport)

When deploying a skill with `transport: sse`, the control plane starts a sidecar container using the `ContainerOrchestrator` interface.

### 4.1 Sidecar Naming & Identifier

To prevent name collisions across instances, the workload will be named using the convention:
`mcp-<instance-id>-<skill-slug>`

This name is used as the container name in Docker and the deployment/service name in Kubernetes.

### 4.2 Network Policy Enhancement (Kubernetes)

On Kubernetes, the orchestrator deploys workloads in a single namespace (determined by `config.Cfg.K8sNamespace`) but applies a strict per-workload NetworkPolicy to deny ingress by default.
We must modify the orchestrator models to allow ingress to the sidecar from the agent pod.

1. Add `IngressAllowedFrom []string` to `WorkloadSpec` in [spec.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/spec.go):

```diff
type WorkloadSpec struct {
	Name string

	Image     string
	Command   []string
	Env       map[string]string
	Resources ResourceParams
+	// IngressAllowedFrom list of app labels allowed to reach this workload
+	IngressAllowedFrom []string
```

2. Update [applyNetworkPolicy](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes_apply.go#L218) in [kubernetes_apply.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes_apply.go) to append ingress rules for these targets:

```diff
	desired := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: ns,
			Labels:    mergeLabels(spec.Labels, map[string]string{"app": spec.Name, "managed-by": "claworc"}),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": spec.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
-			Ingress: []networkingv1.NetworkPolicyIngressRule{{
-				From: []networkingv1.NetworkPolicyPeer{{
-					PodSelector: &metav1.LabelSelector{MatchLabels: controlPlaneSelector},
-				}},
-				Ports: policyPorts,
-			}},
+			Ingress: func() []networkingv1.NetworkPolicyIngressRule {
+				rules := []networkingv1.NetworkPolicyIngressRule{{
+					From: []networkingv1.NetworkPolicyPeer{{
+						PodSelector: &metav1.LabelSelector{MatchLabels: controlPlaneSelector},
+					}},
+					Ports: policyPorts,
+				}}
+				for _, appName := range spec.IngressAllowedFrom {
+					rules = append(rules, networkingv1.NetworkPolicyIngressRule{
+						From: []networkingv1.NetworkPolicyPeer{{
+							PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": appName}},
+						}},
+						Ports: policyPorts,
+					})
+				}
+				return rules
+			}(),
		},
	}
```

### 4.3 Workload Specification & Registration

For Docker and Kubernetes, the sidecar is spun up by calling `orchestrator.Apply`:

```go
orch := orchestrator.Get()
spec := orchestrator.WorkloadSpec{
	Name:    fmt.Sprintf("mcp-%d-%s", instanceID, slug),
	Image:   mcp.Docker.Image,
	Command: mcp.Docker.Command,
	Env:     resolvedEnv,
	Ports: []orchestrator.PortSpec{
		{
			ContainerPort: mcp.Docker.Port,
		},
	},
	Labels: map[string]string{
		"managed-by":  "claworc",
		"type":        "mcp-sidecar",
		"instance_id": fmt.Sprintf("%d", instanceID),
		"skill":       slug,
	},
	IngressAllowedFrom: []string{
		inst.Name, // allow the agent instance container (e.g., bot-1) to connect
	},
}
err := orch.Apply(ctx, spec)
```

Once running, the sidecar is registered via `openclaw mcp add` in the agent:

```go
url := fmt.Sprintf("http://mcp-%d-%s:%d/sse", instanceID, slug, mcp.Docker.Port)
_, stderr, code, err := instConn.ExecOpenclaw(ctx, "mcp", "add", mcp.Name, "--transport", "sse", "--url", url)
```

---

## 5. Local Processes Orchestration (Stdio Transport)

When deploying a skill with `transport: stdio`, no sidecar workload is created. The control plane registers the process command and arguments to be spawned directly inside the agent container.

### 5.1 Registration Command

The control plane invokes `openclaw mcp add` via SSH connection `instConn`:

```go
cmdArgs := []string{"mcp", "add", mcp.Name, "--transport", "stdio", "--command", mcp.Local.Command}
if len(mcp.Local.Args) > 0 {
	joinedArgs := strings.Join(mcp.Local.Args, " ")
	cmdArgs = append(cmdArgs, "--args", joinedArgs)
}
for k, v := range resolvedEnv {
	cmdArgs = append(cmdArgs, "--env", fmt.Sprintf("%s=%s", k, v))
}

_, stderr, code, err := instConn.ExecOpenclaw(ctx, cmdArgs...)
```

---

## 6. Lifecycle Operations & API Changes

### 6.1 Skill Deployment & Update Hook

We will modify [deployToInstance](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go#L1018) in [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) to coordinate the MCP server startup:

1. **Parse `SKILL.md`**: Extract the `mcpConfig` block.
2. **Undeploy Old Sidecars**: If the skill was previously deployed and contains an MCP server, clean up any previous sidecar and run `openclaw mcp unset <old_name>` first to ensure updates roll out cleanly.
3. **Copy Skill Files**: Perform the existing directory copy via SSH.
4. **Deploy MCP Workload**:
   - If `transport: sse`, invoke `orchestrator.Apply` and wait for availability, then call `openclaw mcp add` with the SSE URL.
   - If `transport: stdio`, call `openclaw mcp add` with the stdio settings.

### 6.2 Proposing Undeploy Skill API Endpoint

To cleanly remove a deployed skill, we will introduce a new endpoint.

- **Route**: `POST /skills/{slug}/undeploy`
- **Controller**: `handlers.UndeploySkill`
- **Request Body**: `{"instance_ids": [1, 2]}`
- **Permissions**: Managers of the target instance's team or Admins.

#### Undeploy Implementation Flow:
For each `instance_id`:
1. Connect via SSH.
2. Read the `SKILL.md` from the instance or the local library to parse the `mcpConfig`.
3. If an MCP server is configured:
   - Call `openclaw mcp unset <mcp.Name>` to deregister it.
   - If `transport: sse`, invoke `orchestrator.DeleteWorkload` on `mcp-<instance-id>-<skill-slug>` to stop and remove the sidecar container and its resources.
4. Remove the skill files directory: `/home/claworc/.openclaw/skills/<slug>`.

### 6.3 Instance Deletion Hook

When an instance is deleted via [DeleteInstance](file:///home/ubuntu/claworc/control-plane/internal/handlers/instances.go#L1592) in [instances.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/instances.go), all of its sidecar containers must be stopped and deleted.

Add the following cleanup logic to [DeleteInstance](file:///home/ubuntu/claworc/control-plane/internal/handlers/instances.go#L1592):

```go
// Clean up any MCP sidecar containers for this instance
var skills []database.Skill
if err := database.DB.Find(&skills).Error; err == nil {
	for _, skill := range skills {
		skillDir := filepath.Join(config.Cfg.DataPath, "skills", skill.Slug)
		content, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err == nil {
			if fm, err := parseSkillFrontmatter(content); err == nil && fm.MCP != nil && fm.MCP.Transport == "sse" {
				sidecarName := fmt.Sprintf("mcp-%d-%s", inst.ID, skill.Slug)
				_ = orch.DeleteWorkload(r.Context(), orchestrator.WorkloadSpec{Name: sidecarName})
			}
		}
	}
}
```

---

## 7. File Changes & Impact Analysis

| File | Change Type | Description |
|---|---|---|
| [main.go](file:///home/ubuntu/claworc/control-plane/main.go) | Modified | Register route `POST /skills/{slug}/undeploy` to handle skill undeployment. |
| [skills.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/skills.go) | Modified | - Add `mcpConfig`, `mcpDockerConfig`, `mcpLocalConfig` structures.<br>- Extend `skillFrontmatter` to parse `mcp` blocks.<br>- Add regex-based `resolvePlaceholders` helper.<br>- Implement `UndeploySkill` handler.<br>- Update `deployToInstance` to apply container sidecars or register local processes. |
| [instances.go](file:///home/ubuntu/claworc/control-plane/internal/handlers/instances.go) | Modified | Update `DeleteInstance` to find and delete any registered sidecar workloads for that instance using `orchestrator.DeleteWorkload`. |
| [spec.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/spec.go) | Modified | Add `IngressAllowedFrom []string` to the `WorkloadSpec` struct. |
| [kubernetes_apply.go](file:///home/ubuntu/claworc/control-plane/internal/orchestrator/kubernetes_apply.go) | Modified | Modify `applyNetworkPolicy` to allow ingress on exposed ports from the workloads listed in `spec.IngressAllowedFrom`. |
