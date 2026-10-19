// Holds the home page's entrance until its first-paint fonts have loaded,
// so the hero never paints in a fallback face and then swaps.
//
// The page renders this inline, right after its type CSS. It pauses every
// .rise animation at its first frame (invisible, per home.css), waits for
// the fonts (and any Google stylesheet still downloading), then lets the
// staggered entrance play. Without JavaScript nothing is paused, and a
// slow network gives up after 2.5 s rather than hide the page.
export function fontGateScript(fonts: readonly string[]): string {
  return `(() => {
  const hold = document.createElement("style");
  hold.textContent = ".hp .rise { animation-play-state: paused !important; }";
  document.head.append(hold);
  const go = () => hold.remove();
  setTimeout(go, 2500);
  const sheets = [...document.querySelectorAll('link[rel="stylesheet"][href^="https://fonts.googleapis.com/"]')]
    .filter((l) => !l.sheet)
    .map((l) => new Promise((r) => {
      l.addEventListener("load", r);
      l.addEventListener("error", r);
    }));
  Promise.all(sheets)
    .then(() => Promise.all(${JSON.stringify(fonts)}.map((f) => document.fonts.load(f))))
    .then(go, go);
})();`;
}
