const generatedIgnorePatterns = ["**/routeTree.gen.ts", "**/src/gen/**/*.ts"] as const;

const buildOutputIgnorePatterns = [
  "**/dist/**",
  "**/dist-ssr/**",
  "**/.output/**",
  "**/.tanstack/**",
  "**/.vinxi/**",
  "**/coverage/**",
  "**/test-results/**",
] as const;

const toolIgnorePatterns = [
  "**/node_modules/**",
  ...buildOutputIgnorePatterns,
  ...generatedIgnorePatterns,
] as const;

export default {
  fmt: {
    ignorePatterns: [...toolIgnorePatterns],
  },
  lint: {
    ignorePatterns: [...toolIgnorePatterns],
    options: { typeAware: true, typeCheck: true },
    rules: {
      "no-console": ["error", { allow: ["error"] }],
    },
    overrides: [
      {
        // Tool tier, not shipped code: dev, perf and drill harnesses and the
        // app-local nitro build plugins log to the terminal by design.
        files: [
          "src/products/viteplus-monorepo/apps/*/scripts/**",
          "src/products/viteplus-monorepo/apps/*/perf/**",
          "src/products/viteplus-monorepo/apps/*/e2e/**",
          "src/products/viteplus-monorepo/apps/*/*.mjs",
        ],
        rules: {
          "no-console": "off",
        },
      },
    ],
  },
  test: {
    include: [
      "src/products/viteplus-monorepo/apps/**/*.test.ts",
      "src/products/viteplus-monorepo/apps/**/*.test.tsx",
      "src/products/viteplus-monorepo/packages/**/*.test.ts",
      "src/products/viteplus-monorepo/packages/**/*.test.tsx",
    ],
    exclude: ["**/node_modules/**", ...buildOutputIgnorePatterns],
    environment: "node",
  },
  run: {
    cache: true,
  },
};
