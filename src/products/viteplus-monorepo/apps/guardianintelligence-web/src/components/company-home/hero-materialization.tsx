import { useEffect, useLayoutEffect, useRef } from "react";
import {
  COMPANY_EXPERIENCE_EVENT,
  companyExperienceMode,
  markTitleMaterialization,
} from "./company-experience";
import {
  activationThreshold,
  materializationPixelOpacity,
  pixelLightState,
  spotlightMask,
  type MaterializationPixel,
} from "./hero-materialization-model";
import { GUARDIAN_GLYPH_PATHS, GUARDIAN_WORDMARK } from "./outlined-wordmarks";

const HERO_GLYPH_BOUNDS = [
  [0, 634.609],
  [1007.602, 1576.195],
  [1898.805, 2569.602],
  [2904.804, 3459.21],
  [3853.605, 4443.011],
  [4830.806, 4960.806],
  [5310.01, 5980.807],
  [6316.009, 6903.806],
] as const;
const HERO_GLYPH_GAP = 105;

function makeHeroWordmark() {
  let cursor = 0;
  const glyphs = HERO_GLYPH_BOUNDS.map(([minX, maxX], index) => {
    const path = GUARDIAN_GLYPH_PATHS[index];
    if (!path) throw new Error("Guardian wordmark glyph data is invalid");
    const offsetX = cursor - minX;
    cursor += maxX - minX + HERO_GLYPH_GAP;
    return { offsetX, path };
  });

  return {
    glyphs,
    height: GUARDIAN_WORDMARK.height,
    width: cursor - HERO_GLYPH_GAP,
  } as const;
}

const HERO_WORDMARK = makeHeroWordmark();
const MAX_PIXEL_RATIO = 2;

function makePixelField(
  context: CanvasRenderingContext2D,
  path: Path2D,
  width: number,
  height: number,
  cellSize: number,
) {
  const scale = Math.min(width / HERO_WORDMARK.width, height / HERO_WORDMARK.height);
  const renderedWidth = HERO_WORDMARK.width * scale;
  const renderedHeight = HERO_WORDMARK.height * scale;
  const offsetX = (width - renderedWidth) * 0.5;
  const offsetY = (height - renderedHeight) * 0.5;
  const pixels: MaterializationPixel[] = [];
  const columns = Math.ceil(width / cellSize);
  const rows = Math.ceil(height / cellSize);

  context.resetTransform();
  for (let row = 0; row < rows; row += 1) {
    for (let column = 0; column < columns; column += 1) {
      const x = column * cellSize + cellSize * 0.5;
      const y = row * cellSize + cellSize * 0.5;
      const glyphX = (x - offsetX) / scale;
      const glyphY = (y - offsetY) / scale;
      if (!context.isPointInPath(path, glyphX, glyphY)) continue;
      const normalizedX = (glyphX / HERO_WORDMARK.width) * 2 - 1;
      const normalizedY = (glyphY / HERO_WORDMARK.height) * 2 - 1;
      pixels.push({
        activation: activationThreshold(column, row, normalizedX, normalizedY),
        column,
        normalizedX,
        normalizedY,
        row,
        x: x - cellSize * 0.5,
        y: y - cellSize * 0.5,
      });
    }
  }
  return pixels;
}

