import { createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  component: Home,
});

// Every visible string on this page is charter-approved copy
// (docs/aisucks/charter.md): the site name, and "The promise" — canonical and
// verbatim, changed only by charter amendment. Nothing else may be added.
function Home() {
  return (
    <div className="flex min-h-dvh flex-col">
      <main className="flex flex-1 flex-col items-center justify-center px-6 pb-24">
        <h1 className="text-6xl font-medium tracking-tighter select-none">
          aisucks<span className="text-neutral-400">.app</span>
        </h1>
      </main>
      <footer className="border-t border-neutral-100 px-6 py-5">
        <p className="mx-auto max-w-2xl text-center text-xs leading-relaxed text-neutral-500">
          Your chat and chat messages will never be sold to OpenAI, Anthropic, or anyone else.
          Expert human annotators convert a PII-redacted version of your shared link into an exam
          question for the next generation of AI. Learn more about how we protect your privacy and
          hold AI companies accountable.
        </p>
      </footer>
    </div>
  );
}
