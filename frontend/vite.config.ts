import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 前端开发模式：vite dev (5173) 代理 /api（含 WS）到本机 gengar-server (8080)，
// 免 CORS 与 cookie 域问题；生产构建由 gengar-server go:embed 内嵌托管。
export default defineConfig({
  plugins: [react()],
  server: {
    strictPort: true,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", ws: true },
    },
  },
});
