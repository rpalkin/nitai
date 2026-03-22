import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { AuthProvider } from "../lib/auth";
import { Layout } from "../components/Layout";
import { HealthCheck } from "../pages/HealthCheck";
import { authClient } from "../lib/connect";
import type { User } from "@gen/api/v1/auth_pb";

vi.mock("../lib/connect", () => ({
  authClient: {
    getMe: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
  },
  providerClient: {},
  repoClient: {},
  reviewClient: {},
  instructionClient: {},
  activityClient: {},
  transport: {},
  createAuthTransport: vi.fn(),
}));

const mockUser = { id: "1", orgId: "org1", email: "test@example.com" } as User;

function renderWithAuth(ui: React.ReactNode, initialEntries = ["/health"]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AuthProvider>{ui}</AuthProvider>
    </MemoryRouter>
  );
}

describe("App", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("renders the app shell with navigation when authenticated", async () => {
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });
    localStorage.setItem("auth_token", "test-token");

    renderWithAuth(
      <Routes>
        <Route element={<Layout />}>
          <Route path="/health" element={<HealthCheck />} />
        </Route>
      </Routes>
    );

    await waitFor(() => {
      expect(screen.getByText("AI Reviewer")).toBeInTheDocument();
      expect(screen.getByText("test@example.com")).toBeInTheDocument();
    });
  });

  it("renders the health check page when authenticated", async () => {
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });
    localStorage.setItem("auth_token", "test-token");

    renderWithAuth(
      <Routes>
        <Route element={<Layout />}>
          <Route path="/health" element={<HealthCheck />} />
        </Route>
      </Routes>
    );

    await waitFor(() => {
      expect(screen.getByText("API Health Check")).toBeInTheDocument();
    });
  });
});