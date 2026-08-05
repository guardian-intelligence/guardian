import { createContext, useContext, type ReactNode } from "react";
import {
  COMPANY_SCENE_TIMING,
  companySceneNextWakeDelayMs,
  companySceneShimmerState,
  sampleCompanyScene,
  type CompanySceneFrame,
} from "./company-scene-timeline";

type SceneListener = (frame: CompanySceneFrame) => void;

const FRAME_PROPERTIES = {
  ambient: "--company-ambient-light",
  beacon: "--company-beacon-opacity",
  copy: "--company-copy-opacity",
  eyebrow: "--company-eyebrow-opacity",
  ledeRules: "--company-lede-rule-progress",
  materialization: "--company-materialize-progress",
  nodes: "--company-node-opacity",
  pencil: "--company-pencil-opacity",
  rails: "--company-rail-progress",
  title: "--company-title-opacity",
} as const;

export class CompanySceneController {
  readonly #listeners = new Set<SceneListener>();
  #active = true;
  #elapsedMs = 0;
  #element: HTMLElement | null = null;
  #frame = sampleCompanyScene(0);
  #frameRequest = 0;
  #frozen = false;
  #lastTimestamp = 0;
  #started = false;
  #timeout = 0;

  attach(element: HTMLElement | null) {
    this.#element = element;
    this.#applyFrame();
  }

  currentFrame() {
    return this.#frame;
  }

  subscribe(listener: SceneListener) {
    this.#listeners.add(listener);
    listener(this.#frame);
    return () => this.#listeners.delete(listener);
  }

  start() {
    this.#cancelScheduledWork();
    this.#elapsedMs = 0;
    this.#frozen = false;
    this.#started = true;
    this.#lastTimestamp = performance.now();
    this.#updateFrame();
    this.#schedule();
  }

  finish() {
    this.#cancelScheduledWork();
    this.#elapsedMs = COMPANY_SCENE_TIMING.introEndMs;
    this.#frozen = true;
    this.#started = false;
    this.#frame = { ...sampleCompanyScene(this.#elapsedMs), shimmerProgress: null };
    this.#emit();
  }

  seek(elapsedMs: number) {
    this.#cancelScheduledWork();
    this.#elapsedMs = Math.max(0, elapsedMs);
    this.#frozen = true;
    this.#started = true;
    this.#updateFrame();
  }

  setActive(active: boolean) {
    if (this.#active === active) return;
    if (!active && this.#started && !this.#frozen) this.#advance(performance.now());
    this.#active = active;
    this.#cancelScheduledWork();
    if (!active) return;
    this.#lastTimestamp = performance.now();
    this.#emit();
    this.#schedule();
  }

  dispose() {
    this.#cancelScheduledWork();
    this.#listeners.clear();
    this.#element = null;
  }

  readonly #tick = (timestamp: number) => {
    this.#frameRequest = 0;
    this.#timeout = 0;
    if (!this.#active || !this.#started || this.#frozen) return;
    this.#advance(timestamp);
    this.#updateFrame();
    this.#schedule();
  };

  #advance(timestamp: number) {
    this.#elapsedMs += Math.max(0, timestamp - this.#lastTimestamp);
    this.#lastTimestamp = timestamp;
  }

  #updateFrame() {
    this.#frame = sampleCompanyScene(this.#elapsedMs);
    this.#emit();
  }

  #emit() {
    this.#applyFrame();
    for (const listener of this.#listeners) listener(this.#frame);
  }

  #applyFrame() {
    if (!this.#element) return;
    for (const [key, property] of Object.entries(FRAME_PROPERTIES) as Array<
      [keyof typeof FRAME_PROPERTIES, string]
    >) {
      this.#element.style.setProperty(property, this.#frame[key].toFixed(4));
    }
    this.#element.dataset.sceneAmbient = this.#frame.ambient.toFixed(4);
    this.#element.dataset.sceneBeacon = this.#frame.beacon.toFixed(4);
    this.#element.dataset.sceneElapsedMs = this.#frame.elapsedMs.toFixed(0);
    this.#element.dataset.scenePhase = this.#frame.phase;
    this.#element.dataset.sceneShimmer = this.#frame.shimmerProgress?.toFixed(4) ?? "idle";
    const shimmerState = companySceneShimmerState(this.#frame.elapsedMs);
    const introRemainingMs = Math.max(0, COMPANY_SCENE_TIMING.introEndMs - this.#frame.elapsedMs);
    this.#element.dataset.sceneShimmerCycle = String(shimmerState.cycleIndex);
    this.#element.dataset.sceneShimmerDelayMs = shimmerState.delayMs.toFixed(0);
    this.#element.dataset.sceneNextShimmerMs = (
      introRemainingMs + shimmerState.nextWakeDelayMs
    ).toFixed(0);
  }

  #schedule() {
    if (!this.#active || !this.#started || this.#frozen) return;
    const delay = companySceneNextWakeDelayMs(this.#elapsedMs);
    if (delay <= 16) {
      this.#frameRequest = window.requestAnimationFrame(this.#tick);
      return;
    }
    this.#timeout = window.setTimeout(() => this.#tick(performance.now()), delay);
  }

  #cancelScheduledWork() {
    if (this.#frameRequest) window.cancelAnimationFrame(this.#frameRequest);
    if (this.#timeout) window.clearTimeout(this.#timeout);
    this.#frameRequest = 0;
    this.#timeout = 0;
  }
}

const CompanySceneContext = createContext<CompanySceneController | null>(null);

export function CompanySceneProvider({
  children,
  controller,
}: {
  readonly children: ReactNode;
  readonly controller: CompanySceneController;
}) {
  return <CompanySceneContext.Provider value={controller}>{children}</CompanySceneContext.Provider>;
}

export function useCompanyScene() {
  const controller = useContext(CompanySceneContext);
  if (!controller) throw new Error("Hero materialization requires a company scene controller");
  return controller;
}
