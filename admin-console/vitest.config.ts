import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import path from "path";

const protobufDir = path.resolve(__dirname, "node_modules/@bufbuild/protobuf");

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    globals: true,
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