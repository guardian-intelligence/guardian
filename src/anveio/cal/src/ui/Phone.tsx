import { useEffect, useRef, useState, type CSSProperties, type PointerEvent } from "react";

import { LINES, NOTE, type Stage } from "../content.ts";
import { useFlow, type CardKind } from "../flow.ts";
import { Glass, readShapes } from "../glass/Glass.tsx";
import { getDevice, hexToRgb } from "../gpu/gpu.ts";
import { Scene, type GlassShape } from "../gpu/scene.ts";
import { light } from "../rumi/light.ts";
import { Orb } from "../rumi/Orb.tsx";
import blitShader from "../shaders/blit.wgsl?raw";
import blurShader from "../shaders/blur.wgsl?raw";
import glassShader from "../shaders/glass.wgsl?raw";
import inkShader from "../shaders/ink.wgsl?raw";
import { tuning, useTuning } from "../studio/store.ts";
import { Recently } from "./Recently.tsx";

const SHADERS = { ink: inkShader, blur: blurShader, blit: blitShader, glass: glassShader };

function Invitation({ kind, host }: { kind: CardKind; host: boolean }) {
  const draft = kind === "draft1" || kind === "draft2";
  const thursday = kind === "draft2" || kind === "thu";
  return (
    <section className="enter">
      <h2 className="section-header t-foot">Invitation</h2>
      <div className="cell">
        <div className="invite-head">
          <div className="invite-who">
            <div className="t-headline">Samantha &amp; Shovon</div>
            <div className="t-sub secondary">to talk open source</div>
          </div>
          <span className={draft ? "pill pill-draft" : "pill pill-booked"}>
            {draft ? "Drafting" : "Booked"}
          </span>
        </div>
        <div className="hairline" />
        <div className="invite-when">
          {kind === "draft2" && <div className="t-sub struck">Friday, October 9 · 2:30 PM</div>}
          <div className={draft ? "t-body secondary" : "t-body"}>
            {thursday ? "Thursday, October 8" : "Friday, October 9"}
          </div>
          <div className="t-sub secondary">2:30 – 3:00 PM · Zoom</div>
        </div>
      </div>
      <p className="section-footer t-foot">
        {host ? "Only Samantha can change this." : "Only you and Shovon can see this."}
      </p>
    </section>
  );
}

function Thread() {
  const rumi = (text: string) => (
    <div className="msg-rumi">
      <span className="t-foot msg-name">Rumi</span>
      <p className="t-body">{text}</p>
    </div>
  );
  const voice = (playing: boolean) => (
    <div className="msg-voice">
      <span className="t-foot msg-name">Samantha</span>
      <button
        type="button"
        className="voice"
        aria-label={`${playing ? "Pause" : "Play"} Samantha's voice message`}
      >
        <svg width="38" height="38" viewBox="0 0 40 40" aria-hidden="true">
          <circle cx="20" cy="20" r="18" fill="none" stroke="#48484A" strokeWidth="2.5" />
          <circle
            className={playing ? "voice-prog playing" : "voice-prog"}
            cx="20"
            cy="20"
            r="18"
            fill="none"
            stroke="#0A84FF"
            strokeWidth="2.5"
            strokeLinecap="round"
            strokeDasharray="113.1"
            transform="rotate(-90 20 20)"
          />
          <path
            d={playing ? "M15 13h3.5v14H15zM21.5 13H25v14h-3.5z" : "M16.5 13.5 L27 20 L16.5 26.5 Z"}
            fill="#FFFFFF"
          />
        </svg>
        <span className="t-sub">Voice message</span>
      </button>
    </div>
  );
  const day = (time: string) => (
    <div className="t-cap msg-day">
      <b>Wed, Oct 7</b> at {time}
    </div>
  );
  return (
    <section>
      <h2 className="section-header t-foot">Messages</h2>
      <div className="cell thread">
        {day("1:05 PM")}
        {rumi(LINES.greet)}
        <div className="t-cap msg-note">Samantha signed in with Google</div>
        {voice(false)}
        {rumi(LINES.booked)}
        <div className="t-cap msg-note struck">Friday, October 9 · 2:30 PM</div>
        {day("4:12 PM")}
        {rumi(LINES.greet)}
        {voice(true)}
        {rumi(LINES.rebooked)}
        <div className="t-cap msg-note">Thursday, October 8 · 2:30 PM · Booked</div>
      </div>
    </section>
  );
}

// The waveform glyph iOS puts at the end of a message field for dictation.
const MicIcon = () => (
  <svg
    width="20"
    height="20"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="2"
    strokeLinecap="round"
    aria-hidden="true"
  >
    <path d="M4 10.5v3M8 8v8M12 4.5v15M16 8v8M20 10.5v3" />
  </svg>
);

