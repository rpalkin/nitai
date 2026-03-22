import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { Layout } from "@/components/Layout";
import { HealthCheck } from "@/pages/HealthCheck";

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Layout />}>
          <Route path="/" element={<Navigate to="/health" replace />} />
          <Route path="/health" element={<HealthCheck />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}