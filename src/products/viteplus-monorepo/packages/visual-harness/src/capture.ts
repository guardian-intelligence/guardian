import { createHash } from "node:crypto";
import { chromium, firefox, webkit, type Browser } from "@playwright/test";
import { installDeterminism, seekTo } from "./determinism.ts";
import type { FormFactor } from "./form-factors.ts";

export type EngineName = "chromium" | "firefox" | "webkit";

const ENGINES = { chromium, firefox, webkit } as const;

const CHROMIUM_ARGS = ["--disable-dev-shm-usage", "--disable-gpu", "--force-color-profile=srgb"];

export interface CaptureRequest {
  url: string;
  formFactor: FormFactor;
  seekMs: number;
  seed: number;
  reducedMotion: "reduce" | "no-preference";
  fullPage?: boolean | undefined;
  clipSelector?: string | undefined;
  waitSelector?: string | undefined;
}

export interface CaptureResult {
  png: Buffer;
  sha256: string;
  /** Physical pixels, decoded from the PNG header. */
  width: number;
  height: number;
  cssAnimationsFrozen: number;
}

export interface CaptureSession {
  readonly engine: EngineName;
  capture(request: CaptureRequest): Promise<CaptureResult>;
  close(): Promise<void>;
}

export async function createCaptureSession(
  engine: EngineName = "chromium",
): Promise<CaptureSession> {
  const browser: Browser = await ENGINES[engine].launch({
    args: engine === "chromium" ? CHROMIUM_ARGS : [],
  });

  return {
    engine,

    async capture(request: CaptureRequest): Promise<CaptureResult> {
      const ff = request.formFactor;
      const context = await browser.newContext({
        viewport: { width: ff.width, height: ff.height },
        deviceScaleFactor: ff.deviceScaleFactor,
        reducedMotion: request.reducedMotion,
        ...(ff.userAgent ? { userAgent: ff.userAgent } : {}),
        ...(ff.hasTouch ? { hasTouch: true } : {}),
        // Firefox rejects the isMobile context option outright.
        ...(ff.isMobile && engine !== "firefox" ? { isMobile: true } : {}),
      });
      try {
        const page = await context.newPage();
        await installDeterminism(page, { seed: request.seed });
        await page.goto(request.url, { waitUntil: "load" });
        const cssAnimationsFrozen = await seekTo(page, request.seekMs, {
          waitSelector: request.waitSelector,
        });
        const shooter = request.clipSelector ? page.locator(request.clipSelector) : page;
        const png = await shooter.screenshot({
          // "css" scale would downsample to CSS pixels and forfeit the
          // deviceScaleFactor-carried resolution; "allow" preserves the
          // deterministic mid-animation freeze from seekTo.
          scale: "device",
          animations: "allow",
          ...(request.clipSelector ? {} : { fullPage: request.fullPage ?? false }),
        });
        return {
          png,
          sha256: createHash("sha256").update(png).digest("hex"),
          width: png.readUInt32BE(16),
          height: png.readUInt32BE(20),
          cssAnimationsFrozen,
        };
      } finally {
        await context.close();
      }
    },

    async close(): Promise<void> {
      await browser.close();
    },
  };
}