const GoogleIcon = () => (
  <svg width="18" height="18" viewBox="0 0 24 24" aria-hidden="true">
    <path
      d="M21.6 12.2c0-.7-.1-1.4-.2-2H12v3.8h5.4a4.6 4.6 0 0 1-2 3v2.5h3.2c1.9-1.7 3-4.3 3-7.3z"
      fill="#4285F4"
    />
    <path
      d="M12 22c2.7 0 5-.9 6.6-2.4l-3.2-2.5c-.9.6-2 1-3.4 1-2.6 0-4.8-1.8-5.6-4.1H3.1v2.6A10 10 0 0 0 12 22z"
      fill="#34A853"
    />
    <path d="M6.4 14a6 6 0 0 1 0-3.9V7.5H3.1a10 10 0 0 0 0 9z" fill="#FBBC05" />
    <path
      d="M12 6c1.5 0 2.8.5 3.8 1.5l2.9-2.9A10 10 0 0 0 3.1 7.5l3.3 2.6C7.2 7.8 9.4 6 12 6z"
      fill="#EA4335"
    />
  </svg>
);

// The page under each glass shape is blurred by a backdrop-filter box that sits
// below the optics overlay (a blur on the glass element itself would blur the
// optics too). Kept in step with the shapes every frame.
function syncUnderlay(layer: HTMLDivElement, shapes: readonly GlassShape[]): void {
  while (layer.children.length < shapes.length) layer.appendChild(document.createElement("div"));
  while (layer.children.length > shapes.length) layer.lastElementChild?.remove();
  shapes.forEach((s, i) => {
    const el = layer.children[i];
    if (!(el instanceof HTMLDivElement)) return;
    el.style.left = `${s.cx - s.hw}px`;
    el.style.top = `${s.cy - s.hh}px`;
    el.style.width = `${s.hw * 2}px`;
    el.style.height = `${s.hh * 2}px`;
    el.style.borderRadius = `${s.radius}px`;
  });
}

type Props = { forced: Stage | null; restartKey: number; onGpuError: (msg: string | null) => void };

