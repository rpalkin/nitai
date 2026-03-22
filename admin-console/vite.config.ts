import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "path";

const protobufDir = path.resolve(__dirname, "node_modules/@bufbuild/protobuf");

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 3000,
    proxy: {
      "/api.v1": {
        target: "http://localhost:8090",
        changeOrigin: true,
      },
      "/webhooks": {
        target: "http://localhost:8090",
        changeOrigin: true,
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
      "@gen": path.resolve(__dirname, "../gen/ts"),
      "@bufbuild/protobuf/codegenv2": path.join(protobufDir, "dist/esm/codegenv2/index.js"),
      "@bufbuild/protobuf/wkt": path.join(protobufDir, "dist/esm/wkt/index.js"),
      "@bufbuild/protobuf/wire": path.join(protobufDir, "dist/esm/wire/index.js"),
      "@bufbuild/protobuf": path.join(protobufDir, "dist/esm/index.js"),
    },
  },
});