export function HeroMaterialization({
  active,
  label,
}: {
  readonly active: boolean;
  readonly label: string;
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const controllerRef = useRef<{ resume(): void; suspend(): void } | null>(null);
  const luminosityCanvasRef = useRef<HTMLCanvasElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const activeRef = useRef(active);

  activeRef.current = active;

  useLayoutEffect(() => {
    const canvas = canvasRef.current;
    const luminosityCanvas = luminosityCanvasRef.current;
    const title = titleRef.current;
    if (!canvas || !luminosityCanvas || !title || companyExperienceMode() === "static") return;

    const context = canvas.getContext("2d", { alpha: true });
    const luminosityContext = luminosityCanvas.getContext("2d", { alpha: true });
    if (
      !context ||
      !luminosityContext ||
      typeof DOMMatrix === "undefined" ||
      typeof Path2D === "undefined"
    ) {
      canvas.dataset.titleMaterialization = "failed";
      markTitleMaterialization("failed");
      return;
    }

    const glyphPath = new Path2D();
    for (const glyph of HERO_WORDMARK.glyphs) {
      glyphPath.addPath(new Path2D(glyph.path), new DOMMatrix([1, 0, 0, 1, glyph.offsetX, 0]));
    }
    let animationFrame = 0;
    let cellSize = 4;
    let height = 1;
    let pixelRatio = 1;
    let pixels: MaterializationPixel[] = [];
    let width = 1;

    const clearCanvas = (
      targetContext: CanvasRenderingContext2D,
      targetCanvas: HTMLCanvasElement,
    ) => {
      targetContext.setTransform(1, 0, 0, 1, 0, 0);
      targetContext.clearRect(0, 0, targetCanvas.width, targetCanvas.height);
    };

    const clear = () => {
      clearCanvas(context, canvas);
      clearCanvas(luminosityContext, luminosityCanvas);
    };

    const measure = () => {
      const rect = title.getBoundingClientRect();
      width = Math.max(1, rect.width);
      height = Math.max(1, rect.height);
      pixelRatio = Math.min(window.devicePixelRatio || 1, MAX_PIXEL_RATIO);
      const computedSize = Number.parseFloat(
        getComputedStyle(title).getPropertyValue("--company-pixel-size"),
      );
      cellSize = Number.isFinite(computedSize) ? computedSize : 4;
      canvas.width = Math.max(1, Math.round(width * pixelRatio));
      canvas.height = Math.max(1, Math.round(height * pixelRatio));
      luminosityCanvas.width = canvas.width;
      luminosityCanvas.height = canvas.height;
      canvas.dataset.pixelRatio = pixelRatio.toFixed(2);
      pixels = makePixelField(context, glyphPath, width, height, cellSize);
      canvas.dataset.pixelCount = String(pixels.length);
      canvas.dataset.titleMaterialization = pixels.length > 0 ? "ready" : "failed";
      markTitleMaterialization(pixels.length > 0 ? "ready" : "failed");
    };

    const draw = () => {
      animationFrame = 0;
      if (companyExperienceMode() !== "animated") {
        clear();
        return;
      }

      const progressValue = Number.parseFloat(
        getComputedStyle(title).getPropertyValue("--company-materialize-progress"),
      );
      const progress = Number.isFinite(progressValue) ? Math.min(1, Math.max(0, progressValue)) : 0;
      const pixelOpacity = materializationPixelOpacity(progress);
      const mask = spotlightMask(progress);
      const active = new Path2D();
      const renderedSize = cellSize * 0.76;
      const inset = (cellSize - renderedSize) * 0.5;
      let offCount = 0;
      let onCount = 0;
      let spotlightCount = 0;

      for (const pixel of pixels) {
        const state = pixelLightState(pixel, progress);
        if (state === "off") {
          offCount += 1;
          continue;
        }
        active.rect(pixel.x + inset, pixel.y + inset, renderedSize, renderedSize);
        if (state === "spotlight") spotlightCount += 1;
        else onCount += 1;
      }

      clear();
      context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0);
      context.globalAlpha = pixelOpacity;
      context.fillStyle = "rgb(115 148 190 / 62%)";
      context.fill(active);
      context.globalAlpha = 1;

      luminosityContext.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0);
      luminosityContext.globalAlpha = pixelOpacity;
      luminosityContext.fillStyle = "rgb(231 244 255 / 96%)";
      luminosityContext.fill(active);
      luminosityContext.globalAlpha = 1;

      title.style.setProperty("--company-spotlight-left", `${mask.left.toFixed(3)}%`);
      title.style.setProperty("--company-spotlight-left-trail", `${mask.leftTrail.toFixed(3)}%`);
      title.style.setProperty("--company-spotlight-opacity", mask.opacity.toFixed(4));
      title.style.setProperty("--company-spotlight-right", `${mask.right.toFixed(3)}%`);
      title.style.setProperty("--company-spotlight-right-trail", `${mask.rightTrail.toFixed(3)}%`);
      title.style.setProperty("--company-spotlight-trail-width", `${mask.trailWidth.toFixed(3)}%`);
      title.style.setProperty("--company-spotlight-width", `${mask.width.toFixed(3)}%`);

      canvas.dataset.materializeProgress = progress.toFixed(4);
      canvas.dataset.pixelOpacity = pixelOpacity.toFixed(4);
      canvas.dataset.pixelsOff = String(offCount);
      canvas.dataset.pixelsOn = String(onCount);
      canvas.dataset.pixelsSpotlight = String(spotlightCount);
      if (progress < 0.999) animationFrame = window.requestAnimationFrame(draw);
    };

    const start = () => {
      if (animationFrame || !activeRef.current || companyExperienceMode() !== "animated") return;
      animationFrame = window.requestAnimationFrame(draw);
    };
    const onExperience = () => {
      if (companyExperienceMode() === "animated") start();
      else {
        if (animationFrame) window.cancelAnimationFrame(animationFrame);
        animationFrame = 0;
        clear();
      }
    };
    const onVisualSeek = () => {
      if (!activeRef.current) return;
      if (animationFrame) window.cancelAnimationFrame(animationFrame);
      animationFrame = window.requestAnimationFrame(draw);
    };
    const resizeObserver = new ResizeObserver(() => {
      measure();
      if (companyExperienceMode() === "animated") onVisualSeek();
    });

    resizeObserver.observe(title);
    measure();
    window.addEventListener(COMPANY_EXPERIENCE_EVENT, onExperience);
    window.addEventListener("visual-harness:seek", onVisualSeek);
    controllerRef.current = {
      resume: start,
      suspend: () => {
        if (animationFrame) window.cancelAnimationFrame(animationFrame);
        animationFrame = 0;
      },
    };
    start();

    return () => {
      if (animationFrame) window.cancelAnimationFrame(animationFrame);
      resizeObserver.disconnect();
      window.removeEventListener(COMPANY_EXPERIENCE_EVENT, onExperience);
      window.removeEventListener("visual-harness:seek", onVisualSeek);
      controllerRef.current = null;
      clear();
    };
  }, []);

  useEffect(() => {
    if (active) controllerRef.current?.resume();
    else controllerRef.current?.suspend();
  }, [active]);

  return (
    <h1 ref={titleRef} id="company-home-title" className="company-home-title" aria-label={label}>
      <span className="sr-only">{label}</span>
      <svg
        className="company-home-title__outline"
        aria-hidden="true"
        focusable="false"
        viewBox={`0 0 ${HERO_WORDMARK.width} ${HERO_WORDMARK.height}`}
      >
        <defs>
          <linearGradient id="company-home-title-gradient" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="#98c0ef" />
            <stop offset="1" stopColor="#d8ecf8" />
          </linearGradient>
        </defs>
        {HERO_WORDMARK.glyphs.map((glyph) => (
          <path
            key={glyph.path}
            d={glyph.path}
            fill="url(#company-home-title-gradient)"
            transform={`translate(${glyph.offsetX})`}
          />
        ))}
      </svg>
      <canvas
        ref={canvasRef}
        className="company-home-title__materialization company-home-title__materialization--base"
        data-title-materialization="idle"
        aria-hidden="true"
      />
      <canvas
        ref={luminosityCanvasRef}
        className="company-home-title__materialization company-home-title__materialization--luminous"
        data-title-luminosity="mask"
        aria-hidden="true"
      />
    </h1>
  );
}
