import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter } from "react-router-dom";
import { ActivityLog } from "../pages/ActivityLog";
import { AuthProvider } from "../lib/auth";
import { activityClient, providerClient, repoClient } from "../lib/connect";
import type { ActivityLog as ActivityLogType } from "@gen/api/v1/activity_pb";
import type { Provider } from "@gen/api/v1/provider_pb";
import type { Repository } from "@gen/api/v1/repo_pb";
import { ProviderType } from "@gen/api/v1/provider_pb";

vi.mock("../lib/connect", () => ({
  activityClient: {
    listActivityLogs: vi.fn(),
  },
  providerClient: {
    listProviders: vi.fn(),
  },
  repoClient: {
    listRepos: vi.fn(),
  },
  authClient: {
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

const mockActivityLogs: ActivityLogType[] = [
  {
    id: "log-1",
    orgId: "org-1",
    repoId: "repo-1",
    actorId: "user-1",
    eventType: "provider.created",
    details: { providerName: "GitLab On-Prem" },
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
  } as unknown as ActivityLogType,
  {
    id: "log-2",
    orgId: "org-1",
    repoId: "repo-2",
    actorId: "user-1",
    eventType: "repo.review_enabled",
    details: { repoName: "my-project" },
    createdAt: { seconds: BigInt(1700001000), nanos: 0 },
  } as unknown as ActivityLogType,
  {
    id: "log-3",
    orgId: "org-1",
    repoId: "",
    actorId: "user-2",
    eventType: "review.triggered",
    details: { mrTitle: "Fix bug" },
    createdAt: { seconds: BigInt(1700002000), nanos: 0 },
  } as unknown as ActivityLogType,
];

const mockProviders: Provider[] = [
  {
    id: "provider-1",
    type: ProviderType.GITLAB_SELF_HOSTED,
    name: "GitLab On-Prem",
    baseUrl: "https://gitlab.example.com",
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
  } as Provider,
];

const mockRepos: Repository[] = [
  {
    id: "repo-1",
    providerId: "provider-1",
    remoteId: "123",
    fullPath: "org/project-a",
    reviewEnabled: false,
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
  } as unknown as Repository,
  {
    id: "repo-2",
    providerId: "provider-1",
    remoteId: "456",
    fullPath: "org/project-b",
    reviewEnabled: true,
    createdAt: { seconds: BigInt(1700001000), nanos: 0 },
  } as unknown as Repository,
];

function renderWithRouter(ui: React.ReactNode) {
  return render(
    <BrowserRouter>
      <AuthProvider>{ui}</AuthProvider>
    </BrowserRouter>
  );
}

describe("ActivityLog page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("list rendering", () => {
    it("renders activity log table with entries", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 3,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("provider.created")).toBeInTheDocument();
        expect(screen.getByText("repo.review_enabled")).toBeInTheDocument();
        expect(screen.getByText("review.triggered")).toBeInTheDocument();
      });
    });

    it("shows loading state while fetching", async () => {
      vi.mocked(activityClient.listActivityLogs).mockImplementation(
        () => new Promise(() => {})
      );
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: [],
      } as any);

      renderWithRouter(<ActivityLog />);

      expect(screen.getByText(/loading/i)).toBeInTheDocument();
    });

    it("shows error state on fetch failure", async () => {
      vi.mocked(activityClient.listActivityLogs).mockRejectedValue(
        new Error("Failed to load activity logs")
      );
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: [],
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText(/failed to load activity logs/i)).toBeInTheDocument();
      });
    });

    it("shows empty state when no logs", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: [],
        totalCount: 0,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: [],
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText(/no activity logs/i)).toBeInTheDocument();
      });
    });
  });

  describe("event type filter", () => {
    it("calls API with eventType param when filter changes", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 3,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      await userEvent.selectOptions(
        screen.getByLabelText(/event type/i),
        "provider.created"
      );

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            eventType: "provider.created",
          })
        );
      });
    });
  });

  describe("repo filter", () => {
    it("calls API with repoId param when filter changes", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 3,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      await userEvent.selectOptions(
        screen.getByLabelText(/repository/i),
        "repo-1"
      );

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            repoId: "repo-1",
          })
        );
      });
    });
  });

  describe("pagination", () => {
    it("increments offset when clicking Next", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      vi.mocked(activityClient.listActivityLogs).mockClear();
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);

      await userEvent.click(screen.getByRole("button", { name: /next/i }));

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            offset: 25,
          })
        );
      });
    });

    it("decrements offset when clicking Previous", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      vi.mocked(activityClient.listActivityLogs).mockClear();
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);

      await userEvent.click(screen.getByRole("button", { name: /next/i }));

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            offset: 25,
          })
        );
      });

      vi.mocked(activityClient.listActivityLogs).mockClear();
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);

      await userEvent.click(screen.getByRole("button", { name: /previous/i }));

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            offset: 0,
          })
        );
      });
    });

    it("disables Previous on first page", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      expect(screen.getByRole("button", { name: /previous/i })).toBeDisabled();
    });

    it("disables Next on last page", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 3,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      expect(screen.getByRole("button", { name: /next/i })).toBeDisabled();
    });
  });

  describe("filter reset pagination", () => {
    it("resets to page 1 when filter changes", async () => {
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<ActivityLog />);

      await waitFor(() => {
        expect(screen.getByText("Activity Log")).toBeInTheDocument();
      });

      vi.mocked(activityClient.listActivityLogs).mockClear();
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);

      await userEvent.click(screen.getByRole("button", { name: /next/i }));

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            offset: 25,
          })
        );
      });

      vi.mocked(activityClient.listActivityLogs).mockClear();
      vi.mocked(activityClient.listActivityLogs).mockResolvedValue({
        logs: mockActivityLogs,
        totalCount: 100,
      } as any);

      await userEvent.selectOptions(
        screen.getByLabelText(/event type/i),
        "provider.created"
      );

      await waitFor(() => {
        expect(activityClient.listActivityLogs).toHaveBeenCalledWith(
          expect.objectContaining({
            offset: 0,
          })
        );
      });
    });
  });
});