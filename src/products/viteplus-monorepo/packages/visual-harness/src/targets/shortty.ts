import type { TargetConfig } from "../config.ts";

export const shorttyTarget: TargetConfig = {
  name: "shortty",
  criticalSelectors: [".shortty-title", ".shortty-hero__lede", ".dropzone"],
  foldTolerancePx: 8,
  waitSelector: "#main",
};
