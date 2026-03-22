import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AuthProvider, useAuth } from "../lib/auth";
import { TOKEN_KEY } from "../lib/auth-constants";
import type { ReactNode } from "react";
import { authClient } from "../lib/connect";
import type { User } from "@gen/api/v1/auth_pb";

vi.mock("../lib/connect", () => ({
  authClient: {
    login: vi.fn(),
    register: vi.fn(),
    getMe: vi.fn(),
  },
}));

function renderWithProvider(ui: ReactNode) {
  return render(<AuthProvider>{ui}</AuthProvider>);
}

function TestConsumer() {
  const auth = useAuth();
  return (
    <div>
      <span data-testid="isAuthenticated">{auth.isAuthenticated.toString()}</span>
      <span data-testid="isLoading">{auth.isLoading.toString()}</span>
      <span data-testid="user">{auth.user ? auth.user.email : "null"}</span>
      <button onClick={() => auth.login("test@example.com", "password")}>Login</button>
      <button onClick={() => auth.register("test@example.com", "password")}>Register</button>
      <button onClick={() => auth.logout()}>Logout</button>
    </div>
  );
}

describe("AuthContext", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("renders children", () => {
    renderWithProvider(<div>Test Child</div>);
    expect(screen.getByText("Test Child")).toBeInTheDocument();
  });

  it("useAuth returns expected shape", () => {
    renderWithProvider(<TestConsumer />);
    expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("false");
    expect(screen.getByTestId("isLoading")).toHaveTextContent("false");
    expect(screen.getByTestId("user")).toHaveTextContent("null");
  });

  it("login() stores token in localStorage and sets user", async () => {
    const mockUser = { id: "1", orgId: "org1", email: "test@example.com" } as User;
    vi.mocked(authClient.login).mockResolvedValue({
      token: "test-token",
      user: mockUser,
    });
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });

    renderWithProvider(<TestConsumer />);
    await userEvent.click(screen.getByText("Login"));

    await waitFor(() => {
      expect(localStorage.getItem(TOKEN_KEY)).toBe("test-token");
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("true");
      expect(screen.getByTestId("user")).toHaveTextContent("test@example.com");
    });
  });

  it("register() stores token in localStorage and sets user", async () => {
    const mockUser = { id: "1", orgId: "org1", email: "new@example.com" } as User;
    vi.mocked(authClient.register).mockResolvedValue({
      token: "new-token",
      user: mockUser,
    });
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });

    renderWithProvider(<TestConsumer />);
    await userEvent.click(screen.getByText("Register"));

    await waitFor(() => {
      expect(localStorage.getItem(TOKEN_KEY)).toBe("new-token");
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("true");
      expect(screen.getByTestId("user")).toHaveTextContent("new@example.com");
    });
  });

  it("logout() clears token and user", async () => {
    const mockUser = { id: "1", orgId: "org1", email: "test@example.com" } as User;
    vi.mocked(authClient.login).mockResolvedValue({
      token: "test-token",
      user: mockUser,
    });
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });

    renderWithProvider(<TestConsumer />);
    await userEvent.click(screen.getByText("Login"));

    await waitFor(() => {
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("true");
    });

    await userEvent.click(screen.getByText("Logout"));

    await waitFor(() => {
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("false");
      expect(screen.getByTestId("user")).toHaveTextContent("null");
    });
  });

  it("isAuthenticated is false when no token", () => {
    renderWithProvider(<TestConsumer />);
    expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("false");
  });

  it("reads token from localStorage and calls getMe on mount", async () => {
    const mockUser = { id: "1", orgId: "org1", email: "stored@example.com" } as User;
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser });

    localStorage.setItem(TOKEN_KEY, "stored-token");

    renderWithProvider(<TestConsumer />);

    expect(screen.getByTestId("isLoading")).toHaveTextContent("true");

    await waitFor(() => {
      expect(authClient.getMe).toHaveBeenCalled();
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("true");
      expect(screen.getByTestId("user")).toHaveTextContent("stored@example.com");
    });
  });

  it("clears token and sets unauthenticated if getMe fails", async () => {
    vi.mocked(authClient.getMe).mockRejectedValue(new Error("Invalid token"));

    localStorage.setItem(TOKEN_KEY, "expired-token");

    renderWithProvider(<TestConsumer />);

    await waitFor(() => {
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(screen.getByTestId("isAuthenticated")).toHaveTextContent("false");
      expect(screen.getByTestId("user")).toHaveTextContent("null");
    });
  });
});