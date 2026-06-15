import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import matter from "gray-matter";
import { marked } from "marked";

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, "..");
const lettersDir = resolve(root, "src/content/letters");
const newsDir = resolve(root, "src/content/news");
const siteUrl = "https://guardianintelligence.org";

const css = `
:root{color-scheme:light;font-family:ui-serif,Georgia,Cambria,"Times New Roman",serif;--ink:#111827;--muted:#4b5563;--line:#d1d5db;--paper:#fbfbf8;--accent:#075985;--accent-2:#166534}
*{box-sizing:border-box}
html{background:var(--paper);color:var(--ink)}
body{margin:0;min-height:100vh;background:linear-gradient(180deg,#fff 0,#fbfbf8 42rem);color:var(--ink)}
a{color:inherit;text-decoration-thickness:1px;text-underline-offset:.22em}
.site{min-height:100vh;display:flex;flex-direction:column}
.mast{border-bottom:1px solid var(--line);background:rgba(255,255,255,.86)}
.mast-inner{max-width:74rem;margin:0 auto;padding:1rem clamp(1rem,4vw,2rem);display:flex;gap:1rem;align-items:center;justify-content:space-between}
.brand{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.78rem;font-weight:650;text-transform:uppercase;letter-spacing:0;color:var(--ink);text-decoration:none}
.nav{display:flex;gap:1rem;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.9rem;color:var(--muted)}
.wrap{width:min(100%,74rem);margin:0 auto;padding:clamp(2.5rem,8vw,6rem) clamp(1rem,4vw,2rem)}
.hero{max-width:55rem}
.eyebrow{margin:0 0 .8rem;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.78rem;font-weight:650;text-transform:uppercase;color:var(--accent)}
h1{margin:0;font-size:clamp(2.5rem,8vw,5.8rem);line-height:.96;font-weight:650;letter-spacing:0}
.lede{max-width:43rem;margin:1.2rem 0 0;color:var(--muted);font-size:clamp(1.1rem,2.4vw,1.45rem);line-height:1.55}
.actions{margin-top:2rem;display:flex;gap:.9rem;flex-wrap:wrap}
.button{display:inline-flex;align-items:center;min-height:2.75rem;padding:.72rem 1rem;border:1px solid var(--ink);font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.92rem;text-decoration:none;background:var(--ink);color:#fff}
.button.secondary{background:transparent;color:var(--ink);border-color:var(--line)}
.section-title{font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.82rem;font-weight:700;text-transform:uppercase;color:var(--accent-2)}
.content-list{list-style:none;margin:2rem 0 0;padding:0;border-top:1px solid var(--line)}
.content-row{border-bottom:1px solid var(--line)}
.content-link{display:block;padding:1.35rem 0 1.45rem;text-decoration:none}
.date{margin:0 0 .4rem;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.82rem;color:var(--muted)}
.item-title{margin:0;font-size:clamp(1.55rem,4vw,2.45rem);line-height:1.08;font-weight:620;letter-spacing:0}
.summary{max-width:48rem;margin:.7rem 0 0;color:var(--muted);font-size:1.02rem;line-height:1.6}
.news-bulletin{display:flex;position:relative;min-height:clamp(17rem,38vw,35rem);align-items:center;justify-content:center;padding:clamp(1.5rem,5vw,4.5rem);background:#f97316;color:#111;text-decoration:none}
.news-bulletin h2{max-width:18ch;margin:0;text-align:center;font-size:clamp(2rem,7vw,6.5rem);line-height:.98;font-weight:520;letter-spacing:0}
.news-bulletin .date{position:absolute;left:1.4rem;top:1.4rem;margin:0;color:rgba(17,17,17,.72);font-weight:650;text-transform:uppercase}
.news-bulletin .read{position:absolute;right:1.4rem;bottom:1.4rem;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.78rem;font-weight:700;text-transform:uppercase;color:rgba(17,17,17,.72)}
.news-meta{display:grid;grid-template-columns:minmax(0,1.2fr) minmax(0,1fr);gap:1rem 3rem;margin-top:1.5rem;border-top:1px solid var(--line);padding-top:1.25rem}
.news-meta h2{margin:0;font-size:clamp(1.4rem,3vw,1.9rem);line-height:1.1;font-weight:560;letter-spacing:0}
.news-meta p{margin:0;color:var(--muted);font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:1rem;line-height:1.55}
.news-hero{background:#f97316;color:#111;border-bottom:1px solid rgba(17,17,17,.16)}
.news-hero-inner{width:min(100%,74rem);min-height:clamp(22rem,36vw,32rem);margin:0 auto;padding:clamp(3.5rem,8vw,6rem) clamp(1rem,4vw,2rem);display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1.2rem;text-align:center}
.news-hero .section-title{color:rgba(17,17,17,.72);margin:0}
.news-hero h1{max-width:22ch;font-size:clamp(2.5rem,6.4vw,5.5rem);line-height:1}
.news-hero .lede{margin:0;max-width:56ch;color:rgba(17,17,17,.72);font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:clamp(1rem,1.6vw,1.25rem);line-height:1.5}
.byline{display:flex;flex-direction:column;gap:.15rem;margin:2.4rem 0 0;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--muted)}
.byline strong{color:var(--ink);font-weight:600}
.reading{max-width:46rem}
.back{display:inline-flex;margin-bottom:2rem;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.88rem;color:var(--muted)}
article .date{margin-bottom:1rem}
.prose{margin-top:2rem;font-size:clamp(1.08rem,2vw,1.25rem);line-height:1.78}
.prose p{margin:0 0 1.25rem}
.prose a{color:var(--accent)}
.foot{margin-top:auto;border-top:1px solid var(--line);padding:1rem clamp(1rem,4vw,2rem);font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-size:.82rem;color:var(--muted);text-align:center}
@media(max-width:42rem){.mast-inner{align-items:flex-start;flex-direction:column}.nav{width:100%;justify-content:flex-start}.wrap{padding-top:3rem}h1{font-size:clamp(2.25rem,13vw,4rem)}.news-meta{grid-template-columns:1fr}.news-bulletin .date,.news-bulletin .read{position:static}.news-bulletin{flex-direction:column;gap:1.2rem;text-align:center}.news-bulletin h2{font-size:clamp(2.1rem,13vw,4rem)}}
`.trim();

