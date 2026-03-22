# admin-console

React/TypeScript SPA for AI Reviewer admin operations.

## Stack

- **Vite** — build tool and dev server
- **React 19** + **TypeScript**
- **TailwindCSS v4** — utility-first styling
- **ConnectRPC** — type-safe API client generated from proto definitions
- **React Router v7** — client-side routing
- **Vitest** — unit testing

## Commands

    npm run dev       # Dev server on :3000 (proxies /api.v1/* to api-server :8090)
    npm run build     # Production build
    npm test          # Run tests
    npm run lint      # Lint

## Architecture

- `src/lib/connect.ts` — ConnectRPC transport and service clients
- `src/components/` — shared UI components
- `src/pages/` — route-level page components
- `src/App.tsx` — router and app shell

## Generated Code

ConnectRPC TypeScript clients are generated from proto files into `gen/ts/`.
Run `make proto` from repo root to regenerate after proto changes.