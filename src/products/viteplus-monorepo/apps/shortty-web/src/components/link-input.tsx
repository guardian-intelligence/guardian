import { useCallback, useState } from "react";
import { importFromXLink, ResolveError } from "~/lib/x-download";
import { emitSpan } from "~/lib/telemetry/browser";

export interface LinkInputProps {
  readonly onFile: (file: File) => void;
  readonly onWarm: () => void;
  readonly disabled: boolean;
}

type Phase =
  | { kind: "idle" }
  | { kind: "working"; label: string }
  | { kind: "failed"; message: string };

// Paste-a-link alternative to the dropzone: an X post URL resolves to its mp4
// and drops into the same session path as a local file.
export function LinkInput({ onFile, onWarm, disabled }: LinkInputProps) {
  const [link, setLink] = useState("");
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const busy = phase.kind === "working";

  const submit = useCallback(() => {
    const value = link.trim();
    if (value === "" || busy) return;
    onWarm();
    setPhase({ kind: "working", label: "Looking up the post…" });
    emitSpan("shortty.link_submitted", {});
    const startedAt = performance.now();
    importFromXLink(value, (fraction) => {
      setPhase({
        kind: "working",
        label:
          fraction === null
            ? "Downloading the video…"
            : `Downloading the video… ${Math.round(fraction * 100)}%`,
      });
    })
      .then((file) => {
        emitSpan("shortty.link_imported", {
          bytes: String(file.size),
          wall_ms: String(Math.round(performance.now() - startedAt)),
        });
        setPhase({ kind: "idle" });
        setLink("");
        onFile(file);
      })
      .catch((error: unknown) => {
        const code = error instanceof ResolveError ? error.code : "unknown";
        const message = error instanceof Error ? error.message : "Something went wrong. Try again.";
        emitSpan("shortty.link_failed", { code });
        setPhase({ kind: "failed", message });
      });
  }, [link, busy, onFile, onWarm]);

  return (
    <div>
      <div className="my-6 flex items-center gap-4" aria-hidden="true">
        <span className="h-px flex-1 bg-line-strong" />
        <span className="text-xs uppercase tracking-widest text-mist-faint">or</span>
        <span className="h-px flex-1 bg-line-strong" />
      </div>
      <form
        className="glass flex items-center gap-2 p-2"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <input
          type="url"
          value={link}
          onChange={(e) => setLink(e.currentTarget.value)}
          onFocus={onWarm}
          disabled={disabled || busy}
          placeholder="Paste a link to an X post with a video"
          aria-label="Link to an X post with a video"
          className="min-w-0 flex-1 bg-transparent px-3 py-2 text-sm text-mist placeholder:text-mist-faint focus:outline-none"
        />
        <button
          type="submit"
          disabled={disabled || busy || link.trim() === ""}
          className="rounded-xl border border-line-strong bg-white/[0.06] px-4 py-2 text-sm font-medium text-mist transition-colors hover:bg-white/[0.1] disabled:opacity-50"
        >
          Fetch
        </button>
      </form>
      {phase.kind === "working" && (
        <p className="mt-3 text-center font-mono text-sm text-mist-faint">{phase.label}</p>
      )}
      {phase.kind === "failed" && (
        <p className="mx-auto mt-3 max-w-md rounded-xl border border-red-400/30 bg-red-400/10 px-4 py-3 text-center text-sm text-red-200">
          {phase.message}
        </p>
      )}
    </div>
  );
}
