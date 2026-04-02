import { useState, useEffect } from "react";
import { useParams, Link } from "react-router-dom";
import { repoClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";
import type { Repository } from "@gen/api/v1/repo_pb";

function formatDate(timestamp: { seconds: bigint; nanos: number } | undefined): string {
  if (!timestamp) return "N/A";
  const date = new Date(Number(timestamp.seconds) * 1000);
  return date.toLocaleDateString();
}

export function Repos() {
  const { providerId } = useParams<{ providerId: string }>();
  const [repos, setRepos] = useState<Repository[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [togglingId, setTogglingId] = useState<string | null>(null);
  const [disablingId, setDisablingId] = useState<string | null>(null);

  const fetchRepos = async () => {
    if (!providerId) return;

    try {
      setIsLoading(true);
      setError(null);
      const response = await repoClient.listRepos({ providerId });
      setRepos(response.repositories);
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to load repos");
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
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

      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold text-text-primary">Repositories</h1>
      </div>

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

      {!isLoading && !error && repos.length === 0 && (
        <div className="text-center py-8">
          <p className="text-text-secondary">No repositories found</p>
        </div>
      )}

      {!isLoading && !error && repos.length > 0 && (
        <div className="bg-surface-800 border border-border rounded-lg overflow-hidden">
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
        </div>
      )}

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