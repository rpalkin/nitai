import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { Repos } from "../pages/Repos";
import { AuthProvider } from "../lib/auth";
import { repoClient } from "../lib/connect";
import type { Repository } from "@gen/api/v1/repo_pb";

vi.mock("../lib/connect", () => ({
  repoClient: {
    listRepos: vi.fn(),
    enableReview: vi.fn(),
    disableReview: vi.fn(),
  },
  authClient: {
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

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

function renderWithRouter(ui: React.ReactNode, initialEntries = ["/providers/p1/repos"]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AuthProvider>
        <Routes>
          <Route path="/providers/:providerId/repos" element={ui} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>
  );
}

describe("Repos page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("list rendering", () => {
    it("renders repos table with name, full path, status, and created date", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
        expect(screen.getByText("org/api-server")).toBeInTheDocument();
        expect(screen.getByText("Enabled")).toBeInTheDocument();
        expect(screen.getByText("frontend")).toBeInTheDocument();
        expect(screen.getByText("org/frontend")).toBeInTheDocument();
        expect(screen.getByText("Disabled")).toBeInTheDocument();
      });
    });

    it("shows loading state while fetching", async () => {
      vi.mocked(repoClient.listRepos).mockImplementation(() => new Promise(() => {}));

      renderWithRouter(<Repos />);

      expect(screen.getByText(/loading/i)).toBeInTheDocument();
    });

    it("shows error state on fetch failure", async () => {
      vi.mocked(repoClient.listRepos).mockRejectedValue(new Error("Failed to load repos"));

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText(/failed to load repos/i)).toBeInTheDocument();
      });
    });

    it("shows empty state when provider has no repos", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: [],
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText(/no repositories found/i)).toBeInTheDocument();
      });
    });
  });

  describe("toggle enable", () => {
    it("calls enableReview when Enable button clicked on disabled repo", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.enableReview).mockResolvedValue({
        repository: { ...mockRepos[1], reviewEnabled: true },
      } as any);

      renderWithRouter(<Repos />);

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

    it("refreshes list after enabling review", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.enableReview).mockResolvedValue({
        repository: { ...mockRepos[1], reviewEnabled: true },
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("frontend")).toBeInTheDocument();
      });

      const enableButtons = screen.getAllByRole("button", { name: /enable/i });
      await userEvent.click(enableButtons[0]);

      await waitFor(() => {
        expect(repoClient.listRepos).toHaveBeenCalledTimes(2);
      });
    });

    it("shows spinner on Enable button while enabling", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.enableReview).mockImplementation(
        () => new Promise((resolve) => setTimeout(() => resolve({ repository: mockRepos[1] } as any), 100))
      );

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("frontend")).toBeInTheDocument();
      });

      const enableButtons = screen.getAllByRole("button", { name: /enable/i });
      await userEvent.click(enableButtons[0]);

      await waitFor(() => {
        expect(enableButtons[0]).toBeDisabled();
      });
    });
  });

  describe("toggle disable", () => {
    it("shows confirmation dialog when Disable button clicked", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      expect(screen.getByText(/disable review/i)).toBeInTheDocument();
      expect(screen.getByText(/new merge requests will no longer be reviewed automatically/i)).toBeInTheDocument();
    });

    it("calls disableReview on confirmation", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.disableReview).mockResolvedValue({
        repository: { ...mockRepos[0], reviewEnabled: false },
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      const confirmButton = screen.getByRole("button", { name: /^disable$/i });
      await userEvent.click(confirmButton);

      await waitFor(() => {
        expect(repoClient.disableReview).toHaveBeenCalledWith({
          repoId: "r1",
        });
      });
    });

    it("refreshes list after disabling review", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.disableReview).mockResolvedValue({
        repository: { ...mockRepos[0], reviewEnabled: false },
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      vi.mocked(repoClient.listRepos).mockClear();

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      const confirmButton = screen.getByRole("button", { name: /^disable$/i });
      await userEvent.click(confirmButton);

      await waitFor(() => {
        expect(repoClient.listRepos).toHaveBeenCalled();
      });
      
      const callCount = vi.mocked(repoClient.listRepos).mock.calls.length;
      expect(callCount).toBeGreaterThanOrEqual(1);
    });

    it("cancels disable without calling disableReview", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      const cancelButton = screen.getByRole("button", { name: /cancel/i });
      await userEvent.click(cancelButton);

      await waitFor(() => {
        expect(screen.queryByText(/disable review/i)).not.toBeInTheDocument();
      });

      expect(repoClient.disableReview).not.toHaveBeenCalled();
    });

    it("shows spinner on Disable button while disabling", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(repoClient.disableReview).mockImplementation(
        () => new Promise((resolve) => setTimeout(() => resolve({ repository: mockRepos[0] } as any), 100))
      );

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText("api-server")).toBeInTheDocument();
      });

      const disableButtons = screen.getAllByRole("button", { name: /disable/i });
      await userEvent.click(disableButtons[0]);

      const confirmButton = screen.getByRole("button", { name: /^disable$/i });
      await userEvent.click(confirmButton);

      await waitFor(() => {
        expect(confirmButton).toBeDisabled();
      });
    });
  });

  describe("navigation", () => {
    it("has back link to providers page", async () => {
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);

      renderWithRouter(<Repos />);

      await waitFor(() => {
        expect(screen.getByText(/← back to providers/i)).toBeInTheDocument();
      });

      const backLink = screen.getByRole("link", { name: /← back to providers/i });
      expect(backLink).toHaveAttribute("href", "/providers");
    });
  });
});