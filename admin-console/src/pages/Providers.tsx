import { useState, useEffect } from "react";
import { providerClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";
import type { Provider } from "@gen/api/v1/provider_pb";
import { ProviderType } from "@gen/api/v1/provider_pb";

const PROVIDER_TYPE_LABELS: Record<ProviderType, string> = {
  [ProviderType.UNSPECIFIED]: "Unknown",
  [ProviderType.GITLAB_SELF_HOSTED]: "GitLab Self-Hosted",
  [ProviderType.GITLAB_CLOUD]: "GitLab Cloud",
  [ProviderType.GITHUB]: "GitHub",
};

function formatDate(timestamp: { seconds: bigint; nanos: number } | undefined): string {
  if (!timestamp) return "N/A";
  const date = new Date(Number(timestamp.seconds) * 1000);
  return date.toLocaleDateString();
}

export function Providers() {
  const [providers, setProviders] = useState<Provider[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showCreateForm, setShowCreateForm] = useState(false);
  const [webhookSecret, setWebhookSecret] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const [formData, setFormData] = useState({
    name: "",
    type: ProviderType.GITLAB_CLOUD,
    baseUrl: "",
    token: "",
  });
  const [formError, setFormError] = useState<string | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  const fetchProviders = async () => {
    try {
      setIsLoading(true);
      setError(null);
      const response = await providerClient.listProviders({});
      setProviders(response.providers);
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to load providers");
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    fetchProviders();
  }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setIsSubmitting(true);

    try {
      const baseUrl =
        formData.type === ProviderType.GITLAB_CLOUD
          ? "https://gitlab.com"
          : formData.type === ProviderType.GITHUB
          ? "https://github.com"
          : formData.baseUrl;

      const response = await providerClient.createProvider({
        type: formData.type,
        name: formData.name,
        baseUrl: baseUrl,
        token: formData.token,
      });

      setWebhookSecret(response.webhookSecret);
      setShowCreateForm(false);
      setFormData({
        name: "",
        type: ProviderType.GITLAB_CLOUD,
        baseUrl: "",
        token: "",
      });
      fetchProviders();
    } catch (err) {
      setFormError(err instanceof ConnectError ? err.message : "Failed to create provider");
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleDelete = async () => {
    if (!deletingId) return;

    try {
      await providerClient.deleteProvider({ id: deletingId });
      setDeletingId(null);
      fetchProviders();
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to delete provider");
    }
  };

  const copyToClipboard = async () => {
    if (webhookSecret) {
      await navigator.clipboard.writeText(webhookSecret);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  };

  const providerToDelete = providers.find((p) => p.id === deletingId);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text-primary">Providers</h1>
        <button
          onClick={() => setShowCreateForm(true)}
          className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover transition-colors"
        >
          Add provider
        </button>
      </div>

      {webhookSecret && (
        <div className="bg-surface-800 border border-accent rounded-lg p-4">
          <div className="flex items-start justify-between">
            <div>
              <h3 className="text-text-primary font-semibold">Webhook Secret</h3>
              <p className="text-text-secondary text-sm mt-1">
                Copy this secret now — it won't be shown again.
              </p>
              <div className="flex items-center gap-2 mt-2">
                <code className="bg-surface-900 px-3 py-1.5 rounded text-text-primary font-mono text-sm">
                  {webhookSecret}
                </code>
                <button
                  onClick={copyToClipboard}
                  aria-label="Copy"
                  className="px-3 py-1.5 bg-surface-700 text-text-primary rounded hover:bg-surface-600 transition-colors text-sm"
                >
                  {copied ? "Copied!" : "Copy"}
                </button>
              </div>
            </div>
            <button
              onClick={() => setWebhookSecret(null)}
              aria-label="Dismiss"
              className="text-text-muted hover:text-text-primary transition-colors"
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
              </svg>
            </button>
          </div>
        </div>
      )}

      {showCreateForm && (
        <div className="bg-surface-800 border border-border rounded-lg p-6">
          <h2 className="text-lg font-semibold text-text-primary mb-4">Add Provider</h2>
          <form onSubmit={handleCreate} className="space-y-4">
            <div>
              <label htmlFor="name" className="block text-sm font-medium text-text-secondary mb-2">
                Name
              </label>
              <input
                id="name"
                type="text"
                value={formData.name}
                onChange={(e) => setFormData({ ...formData, name: e.target.value })}
                required
                disabled={isSubmitting}
                placeholder="My GitLab Instance"
                className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
              />
            </div>

            <div>
              <label htmlFor="type" className="block text-sm font-medium text-text-secondary mb-2">
                Type
              </label>
              <select
                id="type"
                value={formData.type}
                onChange={(e) =>
                  setFormData({
                    ...formData,
                    type: Number(e.target.value) as ProviderType,
                    baseUrl:
                      Number(e.target.value) === ProviderType.GITLAB_CLOUD
                        ? "https://gitlab.com"
                        : Number(e.target.value) === ProviderType.GITHUB
                        ? "https://github.com"
                        : "",
                  })
                }
                required
                disabled={isSubmitting}
                className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
              >
                <option value={ProviderType.GITLAB_CLOUD}>GitLab Cloud</option>
                <option value={ProviderType.GITLAB_SELF_HOSTED}>GitLab Self-Hosted</option>
                <option value={ProviderType.GITHUB}>GitHub</option>
              </select>
            </div>

            {formData.type === ProviderType.GITLAB_SELF_HOSTED && (
              <div>
                <label htmlFor="baseUrl" className="block text-sm font-medium text-text-secondary mb-2">
                  Base URL
                </label>
                <input
                  id="baseUrl"
                  type="url"
                  value={formData.baseUrl}
                  onChange={(e) => setFormData({ ...formData, baseUrl: e.target.value })}
                  required
                  disabled={isSubmitting}
                  placeholder="https://gitlab.example.com"
                  className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
                />
              </div>
            )}

            <div>
              <label htmlFor="token" className="block text-sm font-medium text-text-secondary mb-2">
                Token
              </label>
              <input
                id="token"
                type="password"
                value={formData.token}
                onChange={(e) => setFormData({ ...formData, token: e.target.value })}
                required
                disabled={isSubmitting}
                placeholder="Access token"
                className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary placeholder-text-muted focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
              />
            </div>

            {formError && (
              <div className="text-error text-sm">{formError}</div>
            )}

            <div className="flex gap-3">
              <button
                type="submit"
                disabled={isSubmitting}
                className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                {isSubmitting ? "Creating..." : "Create"}
              </button>
              <button
                type="button"
                onClick={() => setShowCreateForm(false)}
                disabled={isSubmitting}
                className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 disabled:opacity-50 transition-colors"
              >
                Cancel
              </button>
            </div>
          </form>
        </div>
      )}

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

      {!isLoading && !error && providers.length === 0 && !showCreateForm && (
        <div className="text-center py-8">
          <p className="text-text-secondary mb-4">No providers configured</p>
          <button
            onClick={() => setShowCreateForm(true)}
            className="py-2 px-4 bg-accent text-white font-semibold rounded-lg hover:bg-accent-hover transition-colors"
          >
            Add provider
          </button>
        </div>
      )}

      {!isLoading && !error && providers.length > 0 && (
        <div className="bg-surface-800 border border-border rounded-lg overflow-hidden">
          <table className="w-full">
            <thead>
              <tr className="border-b border-border">
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Name</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Type</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Base URL</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Created</th>
                <th className="text-right px-4 py-3 text-text-secondary font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {providers.map((provider) => (
                <tr key={provider.id} className="hover:bg-surface-700 transition-colors">
                  <td className="px-4 py-3 text-text-primary">{provider.name}</td>
                  <td className="px-4 py-3 text-text-secondary">
                    {PROVIDER_TYPE_LABELS[provider.type]}
                  </td>
                  <td className="px-4 py-3 text-text-muted text-sm">{provider.baseUrl}</td>
                  <td className="px-4 py-3 text-text-muted text-sm">
                    {formatDate(provider.createdAt)}
                  </td>
                  <td className="px-4 py-3 text-right">
                    <button
                      onClick={() => setDeletingId(provider.id)}
                      aria-label={`Delete ${provider.name}`}
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

      {deletingId && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-surface-800 border border-border rounded-lg p-6 max-w-md">
            <h3 className="text-lg font-semibold text-text-primary mb-2">Delete provider?</h3>
            <p className="text-text-secondary mb-4">
              This will remove "{providerToDelete?.name}" and all associated repos.
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