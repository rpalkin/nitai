import { createContext, useContext, useState, useEffect, type ReactNode } from "react";
import { authClient } from "./connect";
import { TOKEN_KEY } from "./auth-constants";
import type { User } from "@gen/api/v1/auth_pb";

interface AuthState {
  user: User | null;
  token: string | null;
  isAuthenticated: boolean;
  isLoading: boolean;
}

interface AuthContextValue extends AuthState {
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue | null>(null);

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return context;
}

interface AuthProviderProps {
  children: ReactNode;
}

export function AuthProvider({ children }: AuthProviderProps) {
  const storedToken = typeof window !== "undefined" ? localStorage.getItem(TOKEN_KEY) : null;
  const [user, setUser] = useState<User | null>(null);
  const [token, setToken] = useState<string | null>(() => storedToken);
  const [isLoading, setIsLoading] = useState(() => storedToken !== null);

  const isAuthenticated = token !== null && user !== null;

  useEffect(() => {
    if (!storedToken) {
      return;
    }

    let mounted = true;

    authClient
      .getMe({})
      .then((response) => {
        if (mounted) {
          setUser(response.user ?? null);
        }
      })
      .catch(() => {
        if (mounted) {
          localStorage.removeItem(TOKEN_KEY);
          setToken(null);
          setUser(null);
        }
      })
      .finally(() => {
        if (mounted) {
          setIsLoading(false);
        }
      });

    return () => {
      mounted = false;
    };
  }, [storedToken]);

  const login = async (email: string, password: string) => {
    const response = await authClient.login({ email, password });
    if (response.token) {
      localStorage.setItem(TOKEN_KEY, response.token);
      setToken(response.token);
      setUser(response.user ?? null);
      setIsLoading(false);
    }
  };

  const register = async (email: string, password: string) => {
    const response = await authClient.register({ email, password });
    if (response.token) {
      localStorage.setItem(TOKEN_KEY, response.token);
      setToken(response.token);
      setUser(response.user ?? null);
      setIsLoading(false);
    }
  };

  const logout = () => {
    localStorage.removeItem(TOKEN_KEY);
    setToken(null);
    setUser(null);
  };

  const value: AuthContextValue = {
    user,
    token,
    isAuthenticated,
    isLoading,
    login,
    register,
    logout,
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}