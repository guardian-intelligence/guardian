export type SchedulerState = "idle" | "scheduled" | "suspended";

export interface AnimationFrameClock {
  cancel(frame: number): void;
  request(callback: FrameRequestCallback): number;
}

const browserClock: AnimationFrameClock = {
  cancel: (frame) => window.cancelAnimationFrame(frame),
  request: (callback) => window.requestAnimationFrame(callback),
};

export class WakeScheduler {
  readonly #clock: AnimationFrameClock;
  readonly #onStateChange: (state: SchedulerState) => void;
  readonly #step: () => boolean;
  #frame = 0;
  #state: SchedulerState = "idle";

  constructor(
    step: () => boolean,
    onStateChange: (state: SchedulerState) => void,
    clock = browserClock,
  ) {
    this.#clock = clock;
    this.#onStateChange = onStateChange;
    this.#step = step;
  }

  get state() {
    return this.#state;
  }

  wake() {
    if (this.#state === "suspended" || this.#frame !== 0) return;
    this.#setState("scheduled");
    this.#frame = this.#clock.request(this.#run);
  }

  suspend() {
    if (this.#frame !== 0) this.#clock.cancel(this.#frame);
    this.#frame = 0;
    this.#setState("suspended");
  }

  resume() {
    if (this.#state !== "suspended") return;
    this.#setState("idle");
  }

  dispose() {
    if (this.#frame !== 0) this.#clock.cancel(this.#frame);
    this.#frame = 0;
    this.#setState("idle");
  }

  readonly #run = () => {
    this.#frame = 0;
    if (this.#state === "suspended") return;
    if (this.#step()) {
      this.#frame = this.#clock.request(this.#run);
      return;
    }
    this.#setState("idle");
  };

  #setState(state: SchedulerState) {
    if (this.#state === state) return;
    this.#state = state;
    this.#onStateChange(state);
  }
}
