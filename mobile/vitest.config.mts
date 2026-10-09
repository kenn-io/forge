import { defineConfig } from "vite-plus/test/config";

export default defineConfig({
  test: {
    environment: "node",
    include: ["App.test.tsx"],
  },
});
