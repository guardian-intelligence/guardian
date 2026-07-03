import { describe, expect, it } from "vitest";
import { INK_BUCKETS, inkBucketIndex, inkClassRules, inkWrapHtml } from "./ink";

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
    // Every non-space run is wrapped, entities riding inside their word.
    expect(wrapped).toContain(`>It&#39;s</span>`);
    expect(wrapped.match(/letter-ink-\d/g)).toHaveLength(4);
    // Same input, same ink.
    expect(inkWrapHtml(html, "dear-shovon")).toBe(wrapped);
  });

  it("emits one scoped rule per bucket", () => {
    const css = inkClassRules('[data-treatment="letters"]');
    expect(css.match(/\.letter-ink-\d\{/g)).toHaveLength(INK_BUCKETS.length);
    expect(css).toContain(`'wght' ${INK_BUCKETS[0]?.wght}`);
  });
});
