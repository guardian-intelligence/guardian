import { describe, expect, it } from "vitest";
import { dogPayload, Ev } from "@guardian/chunkies-testkit";
import { wumRig } from "./rig.ts";

describe("the game read surface", () => {
  it("hands a renderer the world and the terrain planes", async () => {
    const r = await wumRig();
    await r.establish();
    r.deliver([r.emit(Ev.join, dogPayload(0x51n))]);
    await r.until(() => r.state.seq === r.authority.seq);
    const view = r.frame();
    expect(view).not.toBeNull();
    expect(view!.terrain).not.toBeNull();
    expect(
      new DataView(view!.viewBytes.buffer, view!.viewBytes.byteOffset).getUint32(0, true),
    ).toBe(r.state.dogCount);
    expect(view!.phaseQ16).toBeGreaterThanOrEqual(0);
    expect(view!.phaseQ16).toBeLessThanOrEqual(65536);
  });

  it("keeps the reactive lane low-frequency: hud state is reference-stable while nothing changes", async () => {
    const r = await wumRig();
    await r.establish();
    await r.run(100);
    const before = r.game.state.hud;
    expect(before).not.toBeNull();
    await r.run(50);
    // The external-store contract: a snapshot only changes identity when a
    // field changed, so a subscriber diffing references sees quiet frames
    // as quiet.
    expect(r.game.state.hud).toBe(before);
  });
});
