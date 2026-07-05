import { useRef, useState } from "react";
import { AlertTriangle, Upload, X, Plus, Trash2, FolderOpen } from "lucide-react";
import { useUploadSkill, useCreateSkill, useImportGitSkill } from "@common/hooks/useSkills";
import MonacoConfigEditor from "@common/components/MonacoConfigEditor";

interface Props {
  onClose: () => void;
  onUploaded: () => void;
}

type Tab = "wizard" | "git" | "zip";

export default function UploadSkillModal({ onClose, onUploaded }: Props) {
  const [activeTab, setActiveTab] = useState<Tab>("wizard");

  // Zip Upload state
  const [dragging, setDragging] = useState(false);
  const [selectedFile, setSelectedFile] = useState<File | null>(null);
  const [conflictSlug, setConflictSlug] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const { mutate: upload, isPending: uploadPending } = useUploadSkill();

  // Git Import state
  const [gitUrl, setGitUrl] = useState("");
  const [gitBranch, setGitBranch] = useState("");
  const [gitError, setGitError] = useState<string | null>(null);
  const { mutate: importGit, isPending: gitImportPending } = useImportGitSkill();

  // Visual Wizard state
  const [wizardName, setWizardName] = useState("");
  const [wizardSlug, setWizardSlug] = useState("");
  const [wizardSummary, setWizardSummary] = useState("");
  const [wizardEnvVars, setWizardEnvVars] = useState("");
  const [enableMcp, setEnableMcp] = useState(false);
  const [mcpName, setMcpName] = useState("");
  const [mcpTransport, setMcpTransport] = useState<"stdio" | "sse">("stdio");
  const [mcpCommand, setMcpCommand] = useState("");
  const [mcpArgs, setMcpArgs] = useState("");
  const [mcpImage, setMcpImage] = useState("");
  const [mcpPort, setMcpPort] = useState("8080");

  const [files, setFiles] = useState<Record<string, string>>({
    "main.py": "# Write your custom MCP skill script here\nprint('hello world')",
  });
  const [selectedFileName, setSelectedFileName] = useState("main.py");
  const [newFileName, setNewFileName] = useState("");
  const { mutate: createSkill, isPending: createPending } = useCreateSkill();

  // Handle Zip file select
  const handleZipFile = (file: File) => {
    if (!file.name.endsWith(".zip")) return;
    setSelectedFile(file);
    setConflictSlug(null);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragging(false);
    const file = e.dataTransfer.files[0];
    if (file) handleZipFile(file);
  };

  const doZipUpload = (overwrite: boolean) => {
    if (!selectedFile || uploadPending) return;
    upload(
      { file: selectedFile, overwrite },
      {
        onSuccess: () => {
          onUploaded();
          onClose();
        },
        onError: (error) => {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          if ((error as any)?.response?.status === 409) {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const detail: string = (error as any)?.response?.data?.detail ?? "";
            const match = detail.match(/Skill '(.+)' already exists/);
            setConflictSlug(match ? match[1] : selectedFile.name.replace(".zip", ""));
          }
        },
      },
    );
  };

  // Handle Git Import submit
  const handleGitSubmit = () => {
    if (!gitUrl || gitImportPending) return;
    setGitError(null);
    importGit(
      { git_url: gitUrl, git_branch: gitBranch || undefined },
      {
        onSuccess: () => {
          onUploaded();
          onClose();
        },
        onError: (err: any) => {
          const errMsg = err?.response?.data?.error || err?.message || "Failed to import skill from Git";
          setGitError(errMsg);
        },
      }
    );
  };

  // Handle Wizard Submit
  const handleWizardSubmit = () => {
    if (!wizardName || !wizardSlug || createPending) return;

    const requiredEnvVarsArray = wizardEnvVars
      .split(",")
      .map((v) => v.trim())
      .filter(Boolean);

    let mcpPayload: any = undefined;
    if (enableMcp) {
      mcpPayload = {
        name: mcpName || wizardSlug || "mcp-server",
        transport: mcpTransport,
      };
      if (mcpTransport === "stdio") {
        mcpPayload.local = {
          command: mcpCommand,
          args: mcpArgs ? mcpArgs.split(",").map((a) => a.trim()).filter(Boolean) : [],
        };
      } else {
        mcpPayload.docker = {
          image: mcpImage,
          port: parseInt(mcpPort, 10) || 8080,
        };
      }
    }

    createSkill(
      {
        name: wizardName,
        slug: wizardSlug,
        summary: wizardSummary,
        required_env_vars: requiredEnvVarsArray,
        mcp: mcpPayload,
        files,
      },
      {
        onSuccess: () => {
          onUploaded();
          onClose();
        },
      }
    );
  };

  // Custom Files Helper
  const handleAddFile = () => {
    if (!newFileName.trim()) return;
    const name = newFileName.trim();
    if (files[name] !== undefined) return;
    setFiles((prev) => ({ ...prev, [name]: "" }));
    setSelectedFileName(name);
    setNewFileName("");
  };

  const handleRemoveFile = (name: string) => {
    if (Object.keys(files).length <= 1) return;
    const nextFiles = { ...files };
    delete nextFiles[name];
    setFiles(nextFiles);
    if (selectedFileName === name) {
      setSelectedFileName(Object.keys(nextFiles)[0]);
    }
  };

  const getLanguageFromFilename = (name: string) => {
    if (name.endsWith(".py")) return "python";
    if (name.endsWith(".js") || name.endsWith(".mjs")) return "javascript";
    if (name.endsWith(".ts")) return "typescript";
    if (name.endsWith(".json")) return "json";
    if (name.endsWith(".sh")) return "shell";
    return "text";
  };

  const isPending = uploadPending || gitImportPending || createPending;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <div
        className={`bg-white rounded-xl shadow-xl w-full mx-4 flex flex-col transition-all duration-300 ${
          activeTab === "wizard" ? "max-w-4xl h-[85vh]" : "max-w-md"
        }`}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-200 shrink-0">
          <h2 className="text-base font-semibold text-gray-900">Add Skill</h2>
          <button onClick={onClose} className="text-gray-400 hover:text-gray-600">
            <X size={18} />
          </button>
        </div>

        {/* Tab selection */}
        <div className="flex border-b border-gray-200 shrink-0">
          <button
            onClick={() => !isPending && setActiveTab("wizard")}
            className={`flex-1 py-2.5 text-sm font-medium text-center border-b-2 transition-colors ${
              activeTab === "wizard"
                ? "border-blue-500 text-blue-600"
                : "border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300"
            }`}
            disabled={isPending}
          >
            Visual Wizard
          </button>
          <button
            onClick={() => !isPending && setActiveTab("git")}
            className={`flex-1 py-2.5 text-sm font-medium text-center border-b-2 transition-colors ${
              activeTab === "git"
                ? "border-blue-500 text-blue-600"
                : "border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300"
            }`}
            disabled={isPending}
          >
            Git Import
          </button>
          <button
            onClick={() => !isPending && setActiveTab("zip")}
            className={`flex-1 py-2.5 text-sm font-medium text-center border-b-2 transition-colors ${
              activeTab === "zip"
                ? "border-blue-500 text-blue-600"
                : "border-transparent text-gray-500 hover:text-gray-700 hover:border-gray-300"
            }`}
            disabled={isPending}
          >
            Upload ZIP
          </button>
        </div>

        {/* Tab Content */}
        <div className="flex-1 overflow-y-auto min-h-0">
          {/* 1. Visual Wizard */}
          {activeTab === "wizard" && (
            <div className="flex h-full min-h-0">
              {/* Form Config Left Side */}
              <div className="w-1/2 p-6 overflow-y-auto border-r border-gray-200 flex flex-col gap-4">
                <div>
                  <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                    Skill Name *
                  </label>
                  <input
                    type="text"
                    required
                    value={wizardName}
                    onChange={(e) => setWizardName(e.target.value)}
                    placeholder="e.g. My Custom Skill"
                    className="w-full px-3 py-2 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                  />
                </div>

                <div>
                  <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                    Skill Slug *
                  </label>
                  <input
                    type="text"
                    required
                    value={wizardSlug}
                    onChange={(e) => setWizardSlug(e.target.value)}
                    placeholder="e.g. my-custom-skill"
                    className="w-full px-3 py-2 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                  />
                </div>

                <div>
                  <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                    Summary / Description
                  </label>
                  <textarea
                    rows={2}
                    value={wizardSummary}
                    onChange={(e) => setWizardSummary(e.target.value)}
                    placeholder="Describe what this skill does."
                    className="w-full px-3 py-2 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                  />
                </div>

                <div>
                  <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                    Required Env Vars (comma-separated)
                  </label>
                  <input
                    type="text"
                    value={wizardEnvVars}
                    onChange={(e) => setWizardEnvVars(e.target.value)}
                    placeholder="API_KEY, SECRET_URL"
                    className="w-full px-3 py-2 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                  />
                </div>

                {/* MCP configuration */}
                <div className="border border-gray-200 rounded-lg p-4 bg-gray-50 flex flex-col gap-3">
                  <label className="flex items-center gap-2 font-medium text-sm text-gray-800 cursor-pointer">
                    <input
                      type="checkbox"
                      checked={enableMcp}
                      onChange={(e) => setEnableMcp(e.target.checked)}
                      className="rounded text-blue-600 focus:ring-blue-500"
                    />
                    Enable MCP Configuration
                  </label>

                  {enableMcp && (
                    <div className="flex flex-col gap-3 pt-2 border-t border-gray-200">
                      <div>
                        <label className="block text-xs font-semibold text-gray-600 mb-1">
                          MCP Server Name
                        </label>
                        <input
                          type="text"
                          value={mcpName}
                          onChange={(e) => setMcpName(e.target.value)}
                          placeholder={wizardSlug || "mcp-server"}
                          className="w-full px-3 py-1.5 text-sm bg-white border border-gray-300 rounded-lg focus:outline-none"
                        />
                      </div>

                      <div>
                        <label className="block text-xs font-semibold text-gray-600 mb-1">
                          Transport Type
                        </label>
                        <div className="flex gap-4">
                          <label className="flex items-center gap-1.5 text-sm text-gray-700 cursor-pointer">
                            <input
                              type="radio"
                              name="transport"
                              checked={mcpTransport === "stdio"}
                              onChange={() => setMcpTransport("stdio")}
                            />
                            stdio (Local command)
                          </label>
                          <label className="flex items-center gap-1.5 text-sm text-gray-700 cursor-pointer">
                            <input
                              type="radio"
                              name="transport"
                              checked={mcpTransport === "sse"}
                              onChange={() => setMcpTransport("sse")}
                            />
                            sse (Docker sidecar)
                          </label>
                        </div>
                      </div>

                      {mcpTransport === "stdio" ? (
                        <>
                          <div>
                            <label className="block text-xs font-semibold text-gray-600 mb-1">
                              Command *
                            </label>
                            <input
                              type="text"
                              value={mcpCommand}
                              onChange={(e) => setMcpCommand(e.target.value)}
                              placeholder="python3"
                              className="w-full px-3 py-1.5 text-sm bg-white border border-gray-300 rounded-lg focus:outline-none"
                            />
                          </div>
                          <div>
                            <label className="block text-xs font-semibold text-gray-600 mb-1">
                              Arguments (comma-separated)
                            </label>
                            <input
                              type="text"
                              value={mcpArgs}
                              onChange={(e) => setMcpArgs(e.target.value)}
                              placeholder="main.py, --arg1"
                              className="w-full px-3 py-1.5 text-sm bg-white border border-gray-300 rounded-lg focus:outline-none"
                            />
                          </div>
                        </>
                      ) : (
                        <>
                          <div>
                            <label className="block text-xs font-semibold text-gray-600 mb-1">
                              Docker Image *
                            </label>
                            <input
                              type="text"
                              value={mcpImage}
                              onChange={(e) => setMcpImage(e.target.value)}
                              placeholder="e.g. node-mcp-server:latest"
                              className="w-full px-3 py-1.5 text-sm bg-white border border-gray-300 rounded-lg focus:outline-none"
                            />
                          </div>
                          <div>
                            <label className="block text-xs font-semibold text-gray-600 mb-1">
                              Container Port
                            </label>
                            <input
                              type="number"
                              value={mcpPort}
                              onChange={(e) => setMcpPort(e.target.value)}
                              className="w-full px-3 py-1.5 text-sm bg-white border border-gray-300 rounded-lg focus:outline-none"
                            />
                          </div>
                        </>
                      )}
                    </div>
                  )}
                </div>
              </div>

              {/* Code Files Sidebar & Editor Right Side */}
              <div className="w-1/2 flex flex-col min-h-0 bg-gray-50">
                {/* File Manager bar */}
                <div className="flex border-b border-gray-200 shrink-0">
                  <div className="w-1/3 border-r border-gray-200 p-3 flex flex-col gap-2 bg-white">
                    <span className="text-xs font-bold text-gray-500 uppercase tracking-wider">
                      Files
                    </span>
                    <div className="flex-1 overflow-y-auto flex flex-col gap-1 min-h-0">
                      {Object.keys(files).map((name) => (
                        <div
                          key={name}
                          onClick={() => setSelectedFileName(name)}
                          className={`flex items-center justify-between px-2 py-1.5 rounded text-xs cursor-pointer select-none truncate ${
                            selectedFileName === name
                              ? "bg-blue-50 text-blue-700 font-semibold"
                              : "text-gray-700 hover:bg-gray-100"
                          }`}
                        >
                          <span className="truncate flex items-center gap-1">
                            <FolderOpen size={12} className="shrink-0" />
                            {name}
                          </span>
                          {Object.keys(files).length > 1 && (
                            <button
                              onClick={(e) => {
                                e.stopPropagation();
                                handleRemoveFile(name);
                              }}
                              className="text-gray-400 hover:text-red-500 transition-colors shrink-0"
                            >
                              <Trash2 size={12} />
                            </button>
                          )}
                        </div>
                      ))}
                    </div>
                    {/* Add File Section */}
                    <div className="mt-2 pt-2 border-t border-gray-100 flex gap-1">
                      <input
                        type="text"
                        value={newFileName}
                        onChange={(e) => setNewFileName(e.target.value)}
                        placeholder="new_file.py"
                        className="flex-1 px-1.5 py-1 text-xs border border-gray-300 rounded focus:outline-none"
                      />
                      <button
                        onClick={handleAddFile}
                        className="p-1 bg-blue-600 hover:bg-blue-700 text-white rounded"
                        title="Add File"
                      >
                        <Plus size={14} />
                      </button>
                    </div>
                  </div>

                  {/* Monaco Editor Panel */}
                  <div className="w-2/3 flex flex-col min-h-0">
                    <div className="p-3 bg-white text-xs text-gray-500 font-mono border-b border-gray-200 shrink-0">
                      Editing: {selectedFileName}
                    </div>
                    <div className="flex-1 min-h-0">
                      <MonacoConfigEditor
                        value={files[selectedFileName] ?? ""}
                        onChange={(val) => {
                          if (val !== undefined) {
                            setFiles((prev) => ({ ...prev, [selectedFileName]: val }));
                          }
                        }}
                        language={getLanguageFromFilename(selectedFileName)}
                        height="100%"
                      />
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* 2. Git Import */}
          {activeTab === "git" && (
            <div className="px-6 py-6 flex flex-col gap-4">
              {gitError && (
                <div className="rounded-lg border border-red-200 bg-red-50 p-4 flex gap-3">
                  <AlertTriangle size={18} className="text-red-500 mt-0.5 shrink-0" />
                  <p className="text-sm text-red-800">{gitError}</p>
                </div>
              )}

              <div>
                <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                  Git HTTPS URL *
                </label>
                <input
                  type="url"
                  required
                  value={gitUrl}
                  onChange={(e) => setGitUrl(e.target.value)}
                  placeholder="https://github.com/username/repository.git"
                  className="w-full px-3 py-2.5 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                />
              </div>

              <div>
                <label className="block text-xs font-semibold text-gray-600 uppercase tracking-wider mb-1">
                  Git Branch (optional)
                </label>
                <input
                  type="text"
                  value={gitBranch}
                  onChange={(e) => setGitBranch(e.target.value)}
                  placeholder="e.g. main"
                  className="w-full px-3 py-2.5 text-sm border border-gray-300 rounded-lg focus:outline-none focus:ring-1 focus:ring-blue-500"
                />
              </div>
            </div>
          )}

          {/* 3. ZIP Upload */}
          {activeTab === "zip" && (
            <div className="px-6 py-6 flex flex-col gap-4">
              {conflictSlug ? (
                <div className="rounded-lg border border-amber-200 bg-amber-50 p-4 flex gap-3">
                  <AlertTriangle size={18} className="text-amber-500 mt-0.5 shrink-0" />
                  <p className="text-sm text-amber-800">
                    A skill named <strong>{conflictSlug}</strong> already exists. Overwrite it?
                  </p>
                </div>
              ) : (
                <div
                  onDragOver={(e) => {
                    e.preventDefault();
                    setDragging(true);
                  }}
                  onDragLeave={() => setDragging(false)}
                  onDrop={handleDrop}
                  onClick={() => inputRef.current?.click()}
                  className={`border-2 border-dashed rounded-lg p-8 text-center cursor-pointer transition-colors ${
                    dragging
                      ? "border-blue-400 bg-blue-50"
                      : "border-gray-300 hover:border-gray-400"
                  }`}
                >
                  <Upload size={24} className="mx-auto mb-3 text-gray-400" />
                  {selectedFile ? (
                    <p className="text-sm font-medium text-gray-800">{selectedFile.name}</p>
                  ) : (
                    <>
                      <p className="text-sm font-medium text-gray-700">
                        Drop a .zip file here or click to browse
                      </p>
                      <p className="text-xs text-gray-400 mt-1">
                        Zip must contain a SKILL.md with valid frontmatter
                      </p>
                    </>
                  )}
                  <input
                    ref={inputRef}
                    type="file"
                    accept=".zip"
                    className="hidden"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file) handleZipFile(file);
                    }}
                  />
                </div>
              )}
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-gray-200 flex items-center justify-end gap-3 shrink-0">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium text-gray-700 hover:text-gray-900 transition-colors"
            disabled={isPending}
          >
            Cancel
          </button>

          {activeTab === "wizard" && (
            <button
              onClick={handleWizardSubmit}
              disabled={!wizardName || !wizardSlug || createPending}
              className="px-4 py-2 text-sm font-medium text-white bg-blue-600 rounded-lg hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {createPending ? "Creating…" : "Create Skill"}
            </button>
          )}

          {activeTab === "git" && (
            <button
              onClick={handleGitSubmit}
              disabled={!gitUrl || gitImportPending}
              className="px-4 py-2 text-sm font-medium text-white bg-blue-600 rounded-lg hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
            >
              {gitImportPending ? "Importing…" : "Import Skill"}
            </button>
          )}

          {activeTab === "zip" &&
            (conflictSlug ? (
              <button
                onClick={() => doZipUpload(true)}
                disabled={uploadPending}
                className="px-4 py-2 text-sm font-medium text-white bg-amber-500 rounded-lg hover:bg-amber-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {uploadPending ? "Overwriting…" : "Overwrite"}
              </button>
            ) : (
              <button
                onClick={() => doZipUpload(false)}
                disabled={!selectedFile || uploadPending}
                className="px-4 py-2 text-sm font-medium text-white bg-blue-600 rounded-lg hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {uploadPending ? "Uploading…" : "Upload"}
              </button>
            ))}
        </div>
      </div>
    </div>
  );
}
