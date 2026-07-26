import { useEffect, useRef } from "react";
import { createIlluminationRenderer, type IlluminationRenderer } from "~/illumination/renderer";

export function IlluminationScene() {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    let disposed = false;
    let generation = 0;
    let renderer: IlluminationRenderer | null = null;

    const showFallback = () => {
      canvas.dataset.mode = "css";
      canvas.dataset.state = "idle";
      document.documentElement.dataset.illumination = "css";
    };

    const initialize = async () => {
      const currentGeneration = ++generation;
      canvas.dataset.state = "initializing";
      const nextRenderer = await createIlluminationRenderer(canvas);
      if (disposed || currentGeneration !== generation) {
        nextRenderer.dispose();
        return;
      }
      renderer?.dispose();
      renderer = nextRenderer;
      canvas.dataset.mode = nextRenderer.mode;
      canvas.dataset.state =
        nextRenderer.mode === "webgl2" &&
        !window.matchMedia("(prefers-reduced-motion: reduce)").matches
          ? "scheduled"
          : "idle";
      document.documentElement.dataset.illumination = nextRenderer.mode;
    };

    const onContextLost = (event: Event) => {
      event.preventDefault();
      generation += 1;
      renderer?.dispose();
      renderer = null;
      showFallback();
    };
    const onContextRestored = () => void initialize();

    canvas.addEventListener("webglcontextlost", onContextLost);
    canvas.addEventListener("webglcontextrestored", onContextRestored);
    void initialize();

    return () => {
      disposed = true;
      generation += 1;
      canvas.removeEventListener("webglcontextlost", onContextLost);
      canvas.removeEventListener("webglcontextrestored", onContextRestored);
      delete document.documentElement.dataset.illumination;
      renderer?.dispose();
    };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      className="illumination-scene"
      data-frame-count="0"
      data-mode="css"
      data-state="initializing"
      aria-hidden="true"
    />
  );
}
