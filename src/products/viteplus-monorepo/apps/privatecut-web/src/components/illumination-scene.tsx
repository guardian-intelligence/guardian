import { useEffect, useRef } from "react";
import { createIlluminationRenderer } from "~/illumination/renderer";

export function IlluminationScene() {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;

    const renderer = createIlluminationRenderer(canvas);
    canvas.dataset.mode = renderer.mode;
    canvas.dataset.state = "idle";

    return () => renderer.dispose();
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
