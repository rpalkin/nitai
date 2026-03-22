import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter } from "react-router-dom";
import { Providers } from "../pages/Providers";
import { AuthProvider } from "../lib/auth";
import { providerClient } from "../lib/connect";
import type { Provider } from "@gen/api/v1/provider_pb";
import { ProviderType } from "@gen/api/v1/provider_pb";

vi.mock("../lib/connect", () => ({
  providerClient: {
    listProviders: vi.fn(),
    createProvider: vi.fn(),
    deleteProvider: vi.fn(),
  },
  authClient: {
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

const mockProviders: Provider[] = [
  {
    id: "1",
    type: ProviderType.GITLAB_SELF_HOSTED,
    name: "GitLab On-Prem",
    baseUrl: "https://gitlab.example.com",
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
  } as Provider,
  {
    id: "2",
    type: ProviderType.GITHUB,
    name: "GitHub Enterprise",
    baseUrl: "https://github.example.com",
    createdAt: { seconds: BigInt(1700001000), nanos: 0 },
  } as Provider,
];

function renderWithRouter(ui: React.ReactNode) {
  return render(
    <BrowserRouter>
      <AuthProvider>{ui}</AuthProvider>
    </BrowserRouter>
  );
}

describe("Providers page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("list rendering", () => {
    it("renders provider list with names in table", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText("GitLab On-Prem")).toBeInTheDocument();
        expect(screen.getByText("GitHub Enterprise")).toBeInTheDocument();
      });
    });

    it("shows empty state when no providers", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText(/no providers/i)).toBeInTheDocument();
        const addButtons = screen.getAllByRole("button", { name: /add provider/i });
        expect(addButtons.length).toBeGreaterThanOrEqual(1);
      });
    });

    it("shows loading state while fetching", async () => {
      vi.mocked(providerClient.listProviders).mockImplementation(
        () => new Promise(() => {})
      );

      renderWithRouter(<Providers />);

      expect(screen.getByText(/loading/i)).toBeInTheDocument();
    });

    it("shows error state on fetch failure", async () => {
      vi.mocked(providerClient.listProviders).mockRejectedValue(
        new Error("Failed to load providers")
      );

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText(/failed to load providers/i)).toBeInTheDocument();
      });
    });
  });

  describe("create provider form", () => {
    it("shows form fields when Add provider is clicked", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);

      expect(screen.getByLabelText(/name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/type/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/token/i)).toBeInTheDocument();
    });

    it("shows base url field for self-hosted gitlab", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);

      await userEvent.selectOptions(screen.getByLabelText(/type/i), "1");

      expect(screen.getByLabelText(/base url/i)).toBeInTheDocument();
    });

    it("submits createProvider with correct args", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });
      vi.mocked(providerClient.createProvider).mockResolvedValue({
        provider: { id: "new-id", type: ProviderType.GITLAB_CLOUD, name: "GitLab Cloud" } as Provider,
        webhookSecret: "secret-123",
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);

      await userEvent.type(screen.getByLabelText(/name/i), "GitLab Cloud");
      await userEvent.selectOptions(screen.getByLabelText(/type/i), "2");
      await userEvent.type(screen.getByLabelText(/token/i), "my-token");

      await userEvent.click(screen.getByRole("button", { name: /create/i }));

      await waitFor(() => {
        expect(providerClient.createProvider).toHaveBeenCalledWith({
          type: ProviderType.GITLAB_CLOUD,
          name: "GitLab Cloud",
          baseUrl: "https://gitlab.com",
          token: "my-token",
        });
      });
    });
  });

  describe("webhook secret display", () => {
    it("shows webhook secret after creation", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });
      vi.mocked(providerClient.createProvider).mockResolvedValue({
        provider: { id: "new-id", type: ProviderType.GITLAB_CLOUD, name: "GitLab Cloud" } as Provider,
        webhookSecret: "secret-123",
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);
      await userEvent.type(screen.getByLabelText(/name/i), "GitLab Cloud");
      await userEvent.selectOptions(screen.getByLabelText(/type/i), "2");
      await userEvent.type(screen.getByLabelText(/token/i), "my-token");
      await userEvent.click(screen.getByRole("button", { name: /create/i }));

      await waitFor(() => {
        expect(screen.getByText("secret-123")).toBeInTheDocument();
        expect(screen.getByRole("button", { name: /copy/i })).toBeInTheDocument();
      });
    });

    it("dismisses webhook secret banner", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });
      vi.mocked(providerClient.createProvider).mockResolvedValue({
        provider: { id: "new-id", type: ProviderType.GITLAB_CLOUD, name: "GitLab Cloud" } as Provider,
        webhookSecret: "secret-123",
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);
      await userEvent.type(screen.getByLabelText(/name/i), "GitLab Cloud");
      await userEvent.selectOptions(screen.getByLabelText(/type/i), "2");
      await userEvent.type(screen.getByLabelText(/token/i), "my-token");
      await userEvent.click(screen.getByRole("button", { name: /create/i }));

      await waitFor(() => {
        expect(screen.getByText("secret-123")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /dismiss/i }));

      await waitFor(() => {
        expect(screen.queryByText("secret-123")).not.toBeInTheDocument();
      });
    });
  });

  describe("delete provider", () => {
    it("shows confirmation dialog and calls deleteProvider", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      });
      vi.mocked(providerClient.deleteProvider).mockResolvedValue({});

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText("GitLab On-Prem")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /delete.*gitlab on-prem/i }));

      expect(screen.getByText(/delete provider/i)).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: /^delete$/i }));

      await waitFor(() => {
        expect(providerClient.deleteProvider).toHaveBeenCalledWith({
          id: "1",
        });
      });
    });

    it("cancels delete without calling deleteProvider", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText("GitLab On-Prem")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /delete.*gitlab on-prem/i }));

      expect(screen.getByText(/delete provider/i)).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: /cancel/i }));

      await waitFor(() => {
        expect(screen.queryByText(/delete provider/i)).not.toBeInTheDocument();
      });

      expect(providerClient.deleteProvider).not.toHaveBeenCalled();
    });
  });

  describe("list refresh", () => {
    it("refreshes list after create", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [],
      });
      vi.mocked(providerClient.createProvider).mockResolvedValue({
        provider: { id: "new-id", type: ProviderType.GITLAB_CLOUD, name: "GitLab Cloud" } as Provider,
        webhookSecret: "secret-123",
      });

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByRole("button", { name: /add provider/i })).toBeInTheDocument();
      });

      const addButtons = screen.getAllByRole("button", { name: /add provider/i });
      await userEvent.click(addButtons[0]);
      await userEvent.type(screen.getByLabelText(/name/i), "GitLab Cloud");
      await userEvent.selectOptions(screen.getByLabelText(/type/i), "2");
      await userEvent.type(screen.getByLabelText(/token/i), "my-token");
      await userEvent.click(screen.getByRole("button", { name: /create/i }));

      await waitFor(() => {
        expect(providerClient.listProviders).toHaveBeenCalledTimes(2);
      });
    });

    it("refreshes list after delete", async () => {
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: mockProviders,
      });
      vi.mocked(providerClient.deleteProvider).mockResolvedValue({});

      renderWithRouter(<Providers />);

      await waitFor(() => {
        expect(screen.getByText("GitLab On-Prem")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /delete.*gitlab on-prem/i }));
      await userEvent.click(screen.getByRole("button", { name: /^delete$/i }));

      await waitFor(() => {
        expect(providerClient.listProviders).toHaveBeenCalledTimes(2);
      });
    });
  });
});