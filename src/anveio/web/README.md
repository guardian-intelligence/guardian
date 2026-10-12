# anveio.com

The company site: the home page (`/`), `/contact`, `/privacy` and the
investor deck (`/deck`), server-rendered by TanStack Start. Everything on
anveio.com is this app except `/shovon`, `/oauth` and `/cal`, which belong
to Shovon's contact card (`src/anveio/cal`); `/privacy` covers both apps.
The company pages share the deck's tokens and fonts and are laid out by CSS
alone (`src/styles/site.css`), so they render the same without JavaScript.

```sh
vp run @guardian/anveio-web#dev   # from the repo root; http://127.0.0.1:4254
```

## Deck

The server-rendered deck lives at https://anveio.com/deck. Open Graph and X
cards use a PNG screenshot of its real first slide, including the shared
brand and bundled fonts. The complete 16:9 slide fits a 1200×630 black canvas.

After editing the title, its CSS, fonts or shared brand, regenerate from the
workspace root:

```sh
vp run @guardian/anveio-web#social-preview
```

The helper starts the local source server on port 4264 and uses the catalog-pinned
Playwright Chromium (install its browser with `vp exec playwright install chromium`
from `src/anveio/web` if needed). Commit the PNG and `social-preview.json`.
The image filename is content-addressed so new designs get a fresh image URL;
X may still cache the card for an already-shared page URL. CI rejects stale
source fingerprints or missing/corrupt PNGs. It doesn't require a browser.

The deck remains unlisted (`noindex, nofollow, noarchive`); link crawlers can
read the server-rendered card metadata without executing JavaScript.
