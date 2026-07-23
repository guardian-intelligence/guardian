import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";
import { useCallback, useEffect, useRef, useState } from "react";
import { Dropzone } from "~/components/dropzone";
import { Editor } from "~/components/editor";
import { Header } from "~/components/header";
import { LinkInput } from "~/components/link-input";
import type { PrivateCutEngine } from "~/engine/client";
import type { MediaSource, ProbeSummary } from "~/engine/types";
import { emitSpan } from "~/lib/telemetry/browser";

type DeviceKind = "phone" | "computer";

// Phones (not tablets/desktops) get "your phone" copy. iPadOS 13+ reports a
// desktop Safari UA, so iPads read as "computer" — which matches how people
// describe them here.
function deviceFromUserAgent(ua: string): DeviceKind {
  if (/iphone|ipod|windows phone/i.test(ua)) return "phone";
  if (/android/i.test(ua) && /mobile/i.test(ua)) return "phone";
  return "computer";
}

// Read the UA during SSR so the first document already carries the right word;
// no client-side swap, no hydration flicker.
const getDeviceKind = createServerFn({ method: "GET" }).handler((): DeviceKind => {
  const ua = getRequest().headers.get("user-agent") ?? "";
  return deviceFromUserAgent(ua);
});

export const Route = createFileRoute("/")({
  component: Home,
  loader: () => getDeviceKind(),
});

type Session =
  | { kind: "idle" }
  | { kind: "probing"; name: string }
  | { kind: "ready"; source: MediaSource; summary: ProbeSummary }
  | { kind: "rejected"; message: string };

function Home() {
  const device = Route.useLoaderData();
  const [session, setSession] = useState<Session>({ kind: "idle" });
  const engineRef = useRef<PrivateCutEngine | null>(null);
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
        emitSpan("privatecut.file_dropped", {
          bytes: String(source.size),
          type: source.type,
        });
      }
      warm();
      void (warmRef.current as Promise<typeof import("~/engine/client")>)
        .then(async (m) => {
          engineRef.current?.dispose();
          const engine = new m.PrivateCutEngine();
          engineRef.current = engine;
          const summary = await engine.probe(source);
          emitSpan("privatecut.probed", {
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
          emitSpan("privatecut.probe_failed", {
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
    <div className="privatecut-shell">
      <Header />
      <main id="main" className="privatecut-main">
        {session.kind === "ready" && engineRef.current ? (
          <Editor
            engine={engineRef.current}
            source={session.source}
            summary={session.summary}
            onReset={reset}
          />
        ) : (
          <section className="privatecut-hero" aria-labelledby="privatecut-title">
            <div className="privatecut-hero__eyebrow">
              <span>Browser-native video clipping</span>
            </div>
            <div className="privatecut-hero__copy-frame">
              <span
                className="privatecut-hero__cross privatecut-hero__cross--left"
                aria-hidden="true"
              />
              <span
                className="privatecut-hero__cross privatecut-hero__cross--right"
                aria-hidden="true"
              />
              <h1
                id="privatecut-title"
                className="privatecut-title"
                data-copy="Private Cutting Room Floor"
                aria-label="Private Cutting Room Floor"
              >
                Private Cutting Room Floor
              </h1>
              <p className="privatecut-hero__lede">
                Videos stay on your {device} — never uploaded to the cloud.
              </p>
            </div>
            <div className="privatecut-hero__drop-frame">
              <Dropzone onFile={onSource} onWarm={warm} disabled={session.kind === "probing"} />
              <LinkInput onSource={onSource} onWarm={warm} disabled={session.kind === "probing"} />
            </div>
            {session.kind === "probing" && (
              <p className="privatecut-hero__message font-mono">Reading {session.name}…</p>
            )}
            {session.kind === "rejected" && (
              <p className="privatecut-hero__message privatecut-hero__message--error">
                {session.message}
              </p>
            )}
          </section>
        )}
      </main>
    </div>
  );
}
