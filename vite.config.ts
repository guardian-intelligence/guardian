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
          "src/**/web/scripts/**",
          "src/**/web/perf/**",
          "src/**/web/e2e/**",
          "src/**/web/*.mjs",
        ],
        rules: {
          "no-console": "off",
        },
      },
    ],
  },
  test: {
    include: [
      "src/chunkies/host/ts/**/*.test.ts",
      "src/chunkies/host/ts/**/*.test.tsx",
      "src/chunkies/testkit/ts/**/*.test.ts",
      "src/chunkies/testkit/ts/**/*.test.tsx",
      "src/chunkies/transport/ts/**/*.test.ts",
      "src/chunkies/transport/ts/**/*.test.tsx",
      "src/company/web/**/*.test.ts",
      "src/company/web/**/*.test.tsx",
      "src/games/wake-up-mythra/client/**/*.test.ts",
      "src/games/wake-up-mythra/client/**/*.test.tsx",
      "src/games/wake-up-mythra/web/**/*.test.ts",
      "src/games/wake-up-mythra/web/**/*.test.tsx",
      "src/postflight/checkout/**/*.test.ts",
      "src/postflight/checkout/**/*.test.tsx",
      "src/privatecut/web/**/*.test.ts",
      "src/privatecut/web/**/*.test.tsx",
      "src/shared/ts/brand/**/*.test.ts",
      "src/shared/ts/brand/**/*.test.tsx",
      "src/shared/ts/canary-journeys/**/*.test.ts",
      "src/shared/ts/canary-journeys/**/*.test.tsx",
      "src/shared/ts/telemetry/**/*.test.ts",
      "src/shared/ts/telemetry/**/*.test.tsx",
      "src/shared/ts/visual-harness/**/*.test.ts",
      "src/shared/ts/visual-harness/**/*.test.tsx",
    ],
    exclude: ["**/node_modules/**", ...buildOutputIgnorePatterns],
    environment: "node",
  },
  run: {
    cache: true,
  },
};
