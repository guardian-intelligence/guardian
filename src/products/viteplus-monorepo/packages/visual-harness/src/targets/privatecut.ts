import type { TargetConfig } from "../config.ts";

export const privatecutTarget: TargetConfig = {
  name: "privatecut",
  criticalSelectors: [".privatecut-title", ".privatecut-hero__lede", ".dropzone"],
  foldTolerancePx: 8,
  waitSelector: "#main",
};
