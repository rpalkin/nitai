import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Routes, Route } from "react-router-dom";
import { Layout } from "../components/Layout";
import { HealthCheck } from "../pages/HealthCheck";

describe("App", () => {
  it("renders the app shell with navigation", () => {
    render(
      <MemoryRouter initialEntries={["/health"]}>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/health" element={<HealthCheck />} />
          </Route>
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByText("AI Reviewer")).toBeInTheDocument();
  });

  it("renders the health check page", () => {
    render(
      <MemoryRouter initialEntries={["/health"]}>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/health" element={<HealthCheck />} />
          </Route>
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByText("API Health Check")).toBeInTheDocument();
  });
});