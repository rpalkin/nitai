# admin-console

React/TypeScript SPA for AI Reviewer admin operations.

## Stack

- **Vite** — build tool and dev server
- **React 19** + **TypeScript**
- **TailwindCSS v4** — utility-first styling with custom dark theme
- **ConnectRPC** — type-safe API client generated from proto definitions
- **React Router v7** — client-side routing with protected routes
- **Vitest** — unit testing

## Commands

    npm run dev       # Dev server on :3000 (proxies /api.v1/* to api-server :8090)
    npm run build     # Production build
    npm test          # Run tests
    npm run lint      # Lint

## Architecture

- `src/lib/connect.ts` — ConnectRPC transport and service clients with Bearer token interceptor
- `src/lib/auth.tsx` — AuthContext provider, useAuth hook, JWT localStorage storage
- `src/components/` — shared UI components
  - `Layout.tsx` — main app layout with dark theme nav (authenticated pages)
  - `AuthLayout.tsx` — split-screen layout for auth pages (login/register)
  - `ProtectedRoute.tsx` — route guard that redirects unauthenticated users to /login
- `src/pages/` — route-level page components
  - `Login.tsx` — login form with dark card UI
  - `Register.tsx` — registration form with password strength indicator
  - `HealthCheck.tsx` — health check page (protected)
  - `Providers.tsx` — provider management page (list, create, delete, webhook secret display)
- `src/App.tsx` — router and app shell with AuthProvider wrapper
- `src/index.css` — Tailwind v4 theme with custom colors (surface-*, accent, text-*)

## Authentication Flow

1. User visits protected route → `ProtectedRoute` checks `useAuth().isAuthenticated`
2. If unauthenticated, redirects to `/login`
3. Login/Register calls `authClient.login`/`authClient.register`
4. On success, JWT stored in `localStorage("auth_token")` and user in AuthContext
5. `AuthProvider` rehydrates user on mount by calling `authClient.getMe()`
6. All ConnectRPC requests include `Authorization: Bearer <token>` header via interceptor

## Generated Code

ConnectRPC TypeScript clients are generated from proto files into `gen/ts/`.
Run `make proto` from repo root to regenerate after proto changes.