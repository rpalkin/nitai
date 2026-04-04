import { useState, useEffect } from "react";
import { activityClient, providerClient, repoClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";
import type { ActivityLog } from "@gen/api/v1/activity_pb";

const EVENT_TYPE_OPTIONS = [
  { value: "", label: "All events" },
  { value: "provider.created", label: "Provider created" },
  { value: "provider.deleted", label: "Provider deleted" },
  { value: "repo.review_enabled", label: "Review enabled" },
  { value: "repo.review_disabled", label: "Review disabled" },
  { value: "review.triggered", label: "Review triggered" },
];

const EVENT_TYPE_COLORS: Record<string, string> = {
  "provider.created": "bg-blue-500/20 text-blue-400",
  "provider.deleted": "bg-blue-500/20 text-blue-400",
  "repo.review_enabled": "bg-green-500/20 text-green-400",
  "repo.review_disabled": "bg-green-500/20 text-green-400",
  "review.triggered": "bg-accent/20 text-accent",
};

function formatDateTime(timestamp: { seconds: bigint; nanos: number } | undefined): string {
  if (!timestamp) return "N/A";
  const date = new Date(Number(timestamp.seconds) * 1000);
  return date.toLocaleString();
}

function formatDetails(details: Record<string, unknown> | undefined): string {
  if (!details || Object.keys(details).length === 0) return "—";
  return JSON.stringify(details, null, 2);
}

export function ActivityLog() {
  const [logs, setLogs] = useState<ActivityLog[]>([]);
  const [totalCount, setTotalCount] = useState(0);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [eventTypeFilter, setEventTypeFilter] = useState("");
  const [repoFilter, setRepoFilter] = useState("");

  const [page, setPage] = useState(1);
  const PAGE_SIZE = 25;

  const [repos, setRepos] = useState<Map<string, string>>(new Map());

  const totalPages = Math.ceil(totalCount / PAGE_SIZE);

  const fetchLogs = async () => {
    try {
      setIsLoading(true);
      setError(null);
      const response = await activityClient.listActivityLogs({
        repoId: repoFilter,
        eventType: eventTypeFilter,
        limit: PAGE_SIZE,
        offset: (page - 1) * PAGE_SIZE,
      });
      setLogs(response.logs);
      setTotalCount(response.totalCount);
    } catch (err) {
      setError(err instanceof ConnectError ? err.message : "Failed to load activity logs");
    } finally {
      setIsLoading(false);
    }
  };

  const fetchRepos = async () => {
    try {
      const providersResponse = await providerClient.listProviders({});
      const repoMap = new Map<string, string>();

      for (const provider of providersResponse.providers) {
        const reposResponse = await repoClient.listRepos({ providerId: provider.id });
        for (const repo of reposResponse.repositories) {
          repoMap.set(repo.id, repo.fullPath);
        }
      }

      setRepos(repoMap);
    } catch (err) {
      console.error("Failed to load repos:", err);
    }
  };

  useEffect(() => {
    fetchRepos();
  }, []);

  useEffect(() => {
    fetchLogs();
  }, [page, eventTypeFilter, repoFilter]);

  const handleEventTypeChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    setEventTypeFilter(e.target.value);
    setPage(1);
  };

  const handleRepoChange = (e: React.ChangeEvent<HTMLSelectElement>) => {
    setRepoFilter(e.target.value);
    setPage(1);
  };

  const handlePrevious = () => {
    if (page > 1) {
      setPage(page - 1);
    }
  };

  const handleNext = () => {
    if (page < totalPages) {
      setPage(page + 1);
    }
  };

  const getRepoName = (repoId: string): string => {
    if (!repoId) return "—";
    return repos.get(repoId) || "—";
  };

  const getEventBadgeColor = (eventType: string): string => {
    return EVENT_TYPE_COLORS[eventType] || "bg-surface-700 text-text-secondary";
  };

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold text-text-primary">Activity Log</h1>

      <div className="flex gap-4">
        <div className="flex-1">
          <label htmlFor="event-type-filter" className="block text-sm font-medium text-text-secondary mb-2">
            Event Type
          </label>
          <select
            id="event-type-filter"
            value={eventTypeFilter}
            onChange={handleEventTypeChange}
            className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
          >
            {EVENT_TYPE_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>

        <div className="flex-1">
          <label htmlFor="repo-filter" className="block text-sm font-medium text-text-secondary mb-2">
            Repository
          </label>
          <select
            id="repo-filter"
            value={repoFilter}
            onChange={handleRepoChange}
            className="w-full px-4 py-3 bg-surface-700 border border-border rounded-lg text-text-primary focus:border-accent focus:ring-2 focus:ring-accent-glow transition-colors"
          >
            <option value="">All repositories</option>
            {Array.from(repos.entries()).map(([id, name]) => (
              <option key={id} value={id}>
                {name}
              </option>
            ))}
          </select>
        </div>
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

      {!isLoading && !error && logs.length === 0 && (
        <div className="text-center py-8">
          <p className="text-text-secondary">No activity logs found</p>
        </div>
      )}

      {!isLoading && !error && logs.length > 0 && (
        <>
          <div className="bg-surface-800 border border-border rounded-lg overflow-hidden">
            <table className="w-full">
              <thead>
                <tr className="border-b border-border">
                  <th className="text-left px-4 py-3 text-text-secondary font-medium">Time</th>
                  <th className="text-left px-4 py-3 text-text-secondary font-medium">Event</th>
                  <th className="text-left px-4 py-3 text-text-secondary font-medium">Repository</th>
                  <th className="text-left px-4 py-3 text-text-secondary font-medium">Details</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {logs.map((log) => (
                  <tr key={log.id} className="hover:bg-surface-700 transition-colors">
                    <td className="px-4 py-3 text-text-primary">{formatDateTime(log.createdAt)}</td>
                    <td className="px-4 py-3">
                      <span className={`px-2 py-1 rounded text-sm ${getEventBadgeColor(log.eventType)}`}>
                        {log.eventType}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-text-secondary">{getRepoName(log.repoId)}</td>
                    <td className="px-4 py-3 text-text-muted text-sm">
                      <pre className="whitespace-pre-wrap">{formatDetails(log.details)}</pre>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex items-center justify-between">
            <div className="text-text-secondary">
              Page {page} of {totalPages || 1} ({totalCount} total)
            </div>
            <div className="flex gap-3">
              <button
                onClick={handlePrevious}
                disabled={page === 1}
                className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                Previous
              </button>
              <button
                onClick={handleNext}
                disabled={page >= totalPages}
                className="py-2 px-4 bg-surface-700 text-text-primary rounded-lg hover:bg-surface-600 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
              >
                Next
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  );
}