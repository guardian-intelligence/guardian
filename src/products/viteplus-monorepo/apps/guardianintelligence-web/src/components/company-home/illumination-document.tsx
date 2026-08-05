import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { supportsHtmlInCanvas } from "../../illumination/html-in-canvas";
import { createCanvasRenderer, type CanvasRenderer } from "../../illumination/renderer";
import {
  refreshCompanyExperiencePolicy,
  setCompanyExperience,
  useCompanyExperienceMode,
} from "./company-experience";
import { CompanySceneController, CompanySceneProvider } from "./company-scene";
import { monitorCompanyVisualIntegrity } from "./company-visual-integrity";

export interface IlluminationDocumentProps {
  readonly active: boolean;
  readonly children: ReactNode;
}

const emptySubscribe = () => () => {};

export function IlluminationDocument({ active, children }: IlluminationDocumentProps) {
  const documentRef = useRef<HTMLDivElement>(null);
  const sourceRef = useRef<HTMLCanvasElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const outputRef = useRef<HTMLCanvasElement>(null);
  const rendererRef = useRef<CanvasRenderer | null>(null);
  const activeRef = useRef(active);
  const sceneRef = useRef<CompanySceneController | null>(null);
  sceneRef.current ??= new CompanySceneController();
  const scene = sceneRef.current;
  const [nativeFailed, setNativeFailed] = useState(false);
  const supported = useSyncExternalStore(emptySubscribe, supportsHtmlInCanvas, () => false);
  const native = supported && !nativeFailed;
  const experience = useCompanyExperienceMode();
  const motionAllowed = experience !== "static";

  activeRef.current = active;

  useLayoutEffect(() => {
    scene.attach(documentRef.current);
    return () => scene.attach(null);
  }, [scene]);

  useLayoutEffect(() => {
    if (!active) return;
    const monitor = monitorCompanyVisualIntegrity();
    const unsubscribe = scene.subscribe((frame) => {
      if (frame.phase === "settled") monitor.finish();
    });
    return () => {
      unsubscribe();
      monitor.dispose();
    };
  }, [active, scene]);

  useLayoutEffect(() => {
    if (!motionAllowed) {
      scene.finish();
      return;
    }
    const animationFrame = window.requestAnimationFrame(() => {
      scene.start();
      setCompanyExperience("animated", "ready");
    });
    return () => window.cancelAnimationFrame(animationFrame);
  }, [motionAllowed, scene]);

  useEffect(
    () => () => {
      scene.dispose();
    },
    [scene],
  );

  useEffect(() => {
    const onVisualSeek = (event: Event) => {
      const seekMs = (event as CustomEvent<{ seekMs?: number }>).detail?.seekMs;
      if (typeof seekMs === "number") scene.seek(seekMs);
    };
    window.addEventListener("visual-harness:seek", onVisualSeek);
    return () => window.removeEventListener("visual-harness:seek", onVisualSeek);
  }, [scene]);

  useLayoutEffect(() => {
    const source = sourceRef.current;
    const content = contentRef.current;
    const output = outputRef.current;
    if (!source || !content || !output) return;

    let disposed = false;
    let generation = 0;
    let renderer: CanvasRenderer | null = null;
    let unsubscribeRenderer: (() => void) | null = null;

    const releaseRenderer = () => {
      unsubscribeRenderer?.();
      unsubscribeRenderer = null;
      renderer?.dispose();
      renderer = null;
      rendererRef.current = null;
    };

    const showFallback = (reason?: string) => {
      releaseRenderer();
      output.dataset.mode = "css";
      output.dataset.state = "idle";
      if (reason) output.dataset.reason = reason;
      else delete output.dataset.reason;
      document.documentElement.dataset.canvasMode = "css";
    };

    const initialize = async () => {
      if (!motionAllowed) {
        showFallback();
        return;
      }
      const currentGeneration = ++generation;
      output.dataset.state = "initializing";
      delete output.dataset.reason;
      let nextRenderer: CanvasRenderer;
      try {
        nextRenderer = await createCanvasRenderer({ source, content, output });
      } catch {
        if (!disposed && currentGeneration === generation) showFallback("renderer-unavailable");
        return;
      }
      if (disposed || currentGeneration !== generation) {
        nextRenderer.dispose();
        return;
      }
      if (nextRenderer.mode === "css") {
        nextRenderer.dispose();
        showFallback("renderer-unavailable");
        return;
      }
      if (native && nextRenderer.mode !== "canvas-ui") {
        nextRenderer.dispose();
        setNativeFailed(true);
        return;
      }
      renderer?.dispose();
      unsubscribeRenderer?.();
      renderer = nextRenderer;
      rendererRef.current = nextRenderer;
      unsubscribeRenderer = scene.subscribe((frame) => nextRenderer.setSceneFrame(frame));
      nextRenderer.setActive(activeRef.current);
      output.dataset.mode = nextRenderer.mode;
      output.dataset.state = "scheduled";
      document.documentElement.dataset.canvasMode = nextRenderer.mode;
    };

    const onContextLost = (event: Event) => {
      event.preventDefault();
      showFallback("context-lost");
    };
    const onContextRestored = () => void initialize();

    output.addEventListener("webglcontextlost", onContextLost);
    output.addEventListener("webglcontextrestored", onContextRestored);
    void initialize();

    return () => {
      disposed = true;
      generation += 1;
      output.removeEventListener("webglcontextlost", onContextLost);
      output.removeEventListener("webglcontextrestored", onContextRestored);
      delete document.documentElement.dataset.canvasMode;
      releaseRenderer();
    };
  }, [motionAllowed, native, scene]);

  useEffect(() => {
    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
    const connection = (
      navigator as Navigator & {
        connection?: EventTarget & { effectiveType?: string; saveData?: boolean };
      }
    ).connection;
    const onPreferenceChange = () => refreshCompanyExperiencePolicy();
    reducedMotion.addEventListener("change", onPreferenceChange);
    connection?.addEventListener("change", onPreferenceChange);
    return () => {
      reducedMotion.removeEventListener("change", onPreferenceChange);
      connection?.removeEventListener("change", onPreferenceChange);
    };
  }, []);

  useEffect(() => {
    outputRef.current?.setAttribute("data-route-state", active ? "active" : "suspended");
    scene.setActive(active);
    rendererRef.current?.setActive(active);
  }, [active, scene]);

  return (
    <CompanySceneProvider controller={scene}>
      <div
        ref={documentRef}
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
    </CompanySceneProvider>
  );
}