marked.use({ gfm: true, breaks: false });

function escapeHtml(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function escapeXml(value) {
  return escapeHtml(value).replace(/'/g, "&apos;");
}

function textOf(html) {
  return html
    .replace(/<[^>]+>/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function requireString(data, field, file) {
  const value = data[field];
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  if (typeof value === "string" && value.trim() !== "") return value.trim();
  throw new Error(`${file}: frontmatter ${field} must be a non-empty string`);
}

function optionalString(data, field) {
  const value = data[field];
  if (value === undefined || value === null) return "";
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  if (typeof value === "string") return value.trim();
  throw new Error(`frontmatter ${field} must be a string`);
}

function loadLetters() {
  return readdirSync(lettersDir)
    .filter((name) => name.endsWith(".md"))
    .map((name) => {
      const file = join(lettersDir, name);
      const parsed = matter(readFileSync(file, "utf8"));
      const html = marked.parse(parsed.content);
      const letter = {
        slug: requireString(parsed.data, "slug", name),
        title: requireString(parsed.data, "title", name),
        publishedAt: requireString(parsed.data, "publishedAt", name),
        summary: requireString(parsed.data, "summary", name),
        html,
      };
      if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(letter.slug)) {
        throw new Error(`${name}: slug must be lowercase kebab-case`);
      }
      if (!/^\d{4}-\d{2}-\d{2}$/.test(letter.publishedAt)) {
        throw new Error(`${name}: publishedAt must be YYYY-MM-DD`);
      }
      return letter;
    })
    .sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : -1));
}

function loadNews() {
  return readdirSync(newsDir)
    .filter((name) => name.endsWith(".md"))
    .map((name) => {
      const file = join(newsDir, name);
      const parsed = matter(readFileSync(file, "utf8"));
      const html = marked.parse(parsed.content);
      const item = {
        slug: requireString(parsed.data, "slug", name),
        kicker: requireString(parsed.data, "kicker", name),
        category: requireString(parsed.data, "category", name),
        title: requireString(parsed.data, "title", name),
        deck: requireString(parsed.data, "deck", name),
        date: requireString(parsed.data, "date", name),
        publishedAt: requireString(parsed.data, "publishedAt", name),
        authorName: requireString(parsed.data, "authorName", name),
        authorRole: requireString(parsed.data, "authorRole", name),
        ctaLabel: optionalString(parsed.data, "ctaLabel"),
        ctaHref: optionalString(parsed.data, "ctaHref"),
        html,
      };
      if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(item.slug)) {
        throw new Error(`${name}: slug must be lowercase kebab-case`);
      }
      if (!/^\d{4}-\d{2}-\d{2}$/.test(item.publishedAt)) {
        throw new Error(`${name}: publishedAt must be YYYY-MM-DD`);
      }
      return item;
    })
    .sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : -1));
}

function newsHref(item) {
  return item.ctaHref || `/news/${item.slug}`;
}

