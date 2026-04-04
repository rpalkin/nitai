import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter } from "react-router-dom";
import { Instructions } from "./Instructions";
import { AuthProvider } from "@/lib/auth";
import { instructionClient, repoClient, providerClient } from "@/lib/connect";
import type { ReviewInstruction } from "@gen/api/v1/instruction_pb";
import type { Repository } from "@gen/api/v1/repo_pb";

vi.mock("@/lib/connect", () => ({
  instructionClient: {
    listInstructions: vi.fn(),
    createInstruction: vi.fn(),
    updateInstruction: vi.fn(),
    deleteInstruction: vi.fn(),
  },
  repoClient: {
    listRepos: vi.fn(),
  },
  providerClient: {
    listProviders: vi.fn(),
  },
  authClient: {
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

const mockInstructions: ReviewInstruction[] = [
  {
    id: "inst-1",
    orgId: "org-1",
    name: "Go Best Practices",
    content: "Always check errors in Go code.",
    repoFilter: ["repo-1", "repo-2"],
    filePatternFilter: ["*.go"],
    enabled: true,
    createdAt: { seconds: BigInt(1700000000), nanos: 0 },
    updatedAt: { seconds: BigInt(1700001000), nanos: 0 },
  } as unknown as ReviewInstruction,
  {
    id: "inst-2",
    orgId: "org-1",
    name: "TypeScript Guidelines",
    content: "Prefer interfaces over types.",
    repoFilter: [],
    filePatternFilter: ["*.ts", "*.tsx"],
    enabled: false,
    createdAt: { seconds: BigInt(1700002000), nanos: 0 },
    updatedAt: { seconds: BigInt(1700003000), nanos: 0 },
  } as unknown as ReviewInstruction,
];

const mockRepos: Repository[] = [
  {
    id: "repo-1",
    providerId: "provider-1",
    remoteId: "123",
    name: "project-alpha",
    fullPath: "org/project-alpha",
    reviewEnabled: true,
    createdAt: { seconds: BigInt(1699999999), nanos: 0 },
  } as unknown as Repository,
  {
    id: "repo-2",
    providerId: "provider-1",
    remoteId: "456",
    name: "project-beta",
    fullPath: "org/project-beta",
    reviewEnabled: false,
    createdAt: { seconds: BigInt(1699999998), nanos: 0 },
  } as unknown as Repository,
];

function renderWithRouter(ui: React.ReactNode) {
  return render(
    <BrowserRouter>
      <AuthProvider>{ui}</AuthProvider>
    </BrowserRouter>
  );
}

describe("Instructions page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(providerClient.listProviders).mockResolvedValue({ providers: [] } as any);
  });

  describe("list rendering", () => {
    it("renders loading state, then instruction list table", async () => {
      vi.mocked(instructionClient.listInstructions).mockImplementation(
        () => new Promise(() => {})
      );
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      expect(screen.getByText(/loading/i)).toBeInTheDocument();
    });

    it("renders instruction list with names in table", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: mockInstructions,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(screen.getByText("Go Best Practices")).toBeInTheDocument();
        expect(screen.getByText("TypeScript Guidelines")).toBeInTheDocument();
      });
    });

    it("renders empty state with create prompt when no instructions exist", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(screen.getByText(/no instructions yet/i)).toBeInTheDocument();
      });
    });

    it("shows error state when listInstructions fails", async () => {
      vi.mocked(instructionClient.listInstructions).mockRejectedValue(
        new Error("Failed to load instructions")
      );
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(
          screen.getByText(/failed to load instructions/i)
        ).toBeInTheDocument();
      });
    });
  });

  describe("create form", () => {
    it("opens on button click, submits with name + content + repo filter + file patterns, re-fetches list", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [{ id: "provider-1", name: "Test Provider" } as any],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(instructionClient.createInstruction).mockResolvedValue({
        instruction: mockInstructions[0],
      } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        const buttons = screen.getAllByRole("button", { name: /add instruction/i });
        expect(buttons.length).toBeGreaterThan(0);
      });

      const addButtons = screen.getAllByRole("button", { name: /add instruction/i });
      await userEvent.click(addButtons[0]);

      expect(screen.getByLabelText(/name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/content/i)).toBeInTheDocument();

      await userEvent.type(screen.getByLabelText(/name/i), "New Instruction");
      await userEvent.type(
        screen.getByLabelText(/content/i),
        "Review content here"
      );

      await userEvent.click(
        screen.getByRole("button", { name: /all repositories/i })
      );

      await userEvent.click(
        screen.getByLabelText(/org\/project-alpha/i)
      );

      const fileInput = screen.getByPlaceholderText(/e\.g/);
      await userEvent.type(fileInput, "*.py{enter}");

      await userEvent.click(screen.getByRole("button", { name: /save/i }));

      await waitFor(() => {
        expect(instructionClient.createInstruction).toHaveBeenCalledWith({
          name: "New Instruction",
          content: "Review content here",
          repoFilter: ["repo-1"],
          filePatternFilter: ["*.py"],
        });
      });

      await waitFor(() => {
        expect(instructionClient.listInstructions).toHaveBeenCalledTimes(2);
      });
    });

    it("shows validation — name and content are required", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        const buttons = screen.getAllByRole("button", { name: /add instruction/i });
        expect(buttons.length).toBeGreaterThan(0);
      });

      const addButtons = screen.getAllByRole("button", { name: /add instruction/i });
      await userEvent.click(addButtons[0]);

      await userEvent.click(screen.getByRole("button", { name: /save/i }));

      await waitFor(() => {
        expect(
          screen.getByText(/name and content are required/i)
        ).toBeInTheDocument();
      });

      expect(instructionClient.createInstruction).not.toHaveBeenCalled();
    });
  });

  describe("edit form", () => {
    it("clicking Edit on a row expands inline edit form pre-filled with existing values", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: mockInstructions,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(screen.getByText("Go Best Practices")).toBeInTheDocument();
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /edit/i })[0]
      );

      await waitFor(() => {
        const nameInput = screen.getByLabelText(/name/i) as HTMLInputElement;
        const contentInput = screen.getByLabelText(
          /content/i
        ) as HTMLTextAreaElement;

        expect(nameInput.value).toBe("Go Best Practices");
        expect(contentInput.value).toBe("Always check errors in Go code.");
      });
    });

    it("toggling enabled calls updateInstruction with enabled flipped", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: mockInstructions,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);
      vi.mocked(instructionClient.updateInstruction).mockResolvedValue({
        instruction: { ...mockInstructions[0], enabled: false },
      } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(screen.getByText("Go Best Practices")).toBeInTheDocument();
      });

      const toggleButton = screen.getAllByRole("button", { name: /toggle/i })[0];
      await userEvent.click(toggleButton);

      await waitFor(() => {
        expect(instructionClient.updateInstruction).toHaveBeenCalledWith({
          id: "inst-1",
          name: "Go Best Practices",
          content: "Always check errors in Go code.",
          repoFilter: ["repo-1", "repo-2"],
          filePatternFilter: ["*.go"],
          enabled: false,
        });
      });
    });
  });

  describe("delete", () => {
    it("clicking delete opens confirmation modal, confirming calls deleteInstruction, list refreshes", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: mockInstructions,
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);
      vi.mocked(instructionClient.deleteInstruction).mockResolvedValue({} as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(screen.getByText("Go Best Practices")).toBeInTheDocument();
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /delete/i })[0]
      );

      await waitFor(() => {
        expect(screen.getByText(/delete instruction/i)).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /^delete$/i }));

      await waitFor(() => {
        expect(instructionClient.deleteInstruction).toHaveBeenCalledWith({
          id: "inst-1",
        });
      });

      await waitFor(() => {
        expect(instructionClient.listInstructions).toHaveBeenCalledTimes(2);
      });
    });
  });

  describe("repo filter", () => {
    it("repos are fetched and shown as checkboxes; selecting repos populates repoFilter", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(providerClient.listProviders).mockResolvedValue({
        providers: [{ id: "provider-1", name: "Test Provider" } as any],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({
        repositories: mockRepos,
      } as any);
      vi.mocked(instructionClient.createInstruction).mockResolvedValue({
        instruction: mockInstructions[0],
      } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        expect(providerClient.listProviders).toHaveBeenCalled();
      });
      await waitFor(() => {
        expect(repoClient.listRepos).toHaveBeenCalledWith({ providerId: "provider-1" });
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /add instruction/i })[0]
      );

      await userEvent.type(screen.getByLabelText(/name/i), "Test");
      await userEvent.type(screen.getByLabelText(/content/i), "Test content");

      await userEvent.click(
        screen.getByRole("button", { name: /all repositories/i })
      );

      expect(screen.getByLabelText("org/project-alpha")).toBeInTheDocument();
      expect(screen.getByLabelText("org/project-beta")).toBeInTheDocument();

      await userEvent.click(screen.getByLabelText("org/project-alpha"));
      await userEvent.click(screen.getByLabelText("org/project-beta"));

      await userEvent.click(screen.getByRole("button", { name: /save/i }));

      await waitFor(() => {
        expect(instructionClient.createInstruction).toHaveBeenCalledWith(
          expect.objectContaining({
            repoFilter: ["repo-1", "repo-2"],
          })
        );
      });
    });
  });

  describe("file pattern filter", () => {
    it("typing a pattern and pressing Enter adds it as a tag; clicking X removes it", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);
      vi.mocked(instructionClient.createInstruction).mockResolvedValue({
        instruction: mockInstructions[0],
      } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        const buttons = screen.getAllByRole("button", { name: /add instruction/i });
        expect(buttons.length).toBeGreaterThan(0);
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /add instruction/i })[0]
      );

      await userEvent.type(screen.getByLabelText(/name/i), "Test");
      await userEvent.type(screen.getByLabelText(/content/i), "Content");

      const fileInput = screen.getByPlaceholderText(/e\.g/);
      await userEvent.type(fileInput, "*.go{enter}");

      await waitFor(() => {
        expect(screen.getByText("*.go")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /remove \*\.go/i }));

      await waitFor(() => {
        expect(screen.queryByText("*.go")).not.toBeInTheDocument();
      });
    });
  });

  describe("language presets", () => {
    it("language preset buttons add corresponding glob patterns to file pattern list", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);
      vi.mocked(instructionClient.createInstruction).mockResolvedValue({
        instruction: mockInstructions[0],
      } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        const buttons = screen.getAllByRole("button", { name: /add instruction/i });
        expect(buttons.length).toBeGreaterThan(0);
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /add instruction/i })[0]
      );

      await userEvent.type(screen.getByLabelText(/name/i), "Test");
      await userEvent.type(screen.getByLabelText(/content/i), "Content");

      await userEvent.click(screen.getByRole("button", { name: /typescript/i }));

      await waitFor(() => {
        expect(screen.getByText("*.ts")).toBeInTheDocument();
        expect(screen.getByText("*.tsx")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /save/i }));

      await waitFor(() => {
        expect(instructionClient.createInstruction).toHaveBeenCalledWith(
          expect.objectContaining({
            filePatternFilter: ["*.ts", "*.tsx"],
          })
        );
      });
    });

    it("clicking a preset again removes those patterns", async () => {
      vi.mocked(instructionClient.listInstructions).mockResolvedValue({
        instructions: [],
      } as any);
      vi.mocked(repoClient.listRepos).mockResolvedValue({ repositories: [] } as any);

      renderWithRouter(<Instructions />);

      await waitFor(() => {
        const buttons = screen.getAllByRole("button", { name: /add instruction/i });
        expect(buttons.length).toBeGreaterThan(0);
      });

      await userEvent.click(
        screen.getAllByRole("button", { name: /add instruction/i })[0]
      );

      await userEvent.click(screen.getByRole("button", { name: /typescript/i }));

      await waitFor(() => {
        expect(screen.getByText("*.ts")).toBeInTheDocument();
      });

      await userEvent.click(screen.getByRole("button", { name: /typescript/i }));

      await waitFor(() => {
        expect(screen.queryByText("*.ts")).not.toBeInTheDocument();
        expect(screen.queryByText("*.tsx")).not.toBeInTheDocument();
      });
    });
  });
});