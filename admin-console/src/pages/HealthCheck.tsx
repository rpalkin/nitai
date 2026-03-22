import { useState, useEffect } from "react";
import { authClient } from "@/lib/connect";
import { ConnectError } from "@connectrpc/connect";

export function HealthCheck() {
  const [status, setStatus] = useState<"checking" | "connected" | "error">("checking");

  useEffect(() => {
    authClient.getMe({})
      .then(() => setStatus("connected"))
      .catch((err: unknown) => {
        if (err instanceof ConnectError) {
          setStatus("connected");
        } else {
          setStatus("error");
        }
      });
  }, []);

  return (
    <div className="space-y-4">
      <h2 className="text-2xl font-bold text-gray-900">API Health Check</h2>
      <div className="p-4 rounded-lg border">
        {status === "checking" && (
          <p className="text-gray-600">Checking connection to API server...</p>
        )}
        {status === "connected" && (
          <p className="text-green-600 font-medium">Connected to API</p>
        )}
        {status === "error" && (
          <p className="text-red-600 font-medium">Cannot reach API</p>
        )}
      </div>
    </div>
  );
}