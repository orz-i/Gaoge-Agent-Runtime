import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    coverage: {
      provider: "v8",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.test.ts", "src/types.ts"],
      reporter: ["text", "json-summary", "html"],
      thresholds: {
        lines: 80,
        functions: 90,
        branches: 60,
        statements: 75,
      },
    },
  },
});
