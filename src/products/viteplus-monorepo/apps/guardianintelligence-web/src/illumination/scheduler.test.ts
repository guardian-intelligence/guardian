import { describe, expect, it, vi } from "vitest";
import { type AnimationFrameClock, WakeScheduler } from "./scheduler";

function makeClock() {
  const callbacks = new Map<number, FrameRequestCallback>();
  let nextFrame = 1;
  const clock: AnimationFrameClock = {
    cancel: (frame) => callbacks.delete(frame),
    request: (callback) => {
      const frame = nextFrame;
      nextFrame += 1;
      callbacks.set(frame, callback);
      return frame;
    },
  };
  return {
    clock,
    flush() {
      const pending = [...callbacks.entries()];
      callbacks.clear();
      pending.forEach(([, callback]) => callback(0));
    },
    pending: () => callbacks.size,
  };
}

describe("WakeScheduler", () => {
  it("coalesces wakes and returns to idle after one settled frame", () => {
    const frameClock = makeClock();
    const step = vi.fn(() => false);
    const states: string[] = [];
    const scheduler = new WakeScheduler(step, (state) => states.push(state), frameClock.clock);

    scheduler.wake();
    scheduler.wake();
    expect(frameClock.pending()).toBe(1);
    frameClock.flush();

    expect(step).toHaveBeenCalledOnce();
    expect(states).toEqual(["scheduled", "idle"]);
    expect(frameClock.pending()).toBe(0);
  });

  it("continues only while the renderer reports unsettled work", () => {
    const frameClock = makeClock();
    const step = vi.fn().mockReturnValueOnce(true).mockReturnValueOnce(false);
    const scheduler = new WakeScheduler(step, () => {}, frameClock.clock);

    scheduler.wake();
    frameClock.flush();
    expect(frameClock.pending()).toBe(1);
    frameClock.flush();

    expect(step).toHaveBeenCalledTimes(2);
    expect(scheduler.state).toBe("idle");
  });

  it("cancels scheduled work while suspended", () => {
    const frameClock = makeClock();
    const step = vi.fn(() => false);
    const scheduler = new WakeScheduler(step, () => {}, frameClock.clock);

    scheduler.wake();
    scheduler.suspend();
    frameClock.flush();

    expect(step).not.toHaveBeenCalled();
    expect(scheduler.state).toBe("suspended");
  });
});
