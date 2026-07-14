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
        // Tool tier, not shipped code: dev/perf harnesses and the app-local
        // nitro build plugins log to the terminal by design.
        files: ["apps/*/scripts/**", "apps/*/perf/**", "apps/*/*.mjs"],
        rules: {
          "no-console": "off",
        },
      },
    ],
  },
  test: {
    include: [
      "apps/**/*.test.ts",
      "apps/**/*.test.tsx",
      "packages/**/*.test.ts",
      "packages/**/*.test.tsx",
    ],
    exclude: ["**/node_modules/**", ...buildOutputIgnorePatterns],
    environment: "node",
  },
  run: {
    cache: true,
  },
};
