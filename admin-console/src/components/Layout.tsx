import { useState, useEffect } from "react";
import { Outlet, NavLink, useLocation, useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { providerClient } from "../lib/connect";
import type { Provider } from "@gen/api/v1/provider_pb";

export function Layout() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [providers, setProviders] = useState<Provider[]>([]);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const fetchProviders = async () => {
      try {
        const response = await providerClient.listProviders({});
        setProviders(response.providers);
      } catch (err) {
        console.error("Failed to load providers:", err);
      } finally {
        setIsLoading(false);
      }
    };
    fetchProviders();
  }, []);

  const handleLogout = () => {
    logout();
    navigate("/login");
  };

  const getCurrentProviderId = () => {
    const match = location.pathname.match(/\/providers\/([^\/]+)\/repos/);
    return match ? match[1] : null;
  };

  return (
    <div className="min-h-screen bg-surface-900 flex">
      <aside className="w-64 bg-surface-800 border-r border-border flex flex-col fixed left-0 top-0 bottom-0">
        <div className="flex-1 overflow-y-auto">
          <div className="px-6 py-4 border-b border-border">
            <div className="h-0.5 bg-accent -mx-6 -mt-4 mb-4" />
            <h1 className="text-xl font-semibold text-text-primary">AI Reviewer</h1>
          </div>
          <nav className="px-2 py-2">
            {isLoading ? (
              <div className="px-3 py-2 text-sm text-text-muted">Loading providers...</div>
            ) : providers.length === 0 ? (
              <div className="px-3 py-2 text-sm text-text-muted">No providers configured</div>
            ) : (
              <div className="space-y-0.5">
                {providers.map((provider) => {
                  const currentProviderId = getCurrentProviderId();
                  const isActive = currentProviderId === provider.id;
                  
                  return (
                  <div key={provider.id}>
                    <NavLink
                      to={`/providers/${provider.id}`}
                      className={({ isActive }) =>
                        `flex items-center gap-2 px-3 py-1.5 text-sm rounded transition-colors ${
                          isActive
                            ? "text-accent bg-surface-700"
                            : "text-text-secondary hover:text-text-primary hover:bg-surface-700"
                        }`
                      }
                    >
                      <span className="truncate">{provider.name}</span>
                    </NavLink>
                    <NavLink
                      to={`/providers/${provider.id}/repos`}
                      className={`flex items-center gap-2 pl-6 pr-3 py-1.5 text-sm rounded transition-colors ${
                        isActive
                          ? "text-accent bg-surface-700"
                          : "text-text-muted hover:text-text-primary hover:bg-surface-700"
                      }`}
                    >
                      <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M3 7v10a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-6l-2-2H5a2 2 0 00-2 2z" />
                      </svg>
                      <span>Repos</span>
                    </NavLink>
                  </div>
                );
                })}
              </div>
            )}

            <div className="my-3 border-t border-border" />

            <NavLink
              to="/providers"
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2 text-sm font-medium rounded transition-colors ${
                  isActive
                    ? "text-accent bg-surface-700"
                    : "text-text-secondary hover:text-text-primary hover:bg-surface-700"
                }`
              }
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
              </svg>
              Providers
            </NavLink>
            <NavLink
              to="/instructions"
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2 text-sm font-medium rounded transition-colors ${
                  isActive
                    ? "text-accent bg-surface-700"
                    : "text-text-secondary hover:text-text-primary hover:bg-surface-700"
                }`
              }
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
              </svg>
              Instructions
            </NavLink>
            <NavLink
              to="/health"
              className={({ isActive }) =>
                `flex items-center gap-3 px-3 py-2 text-sm font-medium rounded transition-colors ${
                  isActive
                    ? "text-accent bg-surface-700"
                    : "text-text-secondary hover:text-text-primary hover:bg-surface-700"
                }`
              }
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4.318 6.318a4.5 4.5 0 000 6.364L12 20.364l7.682-7.682a4.5 4.5 0 00-6.364-6.364L12 7.636l-1.318-1.318a4.5 4.5 0 00-6.364 0z" />
              </svg>
              Health Check
            </NavLink>
          </nav>
        </div>
        {user && (
          <div className="px-4 py-4 border-t border-border">
            <div className="px-3 py-2 mb-2">
              <p className="text-sm text-text-secondary truncate">{user.email}</p>
            </div>
            <button
              onClick={handleLogout}
              className="flex items-center gap-3 w-full px-3 py-2 text-sm text-text-secondary hover:text-text-primary transition-colors rounded hover:bg-surface-700"
            >
              <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1" />
              </svg>
              Sign out
            </button>
          </div>
        )}
      </aside>
      <main className="flex-1 ml-64">
        <div className="max-w-7xl mx-auto px-6 py-8">
          <Outlet />
        </div>
      </main>
    </div>
  );
}