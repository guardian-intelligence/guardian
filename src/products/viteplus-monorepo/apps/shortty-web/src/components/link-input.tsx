import { useCallback, useState } from "react";
import type { MediaSource } from "~/engine/types";
import { resolveXVideo, ResolveError } from "~/lib/x-resolve";
import { emitSpan } from "~/lib/telemetry/browser";

export interface LinkInputProps {
  readonly onSource: (source: MediaSource) => void;
  readonly onWarm: () => void;
  readonly disabled: boolean;
}

type Phase = { kind: "idle" } | { kind: "resolving" } | { kind: "failed"; message: string };

// Paste-a-link alternative to the dropzone: an X post URL resolves to its mp4
// and drops into the same session path as a local file.
export function LinkInput({ onSource, onWarm, disabled }: LinkInputProps) {
  const [link, setLink] = useState("");
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  const busy = phase.kind === "resolving";

  const submit = useCallback(() => {
    const value = link.trim();
    if (value === "" || busy) return;
    onWarm();
    setPhase({ kind: "resolving" });
    emitSpan("shortty.link_submitted", {});
    const startedAt = performance.now();
    resolveXVideo(value)
      .then(({ source, traceId }) => {
        emitSpan("shortty.link_resolved", {
          wall_ms: String(Math.round(performance.now() - startedAt)),
          ...(traceId !== "" && { "trace.id": traceId }),
        });
        setPhase({ kind: "idle" });
        setLink("");
        onSource(source);
      })
      .catch((error: unknown) => {
        const code = error instanceof ResolveError ? error.code : "unknown";
        const traceId = error instanceof ResolveError ? error.traceId : "";
        const message = error instanceof Error ? error.message : "Something went wrong. Try again.";
        emitSpan("shortty.link_failed", { code, ...(traceId !== "" && { "trace.id": traceId }) });
        setPhase({ kind: "failed", message });
      });
  }, [link, busy, onSource, onWarm]);

  return (
    <div className="link-input">
      <div className="link-input__divider" aria-hidden="true">
        <span />
        <span>or</span>
        <span />
      </div>
      <form
        className="link-input__form"
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
          className="link-input__field"
        />
        <button
          type="submit"
          disabled={disabled || busy || link.trim() === ""}
          className="link-input__submit"
        >
          Fetch
        </button>
      </form>
      {phase.kind === "resolving" && (
        <p className="link-input__message font-mono">Looking up the post…</p>
      )}
      {phase.kind === "failed" && (
        <p className="link-input__message link-input__message--error">{phase.message}</p>
      )}
    </div>
  );
}
