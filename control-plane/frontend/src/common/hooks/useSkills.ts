import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  deleteSkill,
  deploySkill,
  getSkillFile,
  importClawhubSkill,
  listSkillFiles,
  listSkills,
  saveSkillFile,
  searchClawhub,
  uploadSkill,
  listInstanceSkills,
  listInstanceSkillFiles,
  getInstanceSkillFile,
  saveInstanceSkillFile,
} from "@common/api/skills";
import { errorToast, successToast } from "@common/utils/toast";

export function useSkills() {
  return useQuery({
    queryKey: ["skills"],
    queryFn: listSkills,
  });
}

export function useUploadSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ file, overwrite = false }: { file: File; overwrite?: boolean }) =>
      uploadSkill(file, overwrite),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill uploaded");
    },
    onError: (error, _vars, _ctx) => {
      // 409 conflicts are handled inline in the modal — suppress the toast
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      if ((error as any)?.response?.status === 409) return;
      errorToast("Failed to upload skill", error);
    },
  });
}

export function useImportSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      slug,
      version,
      createNew = false,
    }: {
      slug: string;
      version?: string;
      createNew?: boolean;
    }) => importClawhubSkill(slug, version, createNew),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill added to library");
    },
    onError: (error) => errorToast("Failed to import skill", error),
  });
}

export function useDeleteSkill() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (slug: string) => deleteSkill(slug),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["skills"] });
      successToast("Skill deleted");
    },
    onError: (error) => errorToast("Failed to delete skill", error),
  });
}

export function useClawhubSearch(q: string, enabled: boolean) {
  return useQuery({
    queryKey: ["clawhub-search", q],
    queryFn: () => searchClawhub(q),
    enabled: enabled && q.trim().length > 0,
    staleTime: 60_000,
  });
}

export function useInstanceSkills(id: number) {
  return useQuery({
    queryKey: ["instance-skills", id],
    queryFn: () => listInstanceSkills(id),
  });
}

export function useSkillFiles(slug: string | null, instanceId?: number) {
  return useQuery({
    queryKey: instanceId ? ["instance-skill-files", instanceId, slug] : ["skill-files", slug],
    queryFn: () => instanceId ? listInstanceSkillFiles(instanceId, slug as string) : listSkillFiles(slug as string),
    enabled: !!slug,
  });
}

export function useSkillFile(slug: string | null, path: string | null, instanceId?: number) {
  return useQuery({
    queryKey: instanceId ? ["instance-skill-file", instanceId, slug, path] : ["skill-file", slug, path],
    queryFn: () => instanceId ? getInstanceSkillFile(instanceId, slug as string, path as string) : getSkillFile(slug as string, path as string),
    enabled: !!slug && !!path,
  });
}

export function useSaveSkillFile(slug: string, instanceId?: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ path, content }: { path: string; content: string }) =>
      instanceId ? saveInstanceSkillFile(instanceId, slug, path, content) : saveSkillFile(slug, path, content),
    onSuccess: (_data, { path }) => {
      if (instanceId) {
        qc.invalidateQueries({ queryKey: ["instance-skill-files", instanceId, slug] });
        qc.invalidateQueries({ queryKey: ["instance-skill-file", instanceId, slug, path] });
        qc.invalidateQueries({ queryKey: ["instances", instanceId] });
        qc.invalidateQueries({ queryKey: ["instance-skills", instanceId] });
      } else {
        qc.invalidateQueries({ queryKey: ["skill-files", slug] });
        qc.invalidateQueries({ queryKey: ["skill-file", slug, path] });
        if (path === "SKILL.md") {
          qc.invalidateQueries({ queryKey: ["skills"] });
        }
      }
      successToast("File saved");
    },
    onError: (error) => errorToast("Failed to save file", error),
  });
}

export function useDeploySkill() {
  return useMutation({
    mutationFn: ({
      slug,
      instanceIds,
      source,
      version,
    }: {
      slug: string;
      instanceIds: number[];
      source: "library" | "clawhub";
      version?: string;
    }) => deploySkill(slug, instanceIds, source, version),
  });
}
