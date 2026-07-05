import client from "./client";
import type {
  Skill,
  ClawhubSearchResponse,
  DeployResponse,
  InstanceSkill,
  SkillFileEntry,
  SkillFileContent,
} from "@common/types/skills";

export async function listSkills(): Promise<Skill[]> {
  const res = await client.get<Skill[]>("/skills");
  return res.data;
}

export async function uploadSkill(file: File, overwrite = false): Promise<Skill> {
  const form = new FormData();
  form.append("file", file);
  const res = await client.post<Skill>(`/skills${overwrite ? "?overwrite=true" : ""}`, form, {
    headers: { "Content-Type": "multipart/form-data" },
  });
  return res.data;
}

export async function deleteSkill(slug: string): Promise<void> {
  await client.delete(`/skills/${slug}`);
}

export async function importClawhubSkill(
  slug: string,
  version?: string,
  createNew = false,
): Promise<Skill> {
  const res = await client.post<Skill>("/skills/clawhub/import", {
    slug,
    version,
    create_new: createNew,
  });
  return res.data;
}

export async function searchClawhub(
  q: string,
  limit = 20,
): Promise<ClawhubSearchResponse> {
  const res = await client.get<ClawhubSearchResponse>("/skills/clawhub/search", {
    params: { q, limit },
  });
  return res.data;
}

export async function listInstanceSkills(id: number): Promise<InstanceSkill[]> {
  const { data } = await client.get<InstanceSkill[]>(`/instances/${id}/skills`);
  return data;
}

export async function listInstanceSkillFiles(id: number, slug: string): Promise<SkillFileEntry[]> {
  const { data } = await client.get<SkillFileEntry[]>(`/instances/${id}/skills/${slug}/files`);
  return data;
}

export async function getInstanceSkillFile(id: number, slug: string, path: string): Promise<SkillFileContent> {
  const { data } = await client.get<SkillFileContent>(`/instances/${id}/skills/${slug}/files/${encodeSkillPath(path)}`);
  return data;
}

export async function saveInstanceSkillFile(id: number, slug: string, path: string, content: string): Promise<void> {
  await client.put(`/instances/${id}/skills/${slug}/files/${encodeSkillPath(path)}`, { content });
}

function encodeSkillPath(path: string): string {
  return path
    .split("/")
    .map((segment) => encodeURIComponent(segment))
    .join("/");
}

export async function listSkillFiles(slug: string): Promise<SkillFileEntry[]> {
  const res = await client.get<SkillFileEntry[]>(`/skills/${slug}/files`);
  return res.data;
}

export async function getSkillFile(slug: string, path: string): Promise<SkillFileContent> {
  const res = await client.get<SkillFileContent>(`/skills/${slug}/files/${encodeSkillPath(path)}`);
  return res.data;
}

export async function saveSkillFile(slug: string, path: string, content: string): Promise<void> {
  await client.put(`/skills/${slug}/files/${encodeSkillPath(path)}`, { content });
}

export async function deploySkill(
  slug: string,
  instanceIds: number[],
  source: "library" | "clawhub",
  version?: string,
): Promise<DeployResponse> {
  const res = await client.post<DeployResponse>(`/skills/${slug}/deploy`, {
    instance_ids: instanceIds,
    source,
    version,
  });
  return res.data;
}

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
