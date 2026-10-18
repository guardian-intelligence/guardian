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
    // LICENSE.md is the canonical Apache-2.0 text, kept byte-for-byte; talm
    // templates are Go templates that only render to YAML.
    ignorePatterns: [...toolIgnorePatterns, "LICENSE.md", "src/infrastructure/talm/templates/**"],
  },
  lint: {
    ignorePatterns: [...toolIgnorePatterns],
    options: { typeAware: true, typeCheck: true },
    rules: {
      "no-console": ["error", { allow: ["error"] }],
    },
    overrides: [
      {
        // Tool tier, not shipped code: dev, perf, regression and drill
        // harnesses, k6 load scripts, and the app-local nitro build plugins
        // log to the terminal by design.
        files: [
          "src/**/web/scripts/**",
          "src/**/web/perf/**",
          "src/**/web/e2e/**",
          "src/**/web/regression/**",
          "src/infrastructure/load/**",
          "src/**/web/*.mjs",
          "src/anveio/cal/scripts/**",
        ],
        rules: {
          "no-console": "off",
        },
      },
      {
        // The Anveio deck is laid out entirely by CSS so its server-rendered
        // HTML is already final: no browser globals or state/effect hooks,
        // whose results the server can't know and would repaint on hydration.
        files: ["src/anveio/web/src/**"],
        rules: {
          "no-restricted-globals": [
            "error",
            ...["window", "document", "innerWidth", "innerHeight", "matchMedia", "screen"].map(
              (name) => ({
                name,
                message: "The deck must lay out identically on the server; use CSS.",
              }),
            ),
          ],
          "no-restricted-imports": [
            "error",
            {
              paths: [
                {
                  name: "react",
                  importNames: [
                    "useState",
                    "useReducer",
                    "useEffect",
                    "useLayoutEffect",
                    "useSyncExternalStore",
                  ],
                  message: "The deck must lay out identically on the server; use CSS.",
                },
              ],
            },
          ],
        },
      },
      {
        // Keyboard navigation: behaviour only, never layout.
        files: ["src/anveio/web/src/deck/keyboardNav.ts"],
        rules: {
          "no-restricted-globals": "off",
          "no-restricted-imports": "off",
        },
      },
    ],
  },
  test: {
    include: [
      "src/anveio/web/**/*.test.ts",
      "src/anveio/web/**/*.test.tsx",
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
