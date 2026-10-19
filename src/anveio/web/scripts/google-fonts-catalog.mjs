// Snapshots the Google Fonts catalog for the /design type editor's font
// picker: every Latin family, most popular first, with its category, the
// weights it has upright and italic, and its variation axes.
// Run from the workspace root: vp run @guardian/anveio-web#google-fonts-catalog.
import { writeFileSync } from "node:fs";

const CATEGORIES = ["Sans Serif", "Serif", "Display", "Handwriting", "Monospace"];

const response = await fetch("https://fonts.google.com/metadata/fonts");
if (!response.ok) throw new Error(`Google Fonts metadata: HTTP ${response.status}`);
const { familyMetadataList } = await response.json();

const catalog = familyMetadataList
  .filter((f) => f.subsets?.includes("latin"))
  .sort((a, b) => a.popularity - b.popularity)
  .map((f) => {
    const styles = Object.keys(f.fonts);
    const weights = (italic) =>
      styles
        .filter((s) => s.endsWith("i") === italic)
        .map((s) => Number.parseInt(s, 10))
        .sort((a, b) => a - b);
    const category = CATEGORIES.indexOf(f.category);
    if (category < 0) throw new Error(`${f.family}: unknown category ${f.category}`);
    const axes = (f.axes ?? []).map((a) => [a.tag, a.min, a.max]);
    return [f.family, category, weights(false), weights(true), axes];
  });

const out = new URL("../src/design/google-fonts.json", import.meta.url);
writeFileSync(out, `${JSON.stringify(catalog)}\n`);
