import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { ProtectedRoute } from "../components/ProtectedRoute";
import { AuthProvider } from "../lib/auth";
import { TOKEN_KEY } from "../lib/auth-constants";
import { authClient } from "../lib/connect";
import type { User } from "@gen/api/v1/auth_pb";

vi.mock("../lib/connect", () => ({
  authClient: {
    getMe: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
  },
}));

function TestProtectedPage() {
  return <div>Protected Content</div>;
}

function TestLoginPage() {
  return <div>Login Page</div>;
}

function renderProtectedRoute(initialEntries: string[] = ["/protected"], token: string | null = null) {
  if (token) {
    localStorage.setItem(TOKEN_KEY, token);
  }

  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<TestLoginPage />} />
          <Route
            element={<ProtectedRoute />}
          >
            <Route path="/protected" element={<TestProtectedPage />} />
          </Route>
        </Routes>
      </AuthProvider>
    </MemoryRouter>
  );
}

describe("ProtectedRoute", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("allows authenticated user to access protected route", async () => {
    const mockUser = { id: "1", orgId: "org1", email: "test@example.com" } as User;
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser } as any);

    renderProtectedRoute(["/protected"], "valid-token");

    await waitFor(() => {
      expect(screen.getByText("Protected Content")).toBeInTheDocument();
    });
  });

  it("redirects unauthenticated user to /login", async () => {
    vi.mocked(authClient.getMe).mockRejectedValue(new Error("No token"));

    renderProtectedRoute(["/protected"], null);

    await waitFor(() => {
      expect(screen.getByText("Login Page")).toBeInTheDocument();
    });
  });

  it("shows loading state while checking auth", () => {
    vi.mocked(authClient.getMe).mockImplementation(() => new Promise(() => {}));

    localStorage.setItem(TOKEN_KEY, "some-token");
    renderProtectedRoute(["/protected"], "some-token");

    expect(screen.getByTestId("loading-spinner")).toBeInTheDocument();
  });
});