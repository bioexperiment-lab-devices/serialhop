import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { resolve } from "node:path";

export default defineConfig(({ mode }) => ({
  plugins: [react()],
  build: {
    outDir: mode === "preview" ? "dist-preview" : "dist",
    emptyOutDir: true,
    rollupOptions: {
      input: mode === "preview"
        ? { preview: resolve(__dirname, "preview.html") }
        : { main: resolve(__dirname, "index.html") },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    exclude: ["playwright/**", "node_modules/**"],
  },
}));
