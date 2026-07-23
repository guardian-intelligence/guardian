import { createFileRoute } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Dropzone } from "~/components/dropzone";
import { Editor } from "~/components/editor";
import { Header } from "~/components/header";
import { LinkInput } from "~/components/link-input";
import type { ShorttyEngine } from "~/engine/client";
import type { ProbeSummary } from "~/engine/types";
import { emitSpan } from "~/lib/telemetry/browser";

export const Route = createFileRoute("/")({
  component: Home,
});

type Session =
  | { kind: "idle" }
  | { kind: "probing"; name: string }
  | { kind: "ready"; file: File; summary: ProbeSummary }
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

  const onFile = useCallback(
    (file: File) => {
      setSession({ kind: "probing", name: file.name });
      emitSpan("shortty.file_dropped", {
        bytes: String(file.size),
        type: file.type,
      });
      warm();
      void (warmRef.current as Promise<typeof import("~/engine/client")>)
        .then(async (m) => {
          engineRef.current?.dispose();
          const engine = new m.ShorttyEngine();
          engineRef.current = engine;
          const summary = await engine.probe(file);
          emitSpan("shortty.probed", {
            duration_s: summary.durationS.toFixed(2),
            width: String(summary.video.width),
            height: String(summary.video.height),
            source_codec: summary.video.codec,
            keyframes: String(summary.keyframesS.length),
          });
          setSession({ kind: "ready", file, summary });
        })
        .catch((error: unknown) => {
          const message = error instanceof Error ? error.message : "Could not read this file.";
          emitSpan("shortty.probe_failed", { message });
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
    <div className="mx-auto flex min-h-screen w-full max-w-3xl flex-col px-5 pb-16">
      <Header />
      <main id="main" className="flex flex-1 flex-col justify-center py-10">
        {session.kind === "ready" && engineRef.current ? (
          <Editor
            engine={engineRef.current}
            file={session.file}
            summary={session.summary}
            onReset={reset}
          />
        ) : (
          <>
            <h1 className="text-center text-4xl font-semibold tracking-tight text-mist md:text-5xl">
              Any clip, under 4&thinsp;MB
            </h1>
            <p className="mx-auto mt-4 max-w-md text-center text-mist-dim">
              Pick up to a minute of any video. Get the best possible quality that fits — never a
              byte over.
            </p>
            <div className="mt-10">
              <Dropzone onFile={onFile} onWarm={warm} disabled={session.kind === "probing"} />
              <LinkInput onFile={onFile} onWarm={warm} disabled={session.kind === "probing"} />
            </div>
            {session.kind === "probing" && (
              <p className="mt-4 text-center font-mono text-sm text-mist-faint">
                Reading {session.name}…
              </p>
            )}
            {session.kind === "rejected" && (
              <p className="mx-auto mt-4 max-w-md rounded-xl border border-red-400/30 bg-red-400/10 px-4 py-3 text-center text-sm text-red-200">
                {session.message}
              </p>
            )}
            <p className="mt-12 text-center text-xs text-mist-faint">
              Everything runs in your browser. Your video is never uploaded.
            </p>
          </>
        )}
      </main>
    </div>
  );
}
