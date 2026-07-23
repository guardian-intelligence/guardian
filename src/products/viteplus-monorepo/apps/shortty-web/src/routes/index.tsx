import { createFileRoute } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Dropzone } from "~/components/dropzone";
import { Editor } from "~/components/editor";
import { Header } from "~/components/header";
import { LinkInput } from "~/components/link-input";
import type { ShorttyEngine } from "~/engine/client";
import type { MediaSource, ProbeSummary } from "~/engine/types";
import { emitSpan } from "~/lib/telemetry/browser";

export const Route = createFileRoute("/")({
  component: Home,
});

type Session =
  | { kind: "idle" }
  | { kind: "probing"; name: string }
  | { kind: "ready"; source: MediaSource; summary: ProbeSummary }
  | { kind: "rejected"; message: string };

function Home() {
  const [session, setSession] = useState<Session>({ kind: "idle" });
  const engineRef = useRef<ShorttyEngine | null>(null);
  const warmRef = useRef<Promise<typeof import("~/engine/client")> | null>(null);

  // Engine chunk (worker + mediabunny) starts fetching on first hover of the
  // dropzone, so it is warm by the time a file lands.
  const warm = useCallback(() => {
    warmRef.current ??= import("~/engine/client");
  }, []);

  useEffect(() => () => engineRef.current?.dispose(), []);

  const onSource = useCallback(
    (source: MediaSource) => {
      setSession({ kind: "probing", name: source.name });
      if (source instanceof File) {
        emitSpan("shortty.file_dropped", {
          bytes: String(source.size),
          type: source.type,
        });
      }
      warm();
      void (warmRef.current as Promise<typeof import("~/engine/client")>)
        .then(async (m) => {
          engineRef.current?.dispose();
          const engine = new m.ShorttyEngine();
          engineRef.current = engine;
          const summary = await engine.probe(source);
          emitSpan("shortty.probed", {
            duration_s: summary.durationS.toFixed(2),
            width: String(summary.video.width),
            height: String(summary.video.height),
            source_codec: summary.video.codec,
            keyframes: String(summary.keyframesS.length),
          });
          setSession({ kind: "ready", source, summary });
        })
        .catch((error: unknown) => {
          const message = error instanceof Error ? error.message : "Could not read this file.";
          const traceId = source instanceof File ? undefined : source.traceId;
          emitSpan("shortty.probe_failed", {
            message,
            ...(traceId !== undefined && traceId !== "" && { "trace.id": traceId }),
          });
          setSession({ kind: "rejected", message });
        });
    },
    [warm],
  );

  const reset = useCallback(() => {
    engineRef.current?.dispose();
    engineRef.current = null;
    setSession({ kind: "idle" });
  }, []);

  return (
    <div className="shortty-shell">
      <Header />
      <main id="main" className="shortty-main">
        {session.kind === "ready" && engineRef.current ? (
          <Editor
            engine={engineRef.current}
            source={session.source}
            summary={session.summary}
            onReset={reset}
          />
        ) : (
          <section className="shortty-hero" aria-labelledby="shortty-title">
            <div className="shortty-hero__eyebrow">
              <span>Browser-native video clipping</span>
            </div>
            <div className="shortty-hero__copy-frame">
              <span className="shortty-hero__cross shortty-hero__cross--left" aria-hidden="true" />
              <span className="shortty-hero__cross shortty-hero__cross--right" aria-hidden="true" />
              <h1
                id="shortty-title"
                className="shortty-title"
                data-copy={"Any clip, under 4\u202fMB"}
                aria-label="Any clip, under 4 MB"
              >
                Any clip, under 4&#8239;MB
              </h1>
              <p className="shortty-hero__lede">
                Pick up to a minute of any video. Get the best possible quality that fits — never a
                byte over.
              </p>
            </div>
            <div className="shortty-hero__drop-frame">
              <Dropzone onFile={onSource} onWarm={warm} disabled={session.kind === "probing"} />
              <LinkInput onSource={onSource} onWarm={warm} disabled={session.kind === "probing"} />
            </div>
            {session.kind === "probing" && (
              <p className="shortty-hero__message font-mono">Reading {session.name}…</p>
            )}
            {session.kind === "rejected" && (
              <p className="shortty-hero__message shortty-hero__message--error">
                {session.message}
              </p>
            )}
            <p className="shortty-hero__privacy">
              Everything runs in your browser. Your video is never uploaded.
            </p>
          </section>
        )}
      </main>
    </div>
  );
}