function head({ title, description, path, ogPath }) {
  const url = `${siteUrl}${path}`;
  const image = `${siteUrl}${ogPath}`;
  return `<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="${escapeHtml(description)}">
<link rel="canonical" href="${url}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="Guardian Intelligence">
<meta property="og:title" content="${escapeHtml(title)}">
<meta property="og:description" content="${escapeHtml(description)}">
<meta property="og:url" content="${url}">
<meta property="og:image" content="${image}">
<meta property="og:image:type" content="image/svg+xml">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="${escapeHtml(title)}">
<meta name="twitter:description" content="${escapeHtml(description)}">
<meta name="twitter:image" content="${image}">
<title>${escapeHtml(title)}</title>
<style>${css}</style>`;
}

function layout({ title, description, path, ogPath, body }) {
  return `<!doctype html>
<html lang="en">
<head>
${head({ title, description, path, ogPath })}
</head>
<body>
<div class="site">
<header class="mast"><div class="mast-inner"><a class="brand" href="/">Guardian Intelligence</a><nav class="nav" aria-label="Primary"><a href="/letters">Letters</a><a href="/news">News</a></nav></div></header>
${body}
<footer class="foot">Guardian Intelligence Inc.</footer>
</div>
</body>
</html>
`;
}

function home() {
  return layout({
    title: "Guardian Intelligence",
    description: "Free open-source self-hostable cloud for agent-era software companies.",
    path: "/",
    ogPath: "/og/home.svg",
    body: `<main class="wrap"><section class="hero"><p class="eyebrow">BYOC on-prem</p><h1>Guardian Intelligence</h1><p class="lede">A free open-source self-hostable cloud for turning bare metal into a software company.</p><div class="actions"><a class="button" href="/letters">Read letters</a><a class="button secondary" href="/news">News</a><a class="button secondary" href="https://github.com/guardian-intelligence/guardian">GitHub</a></div></section></main>`,
  });
}

function lettersIndex(letters) {
  return layout({
    title: "Letters - Guardian Intelligence",
    description: "Long-form letters from Guardian Intelligence.",
    path: "/letters",
    ogPath: "/og/letters.svg",
    body: `<main class="wrap"><p class="section-title">Letters</p><h1>Letters</h1><p class="lede">Long-form from Guardian Intelligence. Published when we have something to say, not on a calendar.</p><ul class="content-list">${letters
      .map(
        (letter) =>
          `<li class="content-row"><a class="content-link" href="/letters/${letter.slug}"><p class="date">${escapeHtml(letter.publishedAt)}</p><h2 class="item-title">${escapeHtml(letter.title)}</h2><p class="summary">${escapeHtml(letter.summary)}</p></a></li>`,
      )
      .join("")}</ul></main>`,
  });
}

function letterPage(letter) {
  return layout({
    title: `${letter.title} - Guardian Intelligence`,
    description: letter.summary,
    path: `/letters/${letter.slug}`,
    ogPath: `/og/letters/${letter.slug}.svg`,
    body: `<main class="wrap reading"><a class="back" href="/letters">Back to letters</a><article><p class="date">${escapeHtml(letter.publishedAt)}</p><h1>${escapeHtml(letter.title)}</h1><div class="prose">${letter.html}</div></article></main>`,
  });
}

function newsIndex(news) {
  const current = news[0];
  const body = current
    ? `<main class="wrap"><section aria-label="Featured bulletin"><a class="news-bulletin" href="${escapeHtml(newsHref(current))}"><p class="date">${escapeHtml(current.date)}</p><h2>${escapeHtml(current.title)}</h2><span class="read">${escapeHtml(current.ctaLabel || "Read")}</span></a></section><section class="news-meta"><div><p class="date">${escapeHtml(current.kicker)}</p><h2>${escapeHtml(current.title)}</h2></div><p>${escapeHtml(current.deck)}</p></section></main>`
    : `<main class="wrap"><section class="news-bulletin"><h2>Quiet on the wire.</h2></section></main>`;
  return layout({
    title: "News - Guardian Intelligence",
    description: "News, milestones, and public notes from Guardian Intelligence.",
    path: "/news",
    ogPath: "/og/news.svg",
    body,
  });
}

function newsPage(item) {
  return layout({
    title: `${item.title} - Guardian News`,
    description: item.deck,
    path: `/news/${item.slug}`,
    ogPath: `/og/news/${item.slug}.svg`,
    body: `<article><section class="news-hero"><div class="news-hero-inner"><p class="section-title">${escapeHtml(item.category)} - ${escapeHtml(item.date)}</p><h1>${escapeHtml(item.title)}</h1><p class="lede">${escapeHtml(item.deck)}</p></div></section><main class="wrap reading"><a class="back" href="/news">Back to news</a><div class="byline"><strong>${escapeHtml(item.authorName)}</strong><span>${escapeHtml(item.authorRole)}</span></div><div class="prose">${item.html}</div></main></article>`,
  });
}

