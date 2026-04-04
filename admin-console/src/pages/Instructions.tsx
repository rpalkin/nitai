import { useState, useEffect, useRef } from "react";
import { instructionClient, repoClient, providerClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";
import type { ReviewInstruction } from "@gen/api/v1/instruction_pb";
import type { Repository } from "@gen/api/v1/repo_pb";

const LANGUAGE_PRESETS: Record<string, string[]> = {
  Go: ["*.go"],
  TypeScript: ["*.ts", "*.tsx"],
  JavaScript: ["*.js", "*.jsx"],
  Python: ["*.py"],
  Rust: ["*.rs"],
  Java: ["*.java"],
  CSS: ["*.css", "*.scss"],
  SQL: ["*.sql"],
  Proto: ["*.proto"],
};

function formatDate(timestamp: { seconds: bigint; nanos: number } | undefined): string {
  if (!timestamp) return "N/A";
  const date = new Date(Number(timestamp.seconds) * 1000);
  return date.toLocaleDateString();
}

interface InstructionFormData {
  name: string;
  content: string;
  repoFilter: string[];
  filePatternFilter: string[];
  enabled: boolean;
}

const emptyForm: InstructionFormData = {
  name: "",
  content: "",
  repoFilter: [],
  filePatternFilter: [],
  enabled: true,
};

export function Instructions() {
  const [instructions, setInstructions] = useState<ReviewInstruction[]>([]);
  const [repos, setRepos] = useState<Repository[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [formData, setFormData] = useState<InstructionFormData>(emptyForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [showRepoDropdown, setShowRepoDropdown] = useState(false);
  const [filePatternInput, setFilePatternInput] = useState("");
  const repoDropdownRef = useRef<HTMLDivElement>(null);

  const fetchInstructions = async () => {
    try {
      setIsLoading(true);
      setError(null);
      const response = await instructionClient.listInstructions({});
      setInstructions(response.instructions);
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to load instructions");
    } finally {
      setIsLoading(false);
    }
  };

  const fetchRepos = async () => {
    try {
      const providersResponse = await providerClient.listProviders({});
      const allRepos: Repository[] = [];
      for (const provider of providersResponse.providers) {
        const reposResponse = await repoClient.listRepos({ providerId: provider.id });
        allRepos.push(...reposResponse.repositories);
      }
      setRepos(allRepos);
    } catch (err) {
      console.error("Failed to load repos:", err);
    }
  };

  useEffect(() => {
    fetchInstructions();
    fetchRepos();
  }, []);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (repoDropdownRef.current && !repoDropdownRef.current.contains(event.target as Node)) {
        setShowRepoDropdown(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);

    if (!formData.name.trim() || !formData.content.trim()) {
      setFormError("Name and content are required");
      return;
    }

    setIsSubmitting(true);

    try {
      await instructionClient.createInstruction({
        name: formData.name,
        content: formData.content,
        repoFilter: formData.repoFilter,
        filePatternFilter: formData.filePatternFilter,
      });
      setShowCreateForm(false);
      setFormData(emptyForm);
      fetchInstructions();
    } catch (err) {
      setFormError(err instanceof ConnectError ? err.message : "Failed to create instruction");
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleUpdate = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);

    if (!formData.name.trim() || !formData.content.trim()) {
      setFormError("Name and content are required");
      return;
    }

    if (!editingId) return;

    setIsSubmitting(true);

    try {
      await instructionClient.updateInstruction({
        id: editingId,
        name: formData.name,
        content: formData.content,
        repoFilter: formData.repoFilter,
        filePatternFilter: formData.filePatternFilter,
        enabled: formData.enabled,
      });
      setEditingId(null);
      setFormData(emptyForm);
      fetchInstructions();
    } catch (err) {
      setFormError(err instanceof ConnectError ? err.message : "Failed to update instruction");
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleToggleEnabled = async (instruction: ReviewInstruction) => {
    try {
      await instructionClient.updateInstruction({
        id: instruction.id,
        name: instruction.name,
        content: instruction.content,
        repoFilter: [...instruction.repoFilter],
        filePatternFilter: [...instruction.filePatternFilter],
        enabled: !instruction.enabled,
      });
      fetchInstructions();
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to update instruction");
    }
  };

  const handleDelete = async () => {
    if (!deletingId) return;

    try {
      await instructionClient.deleteInstruction({ id: deletingId });
      setDeletingId(null);
      fetchInstructions();
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to delete instruction");
    }
  };

  const handleStartEdit = (instruction: ReviewInstruction) => {
    setFormData({
      name: instruction.name,
      content: instruction.content,
      repoFilter: [...instruction.repoFilter],
      filePatternFilter: [...instruction.filePatternFilter],
      enabled: instruction.enabled,
    });
    setEditingId(instruction.id);
  };

  const handleAddFilePattern = (pattern: string) => {
    const trimmed = pattern.trim();
    if (trimmed && !formData.filePatternFilter.includes(trimmed)) {
      setFormData({
        ...formData,
        filePatternFilter: [...formData.filePatternFilter, trimmed],
      });
    }
    setFilePatternInput("");
  };

  const handleRemoveFilePattern = (pattern: string) => {
    setFormData({
      ...formData,
      filePatternFilter: formData.filePatternFilter.filter((p) => p !== pattern),
    });
  };

  const handleToggleRepo = (repoId: string) => {
    const newFilter = formData.repoFilter.includes(repoId)
      ? formData.repoFilter.filter((id) => id !== repoId)
      : [...formData.repoFilter, repoId];
    setFormData({ ...formData, repoFilter: newFilter });
  };

  const handleToggleLanguagePreset = ( language: string) => {
    const patterns = LANGUAGE_PRESETS[language];
    const hasAll = patterns.every((p) => formData.filePatternFilter.includes(p));

    if (hasAll) {
      setFormData({
        ...formData,
        filePatternFilter: formData.filePatternFilter.filter((p) => !patterns.includes(p)),
      });
    } else {
      const newPatterns = patterns.filter((p) => !formData.filePatternFilter.includes(p));
      setFormData({
        ...formData,
        filePatternFilter: [...formData.filePatternFilter, ...newPatterns],
      });
    }
  };

  const isLanguagePresetActive = (language: string) => {
    const patterns = LANGUAGE_PRESETS[language];
    return patterns.every((p) => formData.filePatternFilter.includes(p));
  };

  const instructionToDelete = instructions.find((i) => i.id === deletingId);

  const renderForm = (isEdit: boolean) => (
    <div className="bg-surface-800 border border-border rounded-lg p-6">
      <h2 className="text-lg font-semibold text-text-primary mb-4">
        {isEdit ? "Edit Instruction" : "Add Instruction"}
      </h2>
      <form onSubmit={isEdit ? handleUpdate : handleCreate} className="space-y-4">
        <div>
          <label htmlFor="name" className="block text-sm font-medium text-text-secondary mb-2">
            Name
          </label>
          <input
            id="name"
            type="text"
            value={formData.name}
            onChange={(e) => setFormData({ ...formData, name: e.target.value })}
            disabled={isSubmitting}
            className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
          />
        </div>

        <div>
          <label htmlFor="content" className="block text-sm font-medium text-text-secondary mb-2">
            Content
          </label>
          <textarea
            id="content"
            value={formData.content}
            onChange={(e) => setFormData({ ...formData, content: e.target.value })}
            disabled={isSubmitting}
            rows={6}
            placeholder="Enter review instruction for the AI..."
            className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors resize-none"
          />
        </div>

        <div>
          <label className="block text-sm font-medium text-text-secondary mb-2">
            Repository Filter
          </label>
          <div ref={repoDropdownRef} className="relative">
            <button
              type="button"
              onClick={() => setShowRepoDropdown(!showRepoDropdown)}
              disabled={isSubmitting}
              className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary text-left flex items-center justify-between focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
            >
              <span>
                {formData.repoFilter.length === 0
                  ? "All repositories"
                  : `${formData.repoFilter.length} repositories selected`}
              </span>
              <svg
                className={`w-5 h-5 text-text-muted transition-transform ${showRepoDropdown ? "rotate-180" : ""}`}
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M19 9l-7 7-7-7" />
              </svg>
            </button>
            {showRepoDropdown && (
              <div className="absolute z-10 mt-1 w-full bg-surface-700 border border-border rounded-lg shadow-lg max-h-60 overflow-auto">
                {repos.length === 0 ? (
                  <div className="px-4 py-3 text-text-muted">No repositories available</div>
                ) : (
                  repos.map((repo) => (
                    <label
                      key={repo.id}
                      className="flex items-center px-4 py-3 hover:bg-surface-600 cursor-pointer"
                    >
                      <input
                        type="checkbox"
                        checked={formData.repoFilter.includes(repo.id)}
                        onChange={() => handleToggleRepo(repo.id)}
                        className="mr-3 rounded border-border text-accent focus:ring-accent"
                      />
                      <span className="text-text-primary">{repo.fullPath}</span>
                    </label>
                  ))
                )}
              </div>
            )}
          </div>
          <p className="text-text-muted text-sm mt-1">
            Leave empty to apply to all repositories
          </p>
        </div>

        <div>
          <label className="block text-sm font-medium text-text-secondary mb-2">
            File Pattern Filter
          </label>
          <div className="space-y-2">
            <div className="flex flex-wrap gap-2">
              {formData.filePatternFilter.map((pattern) => (
                <span
                  key={pattern}
                  className="inline-flex items-center gap-1 px-3 py-1 bg-surface-700 border border-border rounded-full text-text-primary text-sm"
                >
                  {pattern}
                  <button
                    type="button"
                    onClick={() => handleRemoveFilePattern(pattern)}
                    aria-label={`Remove ${pattern}`}
                    className="text-text-muted hover:text-text-primary"
                  >
                    <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                </span>
              ))}
            </div>
            <input
              type="text"
              value={filePatternInput}
              onChange={(e) => setFilePatternInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  handleAddFilePattern(filePatternInput);
                }
              }}
              disabled={isSubmitting}
              placeholder="e.g., *.go, src/*.ts"
              className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
            />
            <div className="flex flex-wrap gap-1">
              {Object.keys(LANGUAGE_PRESETS).map((language) => (
                <button
                  key={language}
                  type="button"
                  onClick={() => handleToggleLanguagePreset(language)}
                  disabled={isSubmitting}
                  className={`px-3 py-1 text-sm rounded transition-colors ${
                    isLanguagePresetActive(language)
                      ? "bg-accent text-white"
                      : "bg-surface-700 text-text-secondary hover:bg-surface-600"
                  }`}
                >
                  {language}
                </button>
              ))}
            </div>
          </div>
        </div>

        {isEdit && (
          <div className="flex items-center gap-3">
            <label className="text-sm font-medium text-text-secondary">Enabled</label>
            <button
              type="button"
              onClick={() => setFormData({ ...formData, enabled: !formData.enabled })}
              disabled={isSubmitting}
              aria-label="Toggle enabled"
              className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                formData.enabled ? "bg-accent" : "bg-surface-600"
              }`}
            >
              <span
                className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                  formData.enabled ? "translate-x-6" : "translate-x-1"
                }`}
              />
            </button>
          </div>
        )}

        {formError && <div className="text-error text-sm">{formError}</div>}

        <div className="flex gap-3">
          <button
            type="submit"
            disabled={isSubmitting}
            className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {isSubmitting ? "Saving..." : "Save"}
          </button>
          <button
            type="button"
            onClick={() => {
              if (isEdit) {
                setEditingId(null);
              } else {
                setShowCreateForm(false);
              }
              setFormData(emptyForm);
              setFormError(null);
            }}
            disabled={isSubmitting}
            className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 disabled:opacity-50 transition-colors"
          >
            Cancel
          </button>
        </div>
      </form>
    </div>
  );

  const getScopeDisplay = (instruction: ReviewInstruction) => {
    const repoCount = instruction.repoFilter.length;
    const patternCount = instruction.filePatternFilter.length;

    if (repoCount === 0 && patternCount === 0) {
      return "All repos, all files";
    }

    const parts: string[] = [];
    if (repoCount > 0) {
      parts.push(`${repoCount} repo${repoCount === 1 ? "" : "s"}`);
    }
    if (patternCount > 0) {
      parts.push(`${patternCount} pattern${patternCount === 1 ? "" : "s"}`);
    }

    return parts.join(", ");
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text-primary">Instructions</h1>
        {!showCreateForm && !editingId && (
          <button
            onClick={() => {
              setShowCreateForm(true);
              setFormData(emptyForm);
              setFormError(null);
            }}
            className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover transition-colors"
          >
            Add instruction
          </button>
        )}
      </div>

      {showCreateForm && renderForm(false)}

      {isLoading && (
        <div className="text-center py-8">
          <div className="text-text-secondary">Loading...</div>
        </div>
      )}

      {error && (
        <div className="bg-surface-800 border border-error rounded-lg p-4">
          <p className="text-error">{error}</p>
        </div>
      )}

      {!isLoading && !error && instructions.length === 0 && !showCreateForm && !editingId && (
        <div className="text-center py-8">
          <p className="text-text-secondary mb-4">No instructions yet</p>
          <button
            onClick={() => {
              setShowCreateForm(true);
              setFormData(emptyForm);
              setFormError(null);
            }}
            className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover transition-colors"
          >
            Add instruction
          </button>
        </div>
      )}

      {!isLoading && !error && instructions.length > 0 && !showCreateForm && !editingId && (
        <div className="bg-surface-800 border border-border rounded-lg overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-border">
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Name</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Status</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Scope</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Created</th>
                <th className="text-right px-4 py-3 text-text-secondary font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {instructions.map((instruction) => (
                <tr key={instruction.id} className="hover:bg-surface-700 transition-colors">
                  <td className="px-4 py-3 text-text-primary">{instruction.name}</td>
                  <td className="px-4 py-3">
                    <button
                      onClick={() => handleToggleEnabled(instruction)}
                      aria-label="Toggle"
                      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                        instruction.enabled ? "bg-accent" : "bg-surface-600"
                      }`}
                    >
                      <span
                        className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                          instruction.enabled ? "translate-x-6" : "translate-x-1"
                        }`}
                      />
                    </button>
                  </td>
                  <td className="px-4 py-3 text-text-muted text-sm">
                    {getScopeDisplay(instruction)}
                  </td>
                  <td className="px-4 py-3 text-text-muted text-sm">
                    {formatDate(instruction.createdAt)}
                  </td>
                  <td className="px-4 py-3 text-right space-x-2">
                    <button
                      onClick={() => handleStartEdit(instruction)}
                      aria-label={`Edit ${instruction.name}`}
                      className="text-accent hover:text-accent-hover transition-colors text-sm"
                    >
                      Edit
                    </button>
                    <button
                      onClick={() => setDeletingId(instruction.id)}
                      aria-label={`Delete ${instruction.name}`}
                      className="text-error hover:text-error/80 transition-colors text-sm"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editingId && renderForm(true)}

      {deletingId && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-surface-800 border border-border rounded-lg p-6 max-w-md">
            <h3 className="text-lg font-semibold text-text-primary mb-2">Delete instruction?</h3>
            <p className="text-text-secondary mb-4">
              This will remove "{instructionToDelete?.name}" permanently.
            </p>
            <div className="flex gap-3">
              <button
                onClick={handleDelete}
                className="py-2 px-4 bg-error text-white font-semibold rounded-lg hover:bg-error/80 transition-colors"
              >
                Delete
              </button>
              <button
                onClick={() => setDeletingId(null)}
                className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 transition-colors"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}