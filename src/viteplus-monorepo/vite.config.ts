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
        files: ["apps/*/scripts/**"],
        rules: {
          "no-console": "off",
        },
      },
    ],
  },
  test: {
    include: ["apps/**/*.test.ts", "apps/**/*.test.tsx"],
    exclude: ["**/node_modules/**", ...buildOutputIgnorePatterns],
    environment: "node",
  },
  run: {
    cache: true,
  },
};
