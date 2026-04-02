import { useState, useEffect } from "react";
import { useParams, Link } from "react-router-dom";
import { providerClient, repoClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";
import type { Repository } from "@gen/api/v1/repo_pb";
import { ProviderType } from "@gen/api/v1/provider_pb";
import type { Provider } from "@gen/api/v1/provider_pb";

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

export function Provider() {
  const { providerId } = useParams<{ providerId: string }>();
  const [provider, setProvider] = useState<Provider | null>(null);
  const [repos, setRepos] = useState<Repository[]>([]);
  const [isLoadingProvider, setIsLoadingProvider] = useState(true);
  const [isLoadingRepos, setIsLoadingRepos] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [disablingId, setDisablingId] = useState<string | null>(null);

  const fetchProvider = async () => {
    if (!providerId) return;

    try {
      setIsLoadingProvider(true);
      setError(null);
      const response = await providerClient.listProviders({});
      const found = response.providers.find((p) => p.id === providerId);
      if (!found) {
        setError("Provider not found");
        setProvider(null);
      } else {
        setProvider(found);
      }
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to load provider");
    } finally {
      setIsLoadingProvider(false);
    }
  };

  const fetchRepos = async () => {
    if (!providerId) return;

    try {
      setIsLoadingRepos(true);
      const response = await repoClient.listRepos({ providerId });
      setRepos(response.repositories);
    } catch (err) {
      console.error("Failed to load repos:", err);
    } finally {
      setIsLoadingRepos(false);
    }
  };

  useEffect(() => {
    fetchProvider();
    fetchRepos();
  }, [providerId]);

  const handleEnable = async (repoId: string) => {
    try {
      setTogglingId(repoId);
      await repoClient.enableReview({ repoId });
      await fetchRepos();
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to enable review");
    } finally {
      setTogglingId(null);
    }
  };

  const handleDisable = async () => {
    if (!disablingId) return;

    try {
      setTogglingId(disablingId);
      await repoClient.disableReview({ repoId: disablingId });
      setDisablingId(null);
      await fetchRepos();
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to disable review");
    } finally {
      setTogglingId(null);
    }
  };

  const repoToDisable = repos.find((r) => r.id === disablingId);

  if (isLoadingProvider) {
    return (
      <div className="text-center py-8">
        <div className="text-text-secondary">Loading...</div>
      </div>
    );
  }

  if (!provider) {
    return (
      <div className="text-center py-8">
        <p className="text-text-secondary mb-4">Provider not found</p>
        <Link
          to="/providers"
          className="text-accent hover:text-accent-hover transition-colors"
        >
          ← Back to providers
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <Link
          to="/providers"
          className="text-text-secondary hover:text-text-primary transition-colors text-sm"
        >
          ← Back to providers
        </Link>
      </div>

      {error && (
        <div className="bg-surface-800 border border-error rounded-lg p-4">
          <p className="text-error">{error}</p>
        </div>
      )}

      <div className="bg-surface-800 border border-border rounded-lg p-6">
        <h1 className="text-2xl font-bold text-text-primary mb-4">{provider.name}</h1>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="text-sm text-text-secondary">Type</label>
            <p className="text-text-primary">{PROVIDER_TYPE_LABELS[provider.type]}</p>
          </div>
          <div>
            <label className="text-sm text-text-secondary">Base URL</label>
            <p className="text-text-primary">{provider.baseUrl || "N/A"}</p>
          </div>
          <div>
            <label className="text-sm text-text-secondary">Provider ID</label>
            <p className="text-text-primary font-mono text-sm">{provider.id}</p>
          </div>
          <div>
            <label className="text-sm text-text-secondary">Created</label>
            <p className="text-text-primary">{formatDate(provider.createdAt)}</p>
          </div>
        </div>
      </div>

      <div className="bg-surface-800 border border-border rounded-lg overflow-hidden">
        <div className="px-6 py-4 border-b border-border">
          <h2 className="text-lg font-semibold text-text-primary">Repositories</h2>
        </div>

        {isLoadingRepos && (
          <div className="text-center py-8">
            <div className="text-text-secondary">Loading repositories...</div>
          </div>
        )}

        {!isLoadingRepos && repos.length === 0 && (
          <div className="text-center py-8">
            <p className="text-text-secondary">No repositories found</p>
          </div>
        )}

        {!isLoadingRepos && repos.length > 0 && (
          <table className="w-full">
            <thead>
              <tr className="border-b border-border">
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Name</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Full Path</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Review</th>
                <th className="text-left px-4 py-3 text-text-secondary font-medium">Created</th>
                <th className="text-right px-4 py-3 text-text-secondary font-medium">Actions</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {repos.map((repo) => (
                <tr key={repo.id} className="hover:bg-surface-700 transition-colors">
                  <td className="px-4 py-3 text-text-primary">{repo.name}</td>
                  <td className="px-4 py-3 text-text-muted text-sm">{repo.fullPath}</td>
                  <td className="px-4 py-3">
                    {repo.reviewEnabled ? (
                      <span className="text-success">Enabled</span>
                    ) : (
                      <span className="text-text-muted">Disabled</span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-text-muted text-sm">
                    {formatDate(repo.createdAt)}
                  </td>
                  <td className="px-4 py-3 text-right">
                    {repo.reviewEnabled ? (
                      <button
                        onClick={() => setDisablingId(repo.id)}
                        disabled={togglingId !== null}
                        aria-label={`Disable review for ${repo.name}`}
                        className="text-error hover:text-error/80 transition-colors text-sm disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        {togglingId === repo.id ? "Disabling..." : "Disable"}
                      </button>
                    ) : (
                      <button
                        onClick={() => handleEnable(repo.id)}
                        disabled={togglingId !== null}
                        aria-label={`Enable review for ${repo.name}`}
                        className="py-1.5 px-3 bg-accent text-white font-medium rounded hover:bg-accent-hover transition-colors text-sm disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        {togglingId === repo.id ? "Enabling..." : "Enable"}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {disablingId && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div className="bg-surface-800 border border-border rounded-lg p-6 max-w-md">
            <h3 className="text-lg font-semibold text-text-primary mb-2">
              Disable review for "{repoToDisable?.name}"?
            </h3>
            <p className="text-text-secondary mb-4">
              New merge requests will no longer be reviewed automatically.
            </p>
            <div className="flex gap-3">
              <button
                onClick={handleDisable}
                disabled={togglingId !== null}
                className="py-2 px-4 bg-error text-white font-semibold rounded-lg hover:bg-error/80 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                Disable
              </button>
              <button
                onClick={() => setDisablingId(null)}
                disabled={togglingId !== null}
                className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
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