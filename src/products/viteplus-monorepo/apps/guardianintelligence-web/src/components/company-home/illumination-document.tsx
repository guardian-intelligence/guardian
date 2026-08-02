import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { supportsHtmlInCanvas } from "../../illumination/html-in-canvas";
import { createCanvasRenderer, type CanvasRenderer } from "../../illumination/renderer";
import {
  companyExperienceMode,
  setCompanyExperience,
  waitForTitleMaterialization,
  type StaticExperienceReason,
} from "./company-experience";

export interface IlluminationDocumentProps {
  readonly children: ReactNode;
}

const emptySubscribe = () => () => {};
const INITIALIZATION_BUDGET_MS = 500;

export function IlluminationDocument({ children }: IlluminationDocumentProps) {
  const sourceRef = useRef<HTMLCanvasElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const outputRef = useRef<HTMLCanvasElement>(null);
  const [nativeFailed, setNativeFailed] = useState(false);
  const supported = useSyncExternalStore(emptySubscribe, supportsHtmlInCanvas, () => false);
  const native = supported && !nativeFailed;

  useEffect(() => {
    document.documentElement.dataset.companyHome = "";
    return () => {
      delete document.documentElement.dataset.companyHome;
    };
  }, []);

  useEffect(() => {
    const source = sourceRef.current;
    const content = contentRef.current;
    const output = outputRef.current;
    if (!source || !content || !output) return;

    let disposed = false;
    let generation = 0;
    let renderer: CanvasRenderer | null = null;

    const showFallback = () => {
      output.dataset.mode = "css";
      output.dataset.state = "idle";
      document.documentElement.dataset.canvasMode = "css";
    };

    const showStatic = (reason: StaticExperienceReason) => {
      generation += 1;
      renderer?.dispose();
      renderer = null;
      showFallback();
      setCompanyExperience("static", reason);
    };

    const initialize = async () => {
      if (companyExperienceMode() !== "pending") {
        showFallback();
        return;
      }
      const currentGeneration = ++generation;
      output.dataset.state = "initializing";
      const rendererPromise = createCanvasRenderer({ source, content, output });
      const readinessPromise = Promise.all([rendererPromise, waitForTitleMaterialization()]);
      let timeoutId = 0;
      const timeoutPromise = new Promise<null>((resolve) => {
        timeoutId = window.setTimeout(() => resolve(null), INITIALIZATION_BUDGET_MS);
      });
      let ready: Awaited<typeof readinessPromise> | null;
      try {
        ready = await Promise.race([readinessPromise, timeoutPromise]);
      } catch {
        window.clearTimeout(timeoutId);
        if (!disposed && currentGeneration === generation) showStatic("renderer-unavailable");
        return;
      }
      window.clearTimeout(timeoutId);
      if (!ready) {
        void rendererPromise.then((lateRenderer) => lateRenderer.dispose()).catch(() => undefined);
        if (!disposed && currentGeneration === generation) showStatic("init-timeout");
        return;
      }
      const [nextRenderer, titleReady] = ready;
      if (disposed || currentGeneration !== generation) {
        nextRenderer.dispose();
        return;
      }
      if (!titleReady) {
        nextRenderer.dispose();
        showStatic("title-unavailable");
        return;
      }
      if (nextRenderer.mode === "css") {
        nextRenderer.dispose();
        showStatic("renderer-unavailable");
        return;
      }
      if (native && nextRenderer.mode !== "canvas-ui") {
        nextRenderer.dispose();
        setNativeFailed(true);
        return;
      }
      renderer?.dispose();
      renderer = nextRenderer;
      output.dataset.mode = nextRenderer.mode;
      output.dataset.state = !window.matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "scheduled"
        : "idle";
      document.documentElement.dataset.canvasMode = nextRenderer.mode;
      window.requestAnimationFrame(() => {
        if (!disposed && currentGeneration === generation) setCompanyExperience("animated");
      });
    };

    const onContextLost = (event: Event) => {
      event.preventDefault();
      showStatic("renderer-unavailable");
    };
    const onContextRestored = () => void initialize();
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const onReducedMotion = () => {
      if (reducedMotion.matches) showStatic("reduced-motion");
    };

    output.addEventListener("webglcontextlost", onContextLost);
    output.addEventListener("webglcontextrestored", onContextRestored);
    reducedMotion.addEventListener("change", onReducedMotion);
    void initialize();

    return () => {
      disposed = true;
      generation += 1;
      output.removeEventListener("webglcontextlost", onContextLost);
      output.removeEventListener("webglcontextrestored", onContextRestored);
      reducedMotion.removeEventListener("change", onReducedMotion);
      delete document.documentElement.dataset.canvasMode;
      renderer?.dispose();
    };
  }, [native]);

  return (
    <div
      className="illumination-document"
      data-html-in-canvas={native ? "active" : "fallback"}
      data-testid="illumination-document"
    >
      <canvas
        ref={sourceRef}
        className="illumination-document__source"
        hidden={!native}
        // @ts-expect-error experimental html-in-canvas attribute
        layoutsubtree="true"
        suppressHydrationWarning
      >
        {native ? (
          <div ref={contentRef} className="illumination-document__content">
            {children}
          </div>
        ) : null}
      </canvas>
      {!native ? (
        <div ref={contentRef} className="illumination-document__content">
          {children}
        </div>
      ) : null}
      <canvas
        ref={outputRef}
        className="illumination-canvas"
        data-frame-count="0"
        data-mode="css"
        data-state="initializing"
        aria-hidden="true"
      />
    </div>
  );
}
