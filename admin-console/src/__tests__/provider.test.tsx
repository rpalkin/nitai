import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { Provider } from "../pages/Provider";
import { AuthProvider } from "../lib/auth";
import { providerClient, repoClient } from "../lib/connect";
import { ProviderType } from "@gen/api/v1/provider_pb";
import type { Provider as ProviderMsg } from "@gen/api/v1/provider_pb";
import type { Repository } from "@gen/api/v1/repo_pb";

vi.mock("../lib/connect", () => ({
  providerClient: {
    listProviders: vi.fn(),
  },
  repoClient: {
    listRepos: vi.fn(),
    enableReview: vi.fn(),
    disableReview: vi.fn(),
  },
  authClient: {
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

const mockProvider: ProviderMsg = {
  id: "p1",
  type: ProviderType.GITLAB_CLOUD,
  name: "GitLab Cloud",
  baseUrl: "https://gitlab.com",
  createdAt: { seconds: BigInt(1700000000), nanos: 0 },
} as ProviderMsg;

const mockRepos: Repository[] = [
  {
    id: "r1",
    providerId: "p1",
    remoteId: "101",
    name: "api-server",
    fullPath: "org/api-server",
    reviewEnabled: true,
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
  } as Repository,
  {
    id: "r2",
    providerId: "p1",
    remoteId: "102",
    name: "frontend",
    fullPath: "org/frontend",
    reviewEnabled: false,
    createdAt: { seconds: BigInt(1700001000), nanos: 0 },
  } as Repository,
];

function renderWithRouter(initialEntries = ["/providers/p1"]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AuthProvider>
        <Routes>
          <Route path="/providers/:providerId" element={<Provider />} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>
  );
}

describe("Provider page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("provider details", () => {
    it("renders provider details", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByRole("heading", { name: "GitLab Cloud" })).toBeInTheDocument();
        expect(screen.getByText("https://gitlab.com")).toBeInTheDocument();
        expect(screen.getByText("p1")).toBeInTheDocument();
      });
    });

    it("shows loading state while fetching provider", async () => {
      vi.mocked(providerClient.listProviders).mockImplementation(() => new Promise(() => {}));

      renderWithRouter();

      expect(screen.getByText(/loading/i)).toBeInTheDocument();
    });

    it("shows error when provider not found", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText(/provider not found/i)).toBeInTheDocument();
      });
    });
  });

  describe("repository list", () => {
    it("renders repository list", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
        expect(screen.getByText("org/api-server")).toBeInTheDocument();
        expect(screen.getByText("frontend")).toBeInTheDocument();
      });
    });

    it("shows empty state when no repos", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: [],
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText(/no repositories found/i)).toBeInTheDocument();
      });
    });
  });

  describe("toggle review", () => {
    it("enables review for disabled repo", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.enableReview).mockResolvedValue({
        repository: { ...mockRepos[1], reviewEnabled: true },
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText("frontend")).toBeInTheDocument();
      });

      const enableButtons = screen.getAllByRole("button", { name: /enable/i });
      await userEvent.click(enableButtons[0]);

      await waitFor(() => {
        expect(repoClient.enableReview).toHaveBeenCalledWith({
          repoId: "r2",
        });
      });
    });

    it("disables review with confirmation", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.disableReview).mockResolvedValue({
        repository: { ...mockRepos[0], reviewEnabled: false },
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      expect(screen.getByText(/disable review/i)).toBeInTheDocument();

      const confirmButton = screen.getByRole("button", { name: /^disable$/i });
      await userEvent.click(confirmButton);

      await waitFor(() => {
        expect(repoClient.disableReview).toHaveBeenCalledWith({
          repoId: "r1",
        });
      });
    });
  });

  describe("navigation", () => {
    it("has back link to providers", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [mockProvider],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter();

      await waitFor(() => {
        expect(screen.getByText(/← back to providers/i)).toBeInTheDocument();
      });

      const backLink = screen.getByRole("link", { name: /← back to providers/i });
      expect(backLink).toHaveAttribute("href", "/providers");
    });
  });
});