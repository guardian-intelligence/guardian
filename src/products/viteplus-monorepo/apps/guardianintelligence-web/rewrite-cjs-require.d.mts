// Typed face of rewrite-cjs-require.mjs for vite.config.ts. vp build resolves
// the .mjs and never reads this .d.mts; it only exists so tsc does not see an
// implicit any on the import.
export function rewriteCjsRequireOnCompiled(nitro: {
  options: { output: { serverDir: string }; rootDir: string };
}): Promise<void>;
