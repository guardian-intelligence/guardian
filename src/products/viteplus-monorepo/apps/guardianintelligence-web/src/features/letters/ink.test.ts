import { describe, expect, it } from "vitest";
import {
  flowClassRules,
  flowOffset,
  flowSlope,
  INK_BUCKETS,
  inkBucketIndex,
  inkClassRules,
  inkWrapHtml,
  tiltBucketIndex,
  tiltClassRules,
} from "./ink";

// The ink is a pure function of (slug, word index). This is load-bearing
// beyond aesthetics: the letters HTML is baked into the image at build time,
// and the digest pin in deployments/company/prod/web.yaml is verified against
// a CI rebuild — nondeterministic ink would make every rebuild a new digest.
describe("ink", () => {
  it("assigns buckets deterministically", () => {
    const a = Array.from({ length: 32 }, (_, i) => inkBucketIndex("dear-shovon", i));
    const b = Array.from({ length: 32 }, (_, i) => inkBucketIndex("dear-shovon", i));
    expect(a).toEqual(b);
    for (const bucket of a) {
      expect(bucket).toBeGreaterThanOrEqual(0);
      expect(bucket).toBeLessThan(INK_BUCKETS.length);
    }
    // Different slugs write with different ink sequences.
    expect(a).not.toEqual(Array.from({ length: 32 }, (_, i) => inkBucketIndex("letters", i)));
  });

  it("wraps only text, preserving tags, entities and whitespace", () => {
    const html = "<p>It&#39;s a <em>quiet</em>  morning.</p>";
    const wrapped = inkWrapHtml(html, "dear-shovon");
    // Tags and inter-word whitespace survive byte-for-byte.
    expect(wrapped.replace(/<\/?span[^>]*>/g, "")).toBe(html);
    // Every non-space run is wrapped, entities riding inside their word, each
    // span wearing one ink class and one flow class.
    expect(wrapped).toContain(`>It&#39;s</span>`);
    expect(wrapped.match(/letter-ink-\d/g)).toHaveLength(4);
    expect(wrapped.match(/letter-flow-\d+/g)).toHaveLength(4);
    expect(wrapped.match(/letter-tilt-\d/g)).toHaveLength(4);
    // Same input, same ink.
    expect(inkWrapHtml(html, "dear-shovon")).toBe(wrapped);
  });

  it("emits one scoped rule per bucket", () => {
    const css = inkClassRules('[data-treatment="letters"]');
    expect(css.match(/\.letter-ink-\d\{/g)).toHaveLength(INK_BUCKETS.length);
    expect(css).toContain(`'wght' ${INK_BUCKETS[0]?.wght}`);
    const flowCss = flowClassRules('[data-treatment="letters"]');
    expect(flowCss.match(/\.letter-flow-\d+\{/g)).toHaveLength(16);
    expect(flowCss).toContain("position:relative;top:");
    expect(flowCss).toContain("padding-right:");
    const tiltCss = tiltClassRules('[data-treatment="letters"]');
    expect(tiltCss.match(/\.letter-tilt-\d\{/g)).toHaveLength(9);
    // The centre bucket is exactly level; words inside links stay flat.
    expect(tiltCss).toContain("rotate(0.000deg)");
    expect(tiltCss).toContain("display:inline;transform:none;");
  });

  // The tilt is the derivative of the flow curve: a word leans toward where
  // the wander is heading — down-forward when the next word sits lower, level
  // where the curve is flat. Derived from the same curve, so they can never
  // disagree.
  it("tilt follows the flow curve's slope", () => {
    for (const slug of ["dear-shovon", "letters"]) {
      for (let i = 0; i < 300; i++) {
        const slope = flowSlope(slug, i);
        const bucket = tiltBucketIndex(slug, i);
        if (slope > 0.06) expect(bucket).toBeGreaterThan(4);
        if (slope < -0.06) expect(bucket).toBeLessThan(4);
        if (Math.abs(slope) < 0.02) expect(bucket).toBe(4);
      }
    }
  });

  // The wander must read as a hand, not a glitch: a continuous curve, so
  // neighbouring words move together — no word may sit visibly sunk between
  // two level neighbours — and the whole thing stays a small fraction of the
  // ruled pitch, deterministic per (slug, index).
  describe("flow curve", () => {
    it("is deterministic and slug-keyed", () => {
      const a = Array.from({ length: 64 }, (_, i) => flowOffset("dear-shovon", i));
      expect(a).toEqual(Array.from({ length: 64 }, (_, i) => flowOffset("dear-shovon", i)));
      expect(a).not.toEqual(Array.from({ length: 64 }, (_, i) => flowOffset("letters", i)));
    });

    it("wanders smoothly within a bounded band", () => {
      for (const slug of ["dear-shovon", "letters", "another-letter"]) {
        let prev = flowOffset(slug, 0);
        for (let i = 1; i < 500; i++) {
          const cur = flowOffset(slug, i);
          expect(Math.abs(cur)).toBeLessThanOrEqual(0.5);
          expect(Math.abs(cur - prev)).toBeLessThan(0.25);
          prev = cur;
        }
      }
    });
  });
});
