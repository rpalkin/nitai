import { Outlet } from "react-router-dom";

export function AuthLayout() {
  return (
    <div className="min-h-screen bg-surface-900 flex">
      <div className="hidden md:flex md:w-1/2 lg:w-3/5 flex-col justify-center items-center bg-surface-800 relative overflow-hidden">
        <div
          className="absolute inset-0 opacity-20"
          style={{
            backgroundImage:
              "radial-gradient(circle at 1px 1px, rgba(255,255,255,0.15) 1px, transparent 0)",
            backgroundSize: "24px 24px",
          }}
        />
        <div className="absolute inset-0 bg-gradient-to-b from-transparent via-transparent to-accent-glow pointer-events-none" />
        <div className="relative z-10 text-center px-8">
          <div className="flex items-center justify-center gap-3 mb-6">
            <div className="w-12 h-12 rounded-xl bg-accent flex items-center justify-center">
              <span className="text-white text-2xl font-bold">AI</span>
            </div>
            <h1 className="text-4xl font-bold text-text-primary">AI Reviewer</h1>
          </div>
          <p className="text-text-secondary text-lg max-w-md">
            AI-powered code reviews for your team
          </p>
        </div>
      </div>
      <div className="w-full md:w-1/2 lg:w-2/5 flex items-center justify-center p-8">
        <div className="w-full max-w-md">
          <div className="bg-surface-700 border border-border rounded-2xl p-8 shadow-lg shadow-accent-glow">
            <Outlet />
          </div>
        </div>
      </div>
    </div>
  );
}