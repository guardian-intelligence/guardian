import { SITE_DESCRIPTION, SITE_URL } from "~/lib/site";

// FAQ answers render from the same array the FAQPage JSON-LD serializes, so
// the markup and the structured data cannot drift apart.

interface FaqItem {
  readonly question: string;
  readonly answer: string;
}

const FAQ_ITEMS: readonly FaqItem[] = [
  {
    question: "How do I send a video that's over the size limit?",
    answer:
      "Drop it into PrivateCut, drag the selection to the moment that matters — up to 3 minutes — and export. The clip is guaranteed to fit the cap you picked: the default 4 MB clears the upload limit on Discord, email, and effectively every chat app and forum, and 10, 25, or 100 MB are there when the ceiling is higher.",
  },
  {
    question: "How do I compress an MP4 at the best possible quality?",
    answer:
      "Keeping quality means spending every byte you're allowed. PrivateCut encodes a pass, measures the actual file it produced against the byte budget, and re-encodes until the output sits just under the cap instead of far below it. And when your selection can be cut without re-encoding at all, the original bytes are copied through untouched — no re-encode, no quality loss.",
  },
  {
    question: "How do I convert a video to MP4 or WebM?",
    answer:
      "Drop the file in, choose MP4 or WebM, and export. MP4 uses H.264 video and AAC audio for the broadest upload compatibility. WebM uses the best compatible VP9, VP8, or AV1 video and Opus or Vorbis audio encoders available in your browser.",
  },
  {
    question: "Is my video really not uploaded anywhere?",
    answer:
      "Really. Decoding, trimming, and encoding all run inside your browser tab using WebCodecs — open the network panel and watch: the video never leaves your device. That is also why there is no waiting for an upload, a server queue, or a download.",
  },
  {
    question: "Can I clip a video from an X post?",
    answer:
      "Paste a link to the post. PrivateCut resolves the video and you clip it right there — the same trim-and-fit pipeline, no download step.",
  },
  {
    question: "What are the exact limits?",
    answer:
      "Up to 3 minutes of selection, and a size cap you choose: 4, 10, 25, or 100 MB. Caps are SI megabytes — the strict sense upload validators use — so a file that passes PrivateCut's gate passes theirs. No watermark, no account, free.",
  },
];

export function landingJsonLd(): { type: string; children: string }[] {
  return [
    {
      type: "application/ld+json",
      children: JSON.stringify({
        "@context": "https://schema.org",
        "@type": "WebApplication",
        name: "PrivateCut",
        url: `${SITE_URL}/`,
        description: SITE_DESCRIPTION,
        applicationCategory: "MultimediaApplication",
        operatingSystem: "Any — runs in the web browser",
        offers: { "@type": "Offer", price: "0", priceCurrency: "USD" },
      }),
    },
    {
      type: "application/ld+json",
      children: JSON.stringify({
        "@context": "https://schema.org",
        "@type": "FAQPage",
        mainEntity: FAQ_ITEMS.map((item) => ({
          "@type": "Question",
          name: item.question,
          acceptedAnswer: { "@type": "Answer", text: item.answer },
        })),
      }),
    },
  ];
}

export function LandingGuide() {
  return (
    <section className="privatecut-guide" aria-label="About PrivateCut">
      <div className="privatecut-guide__section">
        <h2>Over the limit?</h2>
        <p>
          Every place you share a video has a ceiling — Discord, email, chat apps, forums — and the
          message telling you the file is too big never says what to do about it. PrivateCut is what
          to do about it: keep the moment that matters, up to three minutes of it, pick a cap —
          4&nbsp;MB by default, or 10, 25, 100 — and export the best-looking clip that fits under
          it, guaranteed.
        </p>
      </div>
      <div className="privatecut-guide__section">
        <h2>The best quality that fits</h2>
        <p>
          Shrinking a video usually means guessing at a bitrate and hoping. PrivateCut measures
          instead: each pass encodes, checks the real size of the file it made, and re-encodes until
          the output spends the byte budget instead of wasting it. When your selection can be cut
          without re-encoding at all, the original bytes are copied through untouched — zero
          generation loss.
        </p>
      </div>
      <div className="privatecut-guide__section">
        <h2>Modern video in. MP4 or WebM out.</h2>
        <p>
          Drop MP4, M4V, MOV, MKV, WebM, Ogg, or MPEG-TS containing H.264, H.265, VP8, VP9, or AV1
          video your browser can decode. Export H.264/AAC MP4 for broad compatibility, or WebM when
          you want its modern VP9, VP8, or AV1 and Opus or Vorbis codec family.
        </p>
      </div>
      <div className="privatecut-guide__section">
        <h2>Private by architecture</h2>
        <p>
          There is no upload step to trust anyone with. The work happens in a worker inside your
          browser tab; requests carrying your footage simply never exist. No account, no watermark,
          no queue — close the tab and nothing is left behind.
        </p>
      </div>
      <div className="privatecut-guide__section privatecut-guide__faq">
        <h2>Questions</h2>
        {FAQ_ITEMS.map((item) => (
          <details key={item.question}>
            <summary>{item.question}</summary>
            <p>{item.answer}</p>
          </details>
        ))}
      </div>
    </section>
  );
}
