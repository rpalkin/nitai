import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { BrowserRouter, MemoryRouter, Routes, Route } from "react-router-dom";
import { Login } from "../pages/Login";
import { Register } from "../pages/Register";
import { AuthProvider } from "../lib/auth";
import { authClient } from "../lib/connect";
import type { User } from "@gen/api/v1/auth_pb";

vi.mock("../lib/connect", () => ({
  authClient: {
    login: vi.fn(),
    register: vi.fn(),
    getMe: vi.fn().mockRejectedValue(new Error("Not authenticated")),
  },
}));

const mockUser = { id: "1", orgId: "org1", email: "test@example.com" } as User;

function renderWithRouter(ui: React.ReactNode) {
  return render(
    <BrowserRouter>
      <AuthProvider>{ui}</AuthProvider>
    </BrowserRouter>
  );
}

describe("Login page", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("renders email and password fields and sign-in button", () => {
    renderWithRouter(<Login />);
    expect(screen.getByLabelText(/email/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /sign in/i })).toBeInTheDocument();
  });

  it("has link to register page", () => {
    renderWithRouter(<Login />);
    expect(screen.getByRole("link", { name: /create one/i })).toHaveAttribute("href", "/register");
  });

  it("calls authClient.login on form submission", async () => {
    vi.mocked(authClient.login).mockResolvedValue({
      token: "test-token",
      user: mockUser,
    } as any);

    renderWithRouter(<Login />);

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/password/i), "password123");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => {
      expect(authClient.login).toHaveBeenCalledWith({
        email: "test@example.com",
        password: "password123",
      });
    });
  });

  it("shows error message on login failure", async () => {
    vi.mocked(authClient.login).mockRejectedValue(new Error("Invalid credentials"));

    renderWithRouter(<Login />);

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/password/i), "wrongpassword");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => {
      expect(screen.getByText(/invalid credentials/i)).toBeInTheDocument();
    });
  });

  it("redirects to / on success", async () => {
    vi.mocked(authClient.login).mockResolvedValue({
      token: "test-token",
      user: mockUser,
    } as any);
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser } as any);

    render(
      <MemoryRouter initialEntries={["/login"]}>
        <Routes>
          <Route
            path="/login"
            element={
              <AuthProvider>
                <Login />
              </AuthProvider>
            }
          />
          <Route path="/" element={<div>Dashboard</div>} />
        </Routes>
      </MemoryRouter>
    );

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/password/i), "password123");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));

    await waitFor(() => {
      expect(screen.getByText("Dashboard")).toBeInTheDocument();
    });
  });
});

describe("Register page", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("renders email, password, and confirm-password fields", () => {
    renderWithRouter(<Register />);
    expect(screen.getByLabelText(/email/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/^password/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/confirm.*password/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /create.*account/i })).toBeInTheDocument();
  });

  it("has link to login page", () => {
    renderWithRouter(<Register />);
    expect(screen.getByRole("link", { name: /sign in/i })).toHaveAttribute("href", "/login");
  });

  it("shows validation error on password mismatch", async () => {
    renderWithRouter(<Register />);

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/^password/i), "password123");
    await userEvent.type(screen.getByLabelText(/confirm.*password/i), "different123");
    await userEvent.click(screen.getByRole("button", { name: /create.*account/i }));

    expect(screen.getByText(/passwords.*match/i)).toBeInTheDocument();
    expect(authClient.register).not.toHaveBeenCalled();
  });

  it("calls authClient.register on valid form submission", async () => {
    vi.mocked(authClient.register).mockResolvedValue({
      token: "new-token",
      user: mockUser,
    } as any);

    renderWithRouter(<Register />);

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/^password/i), "password123");
    await userEvent.type(screen.getByLabelText(/confirm.*password/i), "password123");
    await userEvent.click(screen.getByRole("button", { name: /create.*account/i }));

    await waitFor(() => {
      expect(authClient.register).toHaveBeenCalledWith({
        email: "test@example.com",
        password: "password123",
      });
    });
  });

  it("redirects to / on successful registration", async () => {
    vi.mocked(authClient.register).mockResolvedValue({
      token: "new-token",
      user: mockUser,
    } as any);
    vi.mocked(authClient.getMe).mockResolvedValue({ user: mockUser } as any);

    render(
      <MemoryRouter initialEntries={["/register"]}>
        <Routes>
          <Route
            path="/register"
            element={
              <AuthProvider>
                <Register />
              </AuthProvider>
            }
          />
          <Route path="/" element={<div>Dashboard</div>} />
        </Routes>
      </MemoryRouter>
    );

    await userEvent.type(screen.getByLabelText(/email/i), "test@example.com");
    await userEvent.type(screen.getByLabelText(/^password/i), "password123");
    await userEvent.type(screen.getByLabelText(/confirm.*password/i), "password123");
    await userEvent.click(screen.getByRole("button", { name: /create.*account/i }));

    await waitFor(() => {
      expect(screen.getByText("Dashboard")).toBeInTheDocument();
    });
  });
});