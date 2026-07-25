import { useEffect, useRef } from "react";

type Particle = {
  x: number;
  y: number;
  size: number;
  speed: number;
  alpha: number;
  phase: number;
  twinkle: number;
};

type ParticleLayer = {
  blur: number;
  particles: Particle[];
};

const LAYERS = [
  {
    density: 0.0002,
    size: [0.3, 0.55],
    speed: [0.002, 0.005],
    alpha: [0.1, 0.28],
    blur: 0.4,
  },
  {
    density: 0.000065,
    size: [0.45, 0.8],
    speed: [0.006, 0.011],
    alpha: [0.15, 0.4],
    blur: 0.8,
  },
  {
    density: 0.000018,
    size: [0.7, 1.15],
    speed: [0.012, 0.02],
    alpha: [0.2, 0.55],
    blur: 1.8,
  },
] as const;

const between = ([min, max]: readonly [number, number]) => Math.random() * (max - min) + min;

function makeLayers(width: number, height: number): ParticleLayer[] {
  return LAYERS.map((config) => ({
    blur: config.blur,
    particles: Array.from({
      length: Math.max(1, Math.round(width * height * config.density)),
    }).map(() => ({
      x: Math.random(),
      y: Math.random(),
      size: between(config.size),
      speed: between(config.speed),
      alpha: between(config.alpha),
      phase: Math.random() * Math.PI * 2,
      twinkle: between([0.35, 0.8]),
    })),
  }));
}

export function BackgroundParticles() {
  const canvasRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    const context = canvas?.getContext("2d");
    if (!canvas || !context) return;

    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let width = 1;
    let height = 1;
    let layers: ParticleLayer[] = [];
    let animationFrame = 0;
    let lastTimestamp = 0;

    const draw = (timestamp: number, advance: boolean) => {
      const elapsed = lastTimestamp === 0 ? 0 : Math.min((timestamp - lastTimestamp) / 1000, 0.05);
      lastTimestamp = timestamp;
      context.clearRect(0, 0, width, height);
      context.globalCompositeOperation = "lighter";

      for (const layer of layers) {
        context.shadowBlur = layer.blur;
        for (const particle of layer.particles) {
          if (advance) {
            particle.y -= particle.speed * elapsed;
            particle.x -= particle.speed * elapsed * 0.08;
            if (particle.y < -0.02) particle.y = 1.02;
            if (particle.x < -0.02) particle.x = 1.02;
          }

          const shimmer =
            particle.alpha *
            (0.58 +
              0.42 * ((Math.sin(timestamp * 0.001 * particle.twinkle + particle.phase) + 1) / 2));
          context.beginPath();
          context.fillStyle = `rgba(216, 236, 248, ${shimmer})`;
          context.shadowColor = `rgba(174, 207, 242, ${shimmer})`;
          context.arc(particle.x * width, particle.y * height, particle.size, 0, Math.PI * 2);
          context.fill();
        }
      }

      context.globalCompositeOperation = "source-over";
      context.shadowBlur = 0;
    };

    const animate = (timestamp: number) => {
      draw(timestamp, true);
      animationFrame = window.requestAnimationFrame(animate);
    };

    const resize = () => {
      const rect = canvas.getBoundingClientRect();
      width = Math.max(1, rect.width);
      height = Math.max(1, rect.height);
      const pixelRatio = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.round(width * pixelRatio);
      canvas.height = Math.round(height * pixelRatio);
      context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0);
      layers = makeLayers(width, height);
      draw(0, false);
    };

    const observer = new ResizeObserver(resize);
    observer.observe(canvas);
    resize();
    if (!reducedMotion) animationFrame = window.requestAnimationFrame(animate);

    return () => {
      window.cancelAnimationFrame(animationFrame);
      observer.disconnect();
    };
  }, []);

  return <canvas ref={canvasRef} className="stage-particles" aria-hidden="true" />;
}
