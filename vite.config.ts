import { defineConfig } from "vite-plus";

export default defineConfig({
  staged: {
    "*": "vp check --fix",
  },
  fmt: {
    printWidth: 80,
    sortImports: false,
  },
  lint: {
    ignorePatterns: [
      "frontend/scripts/**",
      "frontend/tests/**",
      "frontend/src/**/*.test.ts",
      "frontend/src/**/*.bench.test.ts",
      "packages/ui/src/api/generated/**",
      "packages/ui/src/api/roborev/generated/**",
      "packages/ui/src/**/*.test.ts",
      "packages/ui/src/**/*.bench.test.ts",
    ],
    rules: {
      "eslint/no-unassigned-vars": "off",
      "typescript/no-base-to-string": "off",
      "typescript/no-duplicate-type-constituents": "off",
      "typescript/no-floating-promises": "off",
      "typescript/no-redundant-type-constituents": "off",
      "unicorn/no-useless-fallback-in-spread": "off",
      "unicorn/prefer-string-starts-ends-with": "off",
    },
    options: { typeAware: true, typeCheck: true },
  },
});