function wrapWords(text, maxChars) {
  const words = text.split(/\s+/);
  const lines = [];
  let line = "";
  for (const word of words) {
    const next = line ? `${line} ${word}` : word;
    if (next.length > maxChars && line) {
      lines.push(line);
      line = word;
    } else {
      line = next;
    }
  }
  if (line) lines.push(line);
  return lines.slice(0, 4);
}

function ogSvg({ eyebrow, title, description }) {
  const titleLines = wrapWords(title, 24);
  const descLines = wrapWords(description, 54).slice(0, 2);
  const titleText = titleLines
    .map((line, i) => `<text x="76" y="${210 + i * 70}" class="title">${escapeXml(line)}</text>`)
    .join("");
  const descText = descLines
    .map((line, i) => `<text x="80" y="${500 + i * 34}" class="desc">${escapeXml(line)}</text>`)
    .join("");
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
<rect width="1200" height="630" fill="#fbfbf8"/>
<rect x="36" y="36" width="1128" height="558" fill="#ffffff" stroke="#d1d5db" stroke-width="2"/>
<rect x="76" y="78" width="186" height="8" fill="#075985"/>
<text x="76" y="128" class="eyebrow">${escapeXml(eyebrow)}</text>
${titleText}
${descText}
<text x="80" y="570" class="brand">guardianintelligence.org</text>
<style>
.eyebrow,.brand{font:700 25px ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;letter-spacing:0;fill:#075985;text-transform:uppercase}
.brand{font-size:21px;fill:#4b5563}
.title{font:700 61px Georgia,Cambria,"Times New Roman",serif;letter-spacing:0;fill:#111827}
.desc{font:400 28px ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;letter-spacing:0;fill:#4b5563}
</style>
</svg>
`;
}

export function renderSite() {
  const letters = loadLetters();
  const news = loadNews();
  const files = new Map([
    ["index.html", home()],
    ["letters/index.html", lettersIndex(letters)],
    ["news/index.html", newsIndex(news)],
    [
      "og/home.svg",
      ogSvg({
        eyebrow: "Guardian Intelligence",
        title: "Self-hostable cloud for agent-era software companies",
        description: "BYOC on-prem. Bare metal in, software company out.",
      }),
    ],
    [
      "og/letters.svg",
      ogSvg({
        eyebrow: "Letters",
        title: "Letters from Guardian Intelligence",
        description: "Long-form from Guardian Intelligence.",
      }),
    ],
    [
      "og/news.svg",
      ogSvg({
        eyebrow: "News",
        title: "News from Guardian Intelligence",
        description: "News, milestones, and public notes from Guardian Intelligence.",
      }),
    ],
  ]);
  for (const letter of letters) {
    files.set(`letters/${letter.slug}/index.html`, letterPage(letter));
    files.set(
      `og/letters/${letter.slug}.svg`,
      ogSvg({
        eyebrow: letter.publishedAt,
        title: letter.title,
        description: textOf(letter.summary),
      }),
    );
  }
  for (const item of news) {
    files.set(`news/${item.slug}/index.html`, newsPage(item));
    files.set(
      `og/news/${item.slug}.svg`,
      ogSvg({
        eyebrow: item.kicker,
        title: item.title,
        description: textOf(item.deck),
      }),
    );
  }
  return files;
}

export function writeStaticSite(outDir) {
  const files = renderSite();
  rmSync(outDir, { recursive: true, force: true });
  for (const [path, content] of files) {
    const dest = join(outDir, path);
    mkdirSync(dirname(dest), { recursive: true });
    writeFileSync(dest, content);
  }
  return files.size;
}

function goByteSlice(content) {
  const bytes = Buffer.from(content);
  const chunks = [];
  for (let i = 0; i < bytes.length; i += 24) {
    chunks.push(
      Array.from(bytes.subarray(i, i + 24))
        .map((b) => `0x${b.toString(16).padStart(2, "0")}`)
        .join(", "),
    );
  }
  return `[]byte{\n${chunks.map((chunk) => `\t\t${chunk},`).join("\n")}\n\t}`;
}

export function writeGoSource(outFile) {
  const files = renderSite();
  const entries = Array.from(files)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([path, content]) => `\t${JSON.stringify(path)}: ${goByteSlice(content)},`)
    .join("\n");
  const source = `// Code generated by src/products/company/web. DO NOT EDIT.\npackage main\n\nvar siteAssets = map[string][]byte{\n${entries}\n}\n`;
  mkdirSync(dirname(outFile), { recursive: true });
  writeFileSync(outFile, source);
  return files.size;
}