export function Phone({ forced, restartKey, onGpuError }: Props) {
  const rootRef = useRef<HTMLDivElement>(null);
  const bgRef = useRef<HTMLCanvasElement>(null);
  const glassRef = useRef<HTMLCanvasElement>(null);
  const underlayRef = useRef<HTMLDivElement>(null);
  const sceneRef = useRef<Scene | null>(null);
  const [gpu, setGpu] = useState<"pending" | "on" | "off">("pending");
  const [muted, setMuted] = useState(true);
  const t = useTuning();

  const ripple = (x: number, y: number, amp: number) => sceneRef.current?.ripple(x, y, amp);
  const orbRef = useRef<HTMLDivElement>(null);
  const flow = useFlow(forced);
  const speakingRef = useRef(false);
  speakingRef.current = flow.speaking;
  const { restart } = flow;
  useEffect(() => {
    if (restartKey > 0) restart();
  }, [restartKey, restart]);

  useEffect(() => {
    let raf = 0;
    let dead = false;
    void getDevice().then(async (device) => {
      const root = rootRef.current;
      const bg = bgRef.current;
      const glass = glassRef.current;
      if (dead || !root || !bg || !glass) return;
      if (!device) {
        setGpu("off");
        return;
      }
      const scene = await Scene.create(device, bg, glass, SHADERS).catch((err: unknown) => {
        onGpuError(err instanceof Error ? err.message : String(err));
        return null;
      });
      if (!scene || dead) {
        scene?.dispose();
        if (!scene) setGpu("off");
        return;
      }
      sceneRef.current = scene;
      setGpu("on");
      const tick = () => {
        raf = requestAnimationFrame(tick);
        const shapes = readShapes(root);
        if (underlayRef.current) syncUnderlay(underlayRef.current, shapes);
        const orb = orbRef.current;
        if (orb) {
          const r = root.getBoundingClientRect();
          const o = orb.getBoundingClientRect();
          scene.setVoice(
            o.left - r.left + o.width / 2,
            o.top - r.top + o.height / 2,
            speakingRef.current,
          );
        }
        scene.frame(
          tuning.get(),
          shapes,
          root.clientWidth,
          root.clientHeight,
          Math.min(devicePixelRatio, 2),
        );
      };
      tick();
    });
    return () => {
      dead = true;
      cancelAnimationFrame(raf);
      sceneRef.current?.dispose();
      sceneRef.current = null;
    };
    // mount once; shader edits are applied by the effect below
  }, [onGpuError]);

  // Rumi's candle also lights the message field; its shine reads these vars.
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    return light.subscribe((f) => {
      root.style.setProperty("--flame", f.brightness.toFixed(4));
      root.style.setProperty("--flame-size", f.size.toFixed(4));
      root.style.setProperty("--flame-x", f.swayX.toFixed(4));
      root.style.setProperty("--flame-y", f.swayY.toFixed(4));
    });
  }, []);

  // Editing any .wgsl file hot-reloads it into the running scene.
  useEffect(() => {
    void sceneRef.current?.setShaders(SHADERS).then(onGpuError);
  }, [inkShader, blurShader, blitShader, glassShader, onGpuError]);

  // Tap ripples are a debugging aid only (Studio → Ink → Tap ripples).
  const press = (e: PointerEvent<HTMLDivElement>) => {
    if (!tuning.get().ink.tapRipples) return;
    const r = e.currentTarget.getBoundingClientRect();
    ripple(e.clientX - r.left, e.clientY - r.top, 3);
  };

  const glassVars: CSSProperties & Record<`--${string}`, string> = {
    "--glass-blur": `${t.glass.domBlur}px`,
    "--raise": String(t.glass.raise),
    "--orb-light": String(t.glass.orbLight),
    "--orb-rgb": hexToRgb(t.orb.glow)
      .map((c) => Math.round(c * 255))
      .join(" "),
  };

  return (
    <div ref={rootRef} className={`phone gpu-${gpu}`} onPointerDown={press} style={glassVars}>
      <canvas ref={bgRef} className="layer-ink" aria-hidden="true" />

      <div className="scroll">
        {!flow.host && (
          <header className="hero">
            <img src="/shovon.jpg" alt="Shovon Hasan" width={120} height={120} />
            <h1 className="t-title1">Shovon Hasan</h1>
            <p className="t-sub secondary">Founder, Guardian</p>
          </header>
        )}
        {flow.host && (
          <>
            <header className="hero">
              <div className="monogram" aria-hidden="true">
                S
              </div>
              <h1 className="t-title1">Samantha</h1>
              <p className="t-sub secondary">Your thread, kept by Rumi</p>
            </header>
            <Thread />
          </>
        )}
        {flow.card && <Invitation key={flow.card} kind={flow.card} host={flow.host} />}
        {!flow.host && (
          <>
            <div className="cell note">
              <div className="t-foot secondary">Note</div>
              <p className="t-body">{NOTE}</p>
            </div>
            <Recently />
          </>
        )}
      </div>

      <div ref={underlayRef} className="underlay" aria-hidden="true" />
      <canvas ref={glassRef} className="layer-glass" aria-hidden="true" />

      <div className="controls">
        <Glass key={flow.line} radius={18} className="bubble enter">
          <div className="t-body" role="status" aria-live="polite">
            <span>{flow.shown}</span>
            <span className="unsaid">{flow.rest}</span>
          </div>
        </Glass>

        <div className="bar">
          <div ref={orbRef}>
            <div className="orb-button">
              <button
                type="button"
                className="tap"
                aria-label={muted ? "Unmute Rumi" : "Mute Rumi"}
                aria-pressed={muted}
                onClick={() => setMuted(!muted)}
              >
                <Orb mode={flow.orbMode} muted={muted} size={t.orb.size} />
              </button>
            </div>
          </div>

          {flow.control === "idle" && (
            <Glass radius="capsule" className="field">
              <button type="button" className="tap field-tap t-body" onClick={flow.leave}>
                <span>Leave a message</span>
                <span className="field-icon">
                  <MicIcon />
                </span>
              </button>
            </Glass>
          )}
          {flow.control === "signin" && (
            <button type="button" className="google t-headline" onClick={flow.signIn}>
              <GoogleIcon />
              Continue with Google
            </button>
          )}
          {flow.control === "recording" && (
            <>
              <Glass radius="capsule" className="field">
                <button
                  type="button"
                  className="tap field-tap t-body"
                  aria-label="Send message"
                  onClick={flow.send}
                >
                  <span className="rec-dot" />
                  <span className="field-label">Listening…</span>
                  <span className="send">
                    <svg
                      width="16"
                      height="16"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="3"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      aria-hidden="true"
                    >
                      <path d="M12 19V5M6 11l6-6 6 6" />
                    </svg>
                  </span>
                </button>
              </Glass>
              <Glass radius="capsule" className="discard">
                <button
                  type="button"
                  className="tap"
                  aria-label="Discard this message"
                  onClick={flow.cancel}
                >
                  <svg
                    width="14"
                    height="14"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2.6"
                    strokeLinecap="round"
                    aria-hidden="true"
                  >
                    <path d="M6 6l12 12M18 6L6 18" />
                  </svg>
                </button>
              </Glass>
            </>
          )}
          {flow.control === "thinking" && (
            <Glass radius="capsule" className="field">
              <button type="button" className="tap field-tap t-body secondary" disabled>
                Rumi is on it…
              </button>
            </Glass>
          )}
          {flow.control === "host" && (
            <Glass radius="capsule" className="field host-note">
              <p className="t-foot secondary">Only Samantha can leave messages here.</p>
            </Glass>
          )}
        </div>
      </div>
    </div>
  );
}
