import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { supportsHtmlInCanvas } from "~/canvas/html-in-canvas";
import { createCanvasRenderer, type CanvasRenderer } from "~/canvas/renderer";

export interface CanvasDocumentProps {
  readonly children: ReactNode;
}

const emptySubscribe = () => () => {};

export function CanvasDocument({ children }: CanvasDocumentProps) {
  const sourceRef = useRef<HTMLCanvasElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const outputRef = useRef<HTMLCanvasElement>(null);
  const [nativeFailed, setNativeFailed] = useState(false);
  const supported = useSyncExternalStore(emptySubscribe, supportsHtmlInCanvas, () => false);
  const native = supported && !nativeFailed;

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

    const initialize = async () => {
      const currentGeneration = ++generation;
      output.dataset.state = "initializing";
      const nextRenderer = await createCanvasRenderer({ source, content, output });
      if (disposed || currentGeneration !== generation) {
        nextRenderer.dispose();
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
      output.dataset.state =
        nextRenderer.mode !== "css" &&
        !window.matchMedia("(prefers-reduced-motion: reduce)").matches
          ? "scheduled"
          : "idle";
      document.documentElement.dataset.canvasMode = nextRenderer.mode;
    };

    const onContextLost = (event: Event) => {
      event.preventDefault();
      generation += 1;
      renderer?.dispose();
      renderer = null;
      showFallback();
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
      renderer?.dispose();
    };
  }, [native]);

  return (
    <div
      className="canvas-document"
      data-html-in-canvas={native ? "active" : "fallback"}
      data-testid="canvas-document"
    >
      <canvas
        ref={sourceRef}
        className="canvas-document__source"
        hidden={!native}
        // @ts-expect-error experimental html-in-canvas attribute
        layoutsubtree="true"
        suppressHydrationWarning
      >
        {native ? (
          <div ref={contentRef} className="canvas-document__content">
            {children}
          </div>
        ) : null}
      </canvas>
      {!native ? (
        <div ref={contentRef} className="canvas-document__content">
          {children}
        </div>
      ) : null}
      <canvas
        ref={outputRef}
        className="privatecut-canvas"
        data-frame-count="0"
        data-mode="css"
        data-state="initializing"
        aria-hidden="true"
      />
    </div>
  );
}
