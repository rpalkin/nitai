import { Outlet, NavLink } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { useNavigate } from "react-router-dom";

export function Layout() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  const handleLogout = () => {
    logout();
    navigate("/login");
  };

  return (
    <div className="min-h-screen bg-surface-900">
      <nav className="bg-surface-800 border-b border-border relative">
        <div className="h-0.5 bg-accent absolute top-0 left-0 right-0" />
        <div className="px-6 py-4 flex items-center justify-between">
          <div className="flex items-center gap-6">
            <h1 className="text-xl font-semibold text-text-primary">AI Reviewer</h1>
            <NavLink
              to="/providers"
              className={({ isActive }) =>
                `px-3 py-1.5 text-sm font-medium rounded transition-colors ${
                  isActive
                    ? "text-accent bg-surface-700"
                    : "text-text-secondary hover:text-text-primary"
                }`
              }
            >
              Providers
            </NavLink>
          </div>
          {user && (
            <div className="flex items-center gap-4">
              <span className="text-text-secondary text-sm">{user.email}</span>
              <button
                onClick={handleLogout}
                className="px-3 py-1.5 text-sm text-text-secondary hover:text-text-primary transition-colors"
              >
                Sign out
              </button>
            </div>
          )}
        </div>
      </nav>
      <main className="max-w-7xl mx-auto px-6 py-8">
        <Outlet />
      </main>
    </div>
  );
}