import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { AuthProvider } from "@/lib/auth";
import { AuthLayout } from "@/components/AuthLayout";
import { ProtectedRoute } from "@/components/ProtectedRoute";
import { Layout } from "@/components/Layout";
import { HealthCheck } from "@/pages/HealthCheck";
import { Login } from "@/pages/Login";
import { Register } from "@/pages/Register";
import { Providers } from "@/pages/Providers";
import { Provider } from "@/pages/Provider";
import { Repos } from "@/pages/Repos";
import { Instructions } from "@/pages/Instructions";

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <Routes>
          <Route element={<AuthLayout />}>
            <Route path="/login" element={<Login />} />
            <Route path="/register" element={<Register />} />
          </Route>

          <Route element={<ProtectedRoute />}>
            <Route element={<Layout />}>
              <Route path="/" element={<Navigate to="/providers" replace />} />
              <Route path="/health" element={<HealthCheck />} />
              <Route path="/providers" element={<Providers />} />
              <Route path="/providers/:providerId" element={<Provider />} />
              <Route path="/providers/:providerId/repos" element={<Repos />} />
              <Route path="/instructions" element={<Instructions />} />
            </Route>
          </Route>
        </Routes>
      </AuthProvider>
    </BrowserRouter>
  );
}